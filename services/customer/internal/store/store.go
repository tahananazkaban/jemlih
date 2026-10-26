// Package store persists Renters in DynamoDB, one versioned item per Renter.
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
	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
)

const renterPrefix = "RENTER#"

// ErrConcurrentChange means the Renter changed after it was read; read it again and retry.
var ErrConcurrentChange = errors.New("renter changed concurrently")

// Store keeps Renters in one table partitioned by Rental: PK=RENTAL#<id>, SK=RENTER#<user id>.
type Store struct {
	db    *dynamodb.Client
	table string
}

func New(db *dynamodb.Client, table string) *Store {
	return &Store{db: db, table: table}
}

type documentItem struct {
	Key         string `dynamodbav:"key"`
	ContentType string `dynamodbav:"content_type"`
	Size        int64  `dynamodbav:"size"`
	UploadedAt  string `dynamodbav:"uploaded_at"`
}

type rejectionItem struct {
	Reason    string   `dynamodbav:"reason"`
	Documents []string `dynamodbav:"documents"`
	By        string   `dynamodbav:"by"`
	At        string   `dynamodbav:"at"`
}

type renterItem struct {
	PK              string                  `dynamodbav:"PK"`
	SK              string                  `dynamodbav:"SK"`
	ID              string                  `dynamodbav:"id"`
	Email           string                  `dynamodbav:"email"`
	FullName        string                  `dynamodbav:"full_name,omitempty"`
	PhoneNumber     string                  `dynamodbav:"phone_number,omitempty"`
	Documents       map[string]documentItem `dynamodbav:"documents"`
	Status          string                  `dynamodbav:"status"`
	Rejection       *rejectionItem          `dynamodbav:"rejection,omitempty"`
	VerifiedBy      string                  `dynamodbav:"verified_by,omitempty"`
	VerifiedAt      string                  `dynamodbav:"verified_at,omitempty"`
	StatusChangedAt string                  `dynamodbav:"status_changed_at"`
	CreatedAt       string                  `dynamodbav:"created_at"`
	UpdatedAt       string                  `dynamodbav:"updated_at"`
	Version         int64                   `dynamodbav:"version"`
}

func rentalKey(rentalID string) string { return "RENTAL#" + rentalID }

func renterKey(rentalID, id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
		"SK": &types.AttributeValueMemberS{Value: renterPrefix + id},
	}
}

// Get returns the Renter with a strongly consistent read, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, rentalID, id string) (domain.Renter, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: renterKey(rentalID, id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return domain.Renter{}, fmt.Errorf("get renter: %w", err)
	}
	if out.Item == nil {
		return domain.Renter{}, domain.ErrNotFound
	}
	return fromItem(out.Item)
}

// Save writes r only if it is still at the version it was read at, with its events in the same transaction.
func (s *Store) Save(ctx context.Context, rentalID string, r domain.Renter, published ...events.Envelope) (domain.Renter, error) {
	read := r.Version
	r.Version++
	item, err := attributevalue.MarshalMap(toItem(rentalID, r))
	if err != nil {
		return domain.Renter{}, fmt.Errorf("marshal renter: %w", err)
	}
	put := &types.Put{TableName: &s.table, Item: item, ConditionExpression: aws.String("attribute_not_exists(PK)")}
	if read > 0 {
		put.ConditionExpression = aws.String("version = :read")
		put.ExpressionAttributeValues = map[string]types.AttributeValue{":read": &types.AttributeValueMemberN{Value: strconv.FormatInt(read, 10)}}
	}
	writes := []types.TransactWriteItem{{Put: put}}
	for _, e := range published {
		writes = append(writes, events.OutboxPut(s.table, e))
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes})
	if lostRace(err) {
		return domain.Renter{}, ErrConcurrentChange
	}
	if err != nil {
		return domain.Renter{}, fmt.Errorf("save renter: %w", err)
	}
	return r, nil
}

// lostRace reports whether the transaction failed because another writer got to the Renter first.
func lostRace(err error) bool {
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for i, reason := range canceled.CancellationReasons {
			switch code := aws.ToString(reason.Code); {
			case code == "TransactionConflict", code == "ThrottlingError", code == "ProvisionedThroughputExceeded":
				return true
			case i == 0 && code == "ConditionalCheckFailed":
				return true
			}
		}
		return false
	}
	var inProgress *types.TransactionInProgressException
	return errors.As(err, &inProgress)
}

