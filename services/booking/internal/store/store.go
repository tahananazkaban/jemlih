// Package store persists bookings and per-Unit calendars in DynamoDB.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/booking/internal/domain"
)

const (
	bookingPrefix  = "BOOKING#"
	calendarPrefix = "CALENDAR#"
	// maxBatchGet is the BatchGetItem key limit.
	maxBatchGet = 100
)

// ErrConcurrentChange means another writer changed the Unit's calendar or the Booking since it was read; retry.
var ErrConcurrentChange = errors.New("changed concurrently")

// maxBatchGetAttempts bounds retries of keys BatchGetItem leaves unprocessed under throttling.
const maxBatchGetAttempts = 5

// Store keeps Bookings and Calendars of every Rental in one table: PK=RENTAL#<id>.
type Store struct {
	db    *dynamodb.Client
	table string
}

func New(db *dynamodb.Client, table string) *Store {
	return &Store{db: db, table: table}
}

func rentalKey(rentalID string) string { return "RENTAL#" + rentalID }

func key(rentalID, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// Calendar reads a Unit's calendar with a strongly consistent read; a missing one is empty at version 0.
func (s *Store) Calendar(ctx context.Context, rentalID, unitID string) (domain.Calendar, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            key(rentalID, calendarPrefix+unitID),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Calendar{}, fmt.Errorf("get calendar: %w", err)
	}
	if out.Item == nil {
		return domain.Calendar{UnitID: unitID, Reservations: map[string]domain.Reservation{}}, nil
	}
	return calendarFromItem(out.Item)
}

// Calendars reads many Units' calendars with strongly consistent reads.
func (s *Store) Calendars(ctx context.Context, rentalID string, unitIDs []string) (map[string]domain.Calendar, error) {
	cals := map[string]domain.Calendar{}
	for _, id := range unitIDs {
		cals[id] = domain.Calendar{UnitID: id, Reservations: map[string]domain.Reservation{}}
	}
	for start := 0; start < len(unitIDs); start += maxBatchGet {
		keys := []map[string]types.AttributeValue{}
		for _, id := range unitIDs[start:min(start+maxBatchGet, len(unitIDs))] {
			keys = append(keys, key(rentalID, calendarPrefix+id))
		}
		request := map[string]types.KeysAndAttributes{s.table: {Keys: keys, ConsistentRead: aws.Bool(true)}}
		for attempt := 0; len(request) > 0; attempt++ {
			if attempt == maxBatchGetAttempts {
				return nil, errors.New("get calendars: keys still unprocessed after retries")
			}
			if attempt > 0 {
				time.Sleep(time.Duration(50<<attempt) * time.Millisecond)
			}
			out, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: request})
			if err != nil {
				return nil, fmt.Errorf("get calendars: %w", err)
			}
			for _, item := range out.Responses[s.table] {
				cal, err := calendarFromItem(item)
				if err != nil {
					return nil, err
				}
				cals[cal.UnitID] = cal
			}
			// Throttled keys come back unprocessed and must be asked for again.
			request = out.UnprocessedKeys
		}
	}
	return cals, nil
}

// Create stores the Booking, the Unit's next calendar and the created event, failing with ErrConcurrentChange on a race.
func (s *Store) Create(ctx context.Context, rentalID string, b domain.Booking, next domain.Calendar, created events.Envelope) error {
	booking, err := attributevalue.MarshalMap(toBookingItem(rentalID, b))
	if err != nil {
		return fmt.Errorf("marshal booking: %w", err)
	}
	calendar, err := s.calendarPut(rentalID, next)
	if err != nil {
		return err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: &s.table, Item: booking, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
		calendar,
		events.OutboxPut(s.table, created),
	}})
	// A failed condition on the new Booking means its ID already exists: a bug, not a race.
	return transactionError("create booking", err, false)
}

// Cancel stores the cancelled Booking, the Unit's next calendar and the cancelled event.
func (s *Store) Cancel(ctx context.Context, rentalID string, b domain.Booking, next domain.Calendar, cancelled events.Envelope) error {
	booking, err := attributevalue.MarshalMap(toBookingItem(rentalID, b))
	if err != nil {
		return fmt.Errorf("marshal booking: %w", err)
	}
	calendar, err := s.calendarPut(rentalID, next)
	if err != nil {
		return err
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{
			TableName:                 &s.table,
			Item:                      booking,
			ConditionExpression:       aws.String("#status = :held"),
			ExpressionAttributeNames:  map[string]string{"#status": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":held": &types.AttributeValueMemberS{Value: string(domain.StatusHeld)}},
		}},
		calendar,
		events.OutboxPut(s.table, cancelled),
	}})
	// A failed condition on the Booking means someone else cancelled it first.
	return transactionError("cancel booking", err, true)
}

