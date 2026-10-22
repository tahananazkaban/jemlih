// Package relay forwards outbox items from DynamoDB streams to the event bus.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/tahananazkaban/jemlih/events"
)

// maxEntriesPerPut is the PutEvents batch limit.
const maxEntriesPerPut = 10

// Relay publishes newly inserted outbox items; a returned error makes Lambda retry the whole batch.
type Relay struct {
	Bus     *eventbridge.Client
	BusName string
}

func (r *Relay) Handle(ctx context.Context, batch lambdaevents.DynamoDBEvent) error {
	var entries []types.PutEventsRequestEntry
	for _, rec := range batch.Records {
		// Data items share the stream, and TTL removals arrive as REMOVE records.
		if rec.EventName != string(lambdaevents.DynamoDBOperationTypeInsert) {
			continue
		}
		image := rec.Change.NewImage
		if !events.IsOutboxKey(stringAttr(image, "SK")) {
			continue
		}
		e, err := events.FromOutbox(func(name string) string { return stringAttr(image, name) })
		if err != nil {
			return err
		}
		detail, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", e.EventID, err)
		}
		entries = append(entries, types.PutEventsRequestEntry{
			EventBusName: aws.String(r.BusName),
			Source:       aws.String(e.Source),
			DetailType:   aws.String(e.Type),
			Detail:       aws.String(string(detail)),
			Time:         aws.Time(e.OccurredAt),
		})
	}
	for start := 0; start < len(entries); start += maxEntriesPerPut {
		chunk := entries[start:min(start+maxEntriesPerPut, len(entries))]
		out, err := r.Bus.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: chunk})
		if err != nil {
			return fmt.Errorf("put events: %w", err)
		}
		if out.FailedEntryCount > 0 {
			return fmt.Errorf("put events: %d of %d entries failed", out.FailedEntryCount, len(chunk))
		}
	}
	if len(entries) > 0 {
		slog.InfoContext(ctx, "relayed events", "count", len(entries))
	}
	return nil
}

// stringAttr reads a string or number attribute as text.
func stringAttr(image map[string]lambdaevents.DynamoDBAttributeValue, name string) string {
	v, ok := image[name]
	if !ok {
		return ""
	}
	switch v.DataType() {
	case lambdaevents.DataTypeString:
		return v.String()
	case lambdaevents.DataTypeNumber:
		return v.Number()
	default:
		return ""
	}
}