// List returns every Renter of the Rental who has saved anything.
func (s *Store) List(ctx context.Context, rentalID string) ([]domain.Renter, error) {
	paginator := dynamodb.NewQueryPaginator(s.db, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
			":prefix": &types.AttributeValueMemberS{Value: renterPrefix},
		},
	})
	renters := []domain.Renter{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list renters: %w", err)
		}
		for _, item := range page.Items {
			r, err := fromItem(item)
			if err != nil {
				return nil, err
			}
			renters = append(renters, r)
		}
	}
	return renters, nil
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
	return time.Parse(time.RFC3339Nano, s)
}

func toItem(rentalID string, r domain.Renter) renterItem {
	item := renterItem{
		PK:              rentalKey(rentalID),
		SK:              renterPrefix + r.ID,
		ID:              r.ID,
		Email:           r.Email,
		FullName:        r.FullName,
		PhoneNumber:     r.PhoneNumber,
		Documents:       map[string]documentItem{},
		Status:          string(r.Status),
		VerifiedBy:      r.VerifiedBy,
		VerifiedAt:      formatTime(r.VerifiedAt),
		StatusChangedAt: formatTime(r.StatusChangedAt),
		CreatedAt:       formatTime(r.CreatedAt),
		UpdatedAt:       formatTime(r.UpdatedAt),
		Version:         r.Version,
	}
	for t, d := range r.Documents {
		item.Documents[string(t)] = documentItem{Key: d.Key, ContentType: d.ContentType, Size: d.Size, UploadedAt: formatTime(d.UploadedAt)}
	}
	if rej := r.Rejection; rej != nil {
		item.Rejection = &rejectionItem{Reason: rej.Reason, Documents: domain.Names(rej.Documents), By: rej.By, At: formatTime(rej.At)}
	}
	return item
}

func fromItem(av map[string]types.AttributeValue) (domain.Renter, error) {
	var item renterItem
	if err := attributevalue.UnmarshalMap(av, &item); err != nil {
		return domain.Renter{}, fmt.Errorf("unmarshal renter: %w", err)
	}
	r := domain.Renter{
		ID:          item.ID,
		Email:       item.Email,
		FullName:    item.FullName,
		PhoneNumber: item.PhoneNumber,
		Documents:   map[domain.DocumentType]domain.Document{},
		Status:      domain.Status(item.Status),
		VerifiedBy:  item.VerifiedBy,
		Version:     item.Version,
	}
	for name, field := range map[string]struct {
		raw string
		dst *time.Time
	}{
		"verified_at":       {item.VerifiedAt, &r.VerifiedAt},
		"status_changed_at": {item.StatusChangedAt, &r.StatusChangedAt},
		"created_at":        {item.CreatedAt, &r.CreatedAt},
		"updated_at":        {item.UpdatedAt, &r.UpdatedAt},
	} {
		t, err := parseTime(field.raw)
		if err != nil {
			return domain.Renter{}, fmt.Errorf("parse %s of renter %s: %w", name, item.ID, err)
		}
		*field.dst = t
	}
	for t, d := range item.Documents {
		uploadedAt, err := parseTime(d.UploadedAt)
		if err != nil {
			return domain.Renter{}, fmt.Errorf("parse uploaded_at of renter %s: %w", item.ID, err)
		}
		r.Documents[domain.DocumentType(t)] = domain.Document{Key: d.Key, ContentType: d.ContentType, Size: d.Size, UploadedAt: uploadedAt}
	}
	if rej := item.Rejection; rej != nil {
		at, err := parseTime(rej.At)
		if err != nil {
			return domain.Renter{}, fmt.Errorf("parse rejection time of renter %s: %w", item.ID, err)
		}
		r.Rejection = &domain.Rejection{Reason: rej.Reason, By: rej.By, At: at, Documents: []domain.DocumentType{}}
		for _, t := range rej.Documents {
			r.Rejection.Documents = append(r.Rejection.Documents, domain.DocumentType(t))
		}
	}
	return r, nil
}