// calendarPut writes next at Version+1, only if the stored calendar is still at next.Version.
func (s *Store) calendarPut(rentalID string, next domain.Calendar) (types.TransactWriteItem, error) {
	item, err := attributevalue.MarshalMap(toCalendarItem(rentalID, next, next.Version+1))
	if err != nil {
		return types.TransactWriteItem{}, fmt.Errorf("marshal calendar: %w", err)
	}
	put := &types.Put{TableName: &s.table, Item: item}
	if next.Version == 0 {
		put.ConditionExpression = aws.String("attribute_not_exists(PK)")
	} else {
		put.ConditionExpression = aws.String("version = :read")
		put.ExpressionAttributeValues = map[string]types.AttributeValue{
			":read": &types.AttributeValueMemberN{Value: strconv.FormatInt(next.Version, 10)},
		}
	}
	return types.TransactWriteItem{Put: put}, nil
}

// transactionError maps lost races to ErrConcurrentChange: the calendar's condition (item 1),
// transactions colliding on the same items, throttling, and optionally the Booking's condition (item 0).
func transactionError(op string, err error, bookingRace bool) error {
	if err == nil {
		return nil
	}
	var inProgress *types.TransactionInProgressException
	if errors.As(err, &inProgress) {
		return ErrConcurrentChange
	}
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for i, r := range canceled.CancellationReasons {
			switch code := aws.ToString(r.Code); {
			case code == "TransactionConflict" || code == "ThrottlingError" || code == "ProvisionedThroughputExceeded":
				return ErrConcurrentChange
			case code == "ConditionalCheckFailed" && (i == 1 || (i == 0 && bookingRace)):
				return ErrConcurrentChange
			}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// Get returns one Booking of the Rental, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, rentalID, id string) (domain.Booking, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            key(rentalID, bookingPrefix+id),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Booking{}, fmt.Errorf("get booking: %w", err)
	}
	if out.Item == nil {
		return domain.Booking{}, domain.ErrNotFound
	}
	return bookingFromItem(out.Item)
}

// List returns every Booking of the Rental, newest first.
func (s *Store) List(ctx context.Context, rentalID string) ([]domain.Booking, error) {
	paginator := dynamodb.NewQueryPaginator(s.db, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
			":prefix": &types.AttributeValueMemberS{Value: bookingPrefix},
		},
		ScanIndexForward: aws.Bool(false),
	})
	bookings := []domain.Booking{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list bookings: %w", err)
		}
		for _, item := range page.Items {
			b, err := bookingFromItem(item)
			if err != nil {
				return nil, err
			}
			bookings = append(bookings, b)
		}
	}
	return bookings, nil
}

type reservationItem struct {
	PickupAt      string `dynamodbav:"pickup_at"`
	ReturnAt      string `dynamodbav:"return_at"`
	HoldExpiresAt string `dynamodbav:"hold_expires_at,omitempty"`
}

type calendarItem struct {
	PK           string                     `dynamodbav:"PK"`
	SK           string                     `dynamodbav:"SK"`
	UnitID       string                     `dynamodbav:"unit_id"`
	Version      int64                      `dynamodbav:"version"`
	Reservations map[string]reservationItem `dynamodbav:"reservations"`
}

func toCalendarItem(rentalID string, c domain.Calendar, version int64) calendarItem {
	item := calendarItem{
		PK: rentalKey(rentalID), SK: calendarPrefix + c.UnitID, UnitID: c.UnitID, Version: version,
		Reservations: map[string]reservationItem{},
	}
	for id, r := range c.Reservations {
		item.Reservations[id] = reservationItem{
			PickupAt:      formatTime(r.Window.PickupAt),
			ReturnAt:      formatTime(r.Window.ReturnAt),
			HoldExpiresAt: formatTime(r.HoldExpiresAt),
		}
	}
	return item
}

func calendarFromItem(av map[string]types.AttributeValue) (domain.Calendar, error) {
	var item calendarItem
	if err := attributevalue.UnmarshalMap(av, &item); err != nil {
		return domain.Calendar{}, fmt.Errorf("unmarshal calendar: %w", err)
	}
	cal := domain.Calendar{UnitID: item.UnitID, Version: item.Version, Reservations: map[string]domain.Reservation{}}
	for id, r := range item.Reservations {
		var res domain.Reservation
		var err error
		if res.Window.PickupAt, err = parseTime(r.PickupAt); err != nil {
			return domain.Calendar{}, err
		}
		if res.Window.ReturnAt, err = parseTime(r.ReturnAt); err != nil {
			return domain.Calendar{}, err
		}
		if res.HoldExpiresAt, err = parseTime(r.HoldExpiresAt); err != nil {
			return domain.Calendar{}, err
		}
		cal.Reservations[id] = res
	}
	return cal, nil
}

