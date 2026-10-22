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

// Relay publishes newly inserted outbox items to the bus.
type Relay struct {
	Bus     *eventbridge.Client
	BusName string
}

type pending struct {
	sequence string
	entry    types.PutEventsRequestEntry
}

// Handle publishes the batch in stream order and reports the first record it could not publish;
// Lambda then retries from that record, so nothing after it is skipped.
func (r *Relay) Handle(ctx context.Context, batch lambdaevents.DynamoDBEvent) (lambdaevents.DynamoDBEventResponse, error) {
	var queue []pending
	for _, rec := range batch.Records {
		// Data items share the stream, and TTL removals arrive as REMOVE records.
		if rec.EventName != string(lambdaevents.DynamoDBOperationTypeInsert) {
			continue
		}
		image := rec.Change.NewImage
		if !events.IsOutboxKey(stringAttr(image, "SK")) {
			continue
		}
		entry, err := r.entry(image)
		if err != nil {
			// A malformed item is retried, then lands in the dead-letter queue with its position.
			slog.ErrorContext(ctx, "malformed outbox item", "sequence", rec.Change.SequenceNumber, "err", err)
			return r.publish(ctx, queue, rec.Change.SequenceNumber), nil
		}
		queue = append(queue, pending{sequence: rec.Change.SequenceNumber, entry: entry})
	}
	return r.publish(ctx, queue, ""), nil
}

// publish sends queue in chunks and returns a failure at the first unpublished record, or at failAt.
func (r *Relay) publish(ctx context.Context, queue []pending, failAt string) lambdaevents.DynamoDBEventResponse {
	for start := 0; start < len(queue); start += maxEntriesPerPut {
		chunk := queue[start:min(start+maxEntriesPerPut, len(queue))]
		entries := make([]types.PutEventsRequestEntry, len(chunk))
		for i, p := range chunk {
			entries[i] = p.entry
		}
		out, err := r.Bus.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: entries})
		if err == nil && out.FailedEntryCount == 0 {
			continue
		}
		first := chunk[0].sequence
		if err == nil {
			// Entries before the first failed one are on the bus; retry from the failed one.
			for i, res := range out.Entries {
				if res.ErrorCode != nil {
					first = chunk[i].sequence
					err = fmt.Errorf("%s: %s", aws.ToString(res.ErrorCode), aws.ToString(res.ErrorMessage))
					break
				}
			}
		}
		slog.ErrorContext(ctx, "put events failed", "sequence", first, "err", err)
		return failure(first)
	}
	if len(queue) > 0 {
		slog.InfoContext(ctx, "relayed events", "count", len(queue))
	}
	if failAt != "" {
		return failure(failAt)
	}
	return lambdaevents.DynamoDBEventResponse{}
}

func failure(sequence string) lambdaevents.DynamoDBEventResponse {
	return lambdaevents.DynamoDBEventResponse{
		BatchItemFailures: []lambdaevents.DynamoDBBatchItemFailure{{ItemIdentifier: sequence}},
	}
}

func (r *Relay) entry(image map[string]lambdaevents.DynamoDBAttributeValue) (types.PutEventsRequestEntry, error) {
	e, err := events.FromOutbox(func(name string) string { return stringAttr(image, name) })
	if err != nil {
		return types.PutEventsRequestEntry{}, err
	}
	detail, err := json.Marshal(e)
	if err != nil {
		return types.PutEventsRequestEntry{}, fmt.Errorf("marshal %s: %w", e.EventID, err)
	}
	return types.PutEventsRequestEntry{
		EventBusName: aws.String(r.BusName),
		Source:       aws.String(e.Source),
		DetailType:   aws.String(e.Type),
		Detail:       aws.String(string(detail)),
		Time:         aws.Time(e.OccurredAt),
	}, nil
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
