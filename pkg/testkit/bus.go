package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/pkg/env"
)

// Tap receives every event published for one Rental, through a temporary queue subscribed to the bus.
type Tap struct {
	t        testing.TB
	sqs      *sqs.Client
	queueURL string
	pending  []Delivered
	// seen drops at-least-once redeliveries, as every consumer must.
	seen map[string]bool
}

// Delivered is an event as EventBridge handed it to a target.
type Delivered struct {
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
	Envelope   events.Envelope `json:"-"`
}

// TapRental subscribes a fresh queue to the bus for events whose rental_id is rentalID.
func TapRental(t testing.TB, rentalID string) *Tap {
	t.Helper()
	ctx := context.Background()
	cfg := AWSConfig(t)
	bus := env.String("EVENT_BUS_NAME", "jemlih")
	name := "tap-" + rentalID
	sqsClient, eb := sqs.NewFromConfig(cfg), eventbridge.NewFromConfig(cfg)

	queue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &name})
	if err != nil {
		t.Fatalf("create tap queue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sqsClient.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl})
	})
	attrs, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("tap queue arn: %v", err)
	}
	queueARN := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	// Real AWS needs this policy before EventBridge may deliver to the queue.
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage","Resource":%q}]}`, queueARN)
	if _, err := sqsClient.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, Attributes: map[string]string{"Policy": policy},
	}); err != nil {
		t.Fatalf("tap queue policy: %v", err)
	}

	pattern := fmt.Sprintf(`{"source":[{"prefix":"jemlih."}],"detail":{"rental_id":[%q]}}`, rentalID)
	if _, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{Name: &name, EventBusName: &bus, EventPattern: &pattern}); err != nil {
		t.Fatalf("create tap rule: %v", err)
	}
	t.Cleanup(func() {
		_, _ = eb.DeleteRule(context.Background(), &eventbridge.DeleteRuleInput{Name: &name, EventBusName: &bus})
	})
	if _, err := eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: &name, EventBusName: &bus, Targets: []ebtypes.Target{{Id: aws.String("tap"), Arn: &queueARN}},
	}); err != nil {
		t.Fatalf("add tap target: %v", err)
	}
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(context.Background(), &eventbridge.RemoveTargetsInput{Rule: &name, EventBusName: &bus, Ids: []string{"tap"}})
	})
	return &Tap{t: t, sqs: sqsClient, queueURL: aws.ToString(queue.QueueUrl), seen: map[string]bool{}}
}

// ExpectNone fails the test if any event for the Rental arrives within window.
func (tap *Tap) ExpectNone(window time.Duration) {
	tap.t.Helper()
	if got, ok := tap.receive(window); ok {
		tap.t.Fatalf("unexpected %s event: %s", got.Envelope.Type, got.Detail)
	}
}

// Next waits for the next event of eventType, failing the test if another type arrives first or none does.
func (tap *Tap) Next(eventType string, timeout time.Duration) Delivered {
	tap.t.Helper()
	got, ok := tap.receive(timeout)
	if !ok {
		tap.t.Fatalf("no %s event within %s", eventType, timeout)
	}
	if got.Envelope.Type != eventType {
		tap.t.Fatalf("next event is %s, want %s: %s", got.Envelope.Type, eventType, got.Detail)
	}
	return got
}

func (tap *Tap) receive(timeout time.Duration) (Delivered, bool) {
	tap.t.Helper()
	deadline := time.Now().Add(timeout)
	for len(tap.pending) == 0 && time.Now().Before(deadline) {
		wait := min(int32(time.Until(deadline).Seconds())+1, 5)
		out, err := tap.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: &tap.queueURL, MaxNumberOfMessages: 10, WaitTimeSeconds: wait,
		})
		if err != nil {
			tap.t.Fatalf("receive from tap: %v", err)
		}
		for _, m := range out.Messages {
			var d Delivered
			if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &d); err != nil {
				tap.t.Fatalf("decode delivered event: %v", err)
			}
			if err := json.Unmarshal(d.Detail, &d.Envelope); err != nil {
				tap.t.Fatalf("decode envelope: %v", err)
			}
			_, _ = tap.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: &tap.queueURL, ReceiptHandle: m.ReceiptHandle})
			if tap.seen[d.Envelope.EventID] {
				continue
			}
			tap.seen[d.Envelope.EventID] = true
			tap.pending = append(tap.pending, d)
		}
	}
	if len(tap.pending) == 0 {
		return Delivered{}, false
	}
	// Standard queues do not keep order, so deliver the oldest event first.
	oldest := 0
	for i, d := range tap.pending {
		if d.Envelope.OccurredAt.Before(tap.pending[oldest].Envelope.OccurredAt) {
			oldest = i
		}
	}
	next := tap.pending[oldest]
	tap.pending = append(tap.pending[:oldest], tap.pending[oldest+1:]...)
	return next, true
}