type bookingItem struct {
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	ID              string `dynamodbav:"id"`
	UnitID          string `dynamodbav:"unit_id"`
	UnitModel       string `dynamodbav:"unit_model"`
	UnitPlateNumber string `dynamodbav:"unit_plate_number"`
	RenterID        string `dynamodbav:"renter_id"`
	RenterEmail     string `dynamodbav:"renter_email"`
	PickupAt        string `dynamodbav:"pickup_at"`
	ReturnAt        string `dynamodbav:"return_at"`
	DailyPrice      int64  `dynamodbav:"daily_price"`
	HourlyPrice     int64  `dynamodbav:"hourly_price"`
	SecurityDeposit int64  `dynamodbav:"security_deposit"`
	Days            int64  `dynamodbav:"days"`
	ExtraHours      int64  `dynamodbav:"extra_hours"`
	ExtraHoursFee   int64  `dynamodbav:"extra_hours_fee"`
	RentalFee       int64  `dynamodbav:"rental_fee"`
	TotalAmount     int64  `dynamodbav:"total_amount"`
	Status          string `dynamodbav:"status"`
	HoldExpiresAt   string `dynamodbav:"hold_expires_at"`
	CreatedAt       string `dynamodbav:"created_at"`
	CancelledBy     string `dynamodbav:"cancelled_by,omitempty"`
	CancelReason    string `dynamodbav:"cancel_reason,omitempty"`
	CancelledAt     string `dynamodbav:"cancelled_at,omitempty"`
}

func toBookingItem(rentalID string, b domain.Booking) bookingItem {
	item := bookingItem{
		PK: rentalKey(rentalID), SK: bookingPrefix + b.ID, ID: b.ID,
		UnitID: b.Unit.ID, UnitModel: b.Unit.Model, UnitPlateNumber: b.Unit.PlateNumber,
		RenterID: b.RenterID, RenterEmail: b.RenterEmail,
		PickupAt: formatTime(b.Window.PickupAt), ReturnAt: formatTime(b.Window.ReturnAt),
		DailyPrice: b.Price.DailyPrice, HourlyPrice: b.Price.HourlyPrice, SecurityDeposit: b.Price.SecurityDeposit,
		Days: b.Price.Days, ExtraHours: b.Price.ExtraHours, ExtraHoursFee: b.Price.ExtraHoursFee, RentalFee: b.Price.RentalFee, TotalAmount: b.Price.TotalAmount,
		Status: string(b.Status), HoldExpiresAt: formatTime(b.HoldExpiresAt), CreatedAt: formatTime(b.CreatedAt),
	}
	if c := b.Cancellation; c != nil {
		item.CancelledBy, item.CancelReason, item.CancelledAt = c.By, c.Reason, formatTime(c.At)
	}
	return item
}

func bookingFromItem(av map[string]types.AttributeValue) (domain.Booking, error) {
	var item bookingItem
	if err := attributevalue.UnmarshalMap(av, &item); err != nil {
		return domain.Booking{}, fmt.Errorf("unmarshal booking: %w", err)
	}
	b := domain.Booking{
		ID:          item.ID,
		Unit:        domain.UnitSnapshot{ID: item.UnitID, Model: item.UnitModel, PlateNumber: item.UnitPlateNumber},
		RenterID:    item.RenterID,
		RenterEmail: item.RenterEmail,
		Price: domain.Price{
			Rates: domain.Rates{DailyPrice: item.DailyPrice, HourlyPrice: item.HourlyPrice, SecurityDeposit: item.SecurityDeposit},
			Days:  item.Days, ExtraHours: item.ExtraHours, ExtraHoursFee: item.ExtraHoursFee,
			RentalFee: item.RentalFee, TotalAmount: item.TotalAmount,
		},
		Status: domain.Status(item.Status),
	}
	var err error
	for _, f := range []struct {
		dst *time.Time
		src string
	}{
		{&b.Window.PickupAt, item.PickupAt}, {&b.Window.ReturnAt, item.ReturnAt},
		{&b.HoldExpiresAt, item.HoldExpiresAt}, {&b.CreatedAt, item.CreatedAt},
	} {
		if *f.dst, err = parseTime(f.src); err != nil {
			return domain.Booking{}, fmt.Errorf("booking %s: %w", item.ID, err)
		}
	}
	if item.CancelledAt != "" {
		at, err := parseTime(item.CancelledAt)
		if err != nil {
			return domain.Booking{}, fmt.Errorf("booking %s: %w", item.ID, err)
		}
		b.Cancellation = &domain.Cancellation{By: item.CancelledBy, Reason: item.CancelReason, At: at}
	}
	return b, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
}
