package events

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// OutboxPrefix starts the sort key of every outbox item.
const OutboxPrefix = "EVENT#"

// outboxRetention is how long a relayed outbox item stays before TTL deletes it.
const outboxRetention = 24 * time.Hour

// OutboxPut is the transaction item that stores e next to the change it describes, under partition pk.
func OutboxPut(table, pk string, e Envelope) types.TransactWriteItem {
	return types.TransactWriteItem{Put: &types.Put{
		TableName:           aws.String(table),
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
		Item: map[string]types.AttributeValue{
			"PK":          &types.AttributeValueMemberS{Value: pk},
			"SK":          &types.AttributeValueMemberS{Value: OutboxPrefix + e.EventID},
			"event_id":    &types.AttributeValueMemberS{Value: e.EventID},
			"type":        &types.AttributeValueMemberS{Value: e.Type},
			"version":     &types.AttributeValueMemberN{Value: strconv.Itoa(e.Version)},
			"occurred_at": &types.AttributeValueMemberS{Value: e.OccurredAt.Format(time.RFC3339Nano)},
			"rental_id":   &types.AttributeValueMemberS{Value: e.RentalID},
			"source":      &types.AttributeValueMemberS{Value: e.Source},
			"data":        &types.AttributeValueMemberS{Value: string(e.Data)},
			"expires_at":  &types.AttributeValueMemberN{Value: strconv.FormatInt(e.OccurredAt.Add(outboxRetention).Unix(), 10)},
		},
	}}
}

// IsOutboxKey reports whether a sort key belongs to an outbox item.
func IsOutboxKey(sk string) bool {
	return strings.HasPrefix(sk, OutboxPrefix)
}

// FromOutbox rebuilds the envelope from an outbox item's string and number attributes.
func FromOutbox(attr func(name string) string) (Envelope, error) {
	version, err := strconv.Atoi(attr("version"))
	if err != nil {
		return Envelope{}, fmt.Errorf("outbox version: %w", err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, attr("occurred_at"))
	if err != nil {
		return Envelope{}, fmt.Errorf("outbox occurred_at: %w", err)
	}
	e := Envelope{
		EventID:    attr("event_id"),
		Type:       attr("type"),
		Version:    version,
		OccurredAt: occurredAt,
		RentalID:   attr("rental_id"),
		Source:     attr("source"),
		Data:       []byte(attr("data")),
	}
	if e.EventID == "" || e.Type == "" || e.Source == "" || e.RentalID == "" {
		return Envelope{}, fmt.Errorf("outbox item %q is missing envelope fields", e.EventID)
	}
	return e, nil
}
