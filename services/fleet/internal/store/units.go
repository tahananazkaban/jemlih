// Package store persists fleet data in DynamoDB.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/domain"
)

const (
	unitPrefix  = "UNIT#"
	platePrefix = "PLATE#"
)

// Units stores Units in one table partitioned by Rental: PK=RENTAL#<id>, SK=UNIT#<id> or PLATE#<plate>.
type Units struct {
	db    *dynamodb.Client
	table string
}

func NewUnits(db *dynamodb.Client, table string) *Units {
	return &Units{db: db, table: table}
}

type unitItem struct {
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	ID              string `dynamodbav:"id"`
	Model           string `dynamodbav:"model"`
	PlateNumber     string `dynamodbav:"plate_number"`
	Year            int    `dynamodbav:"year"`
	Transmission    string `dynamodbav:"transmission"`
	DailyPrice      int64  `dynamodbav:"daily_price"`
	HourlyPrice     int64  `dynamodbav:"hourly_price"`
	SecurityDeposit int64  `dynamodbav:"security_deposit"`
	Status          string `dynamodbav:"status"`
	CreatedAt       string `dynamodbav:"created_at"`
}

type plateItem struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	UnitID string `dynamodbav:"unit_id"`
}

func rentalKey(rentalID string) string { return "RENTAL#" + rentalID }

// Create writes the Unit, claims its plate number and records the created event in one transaction.
func (s *Units) Create(ctx context.Context, rentalID string, u domain.Unit, created events.Envelope) error {
	unit, err := attributevalue.MarshalMap(toItem(rentalID, u))
	if err != nil {
		return fmt.Errorf("marshal unit: %w", err)
	}
	plate, err := attributevalue.MarshalMap(plateItem{PK: rentalKey(rentalID), SK: platePrefix + u.PlateNumber, UnitID: u.ID})
	if err != nil {
		return fmt.Errorf("marshal plate: %w", err)
	}
	notExists := aws.String("attribute_not_exists(PK)")
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: &s.table, Item: unit, ConditionExpression: notExists}},
			{Put: &types.Put{TableName: &s.table, Item: plate, ConditionExpression: notExists}},
			events.OutboxPut(s.table, created),
		},
	})
	if plateTaken(err) {
		return domain.ErrDuplicatePlate
	}
	if err != nil {
		return fmt.Errorf("create unit: %w", err)
	}
	return nil
}

// plateTaken reports whether the transaction failed only because the plate claim (item 1) exists.
func plateTaken(err error) bool {
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) || len(canceled.CancellationReasons) < 2 {
		return false
	}
	return aws.ToString(canceled.CancellationReasons[1].Code) == "ConditionalCheckFailed"
}

// Get returns one Unit of the Rental, or domain.ErrNotFound.
func (s *Units) Get(ctx context.Context, rentalID, id string) (domain.Unit, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
			"SK": &types.AttributeValueMemberS{Value: unitPrefix + id},
		},
	})
	if err != nil {
		return domain.Unit{}, fmt.Errorf("get unit: %w", err)
	}
	if out.Item == nil {
		return domain.Unit{}, domain.ErrNotFound
	}
	return fromItem(out.Item)
}

// List returns every Unit of the Rental, oldest first.
func (s *Units) List(ctx context.Context, rentalID string) ([]domain.Unit, error) {
	paginator := dynamodb.NewQueryPaginator(s.db, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: rentalKey(rentalID)},
			":prefix": &types.AttributeValueMemberS{Value: unitPrefix},
		},
	})
	units := []domain.Unit{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list units: %w", err)
		}
		for _, item := range page.Items {
			u, err := fromItem(item)
			if err != nil {
				return nil, err
			}
			units = append(units, u)
		}
	}
	return units, nil
}

func toItem(rentalID string, u domain.Unit) unitItem {
	return unitItem{
		PK:              rentalKey(rentalID),
		SK:              unitPrefix + u.ID,
		ID:              u.ID,
		Model:           u.Model,
		PlateNumber:     u.PlateNumber,
		Year:            u.Year,
		Transmission:    string(u.Transmission),
		DailyPrice:      u.DailyPrice,
		HourlyPrice:     u.HourlyPrice,
		SecurityDeposit: u.SecurityDeposit,
		Status:          string(u.Status),
		CreatedAt:       u.CreatedAt.Format(time.RFC3339Nano),
	}
}

func fromItem(av map[string]types.AttributeValue) (domain.Unit, error) {
	var item unitItem
	if err := attributevalue.UnmarshalMap(av, &item); err != nil {
		return domain.Unit{}, fmt.Errorf("unmarshal unit: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, item.CreatedAt)
	if err != nil {
		return domain.Unit{}, fmt.Errorf("parse created_at of unit %s: %w", item.ID, err)
	}
	return domain.Unit{
		ID:              item.ID,
		Model:           item.Model,
		PlateNumber:     item.PlateNumber,
		Year:            item.Year,
		Transmission:    domain.Transmission(item.Transmission),
		DailyPrice:      item.DailyPrice,
		HourlyPrice:     item.HourlyPrice,
		SecurityDeposit: item.SecurityDeposit,
		Status:          domain.Status(item.Status),
		CreatedAt:       createdAt,
	}, nil
}
