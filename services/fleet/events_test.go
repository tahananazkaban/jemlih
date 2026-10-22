package fleet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
)

// relayTimeout covers stream polling, the relay Lambda and EventBridge delivery.
const relayTimeout = 20 * time.Second

func TestCreatingAUnitPublishesUnitCreated(t *testing.T) {
	rentalID := testkit.NewRentalID()
	tap := testkit.TapRental(t, rentalID)
	fleet := startFleet(t, rentalID)

	var u unit
	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated).Decode(&u)

	got := tap.Next("UnitCreated", relayTimeout)
	if got.Source != "jemlih.fleet" || got.DetailType != "UnitCreated" {
		t.Errorf("source/detail-type = %s/%s, want jemlih.fleet/UnitCreated", got.Source, got.DetailType)
	}
	if err := events.ValidateAgainst(events.EnvelopeSchema, got.Detail); err != nil {
		t.Errorf("envelope: %v", err)
	}
	e := got.Envelope
	if e.EventID == "" || e.Version != 1 || e.RentalID != rentalID || e.Source != "jemlih.fleet" {
		t.Errorf("envelope = %+v", e)
	}
	if created, _ := time.Parse(time.RFC3339Nano, u.CreatedAt); !e.OccurredAt.Equal(created) {
		t.Errorf("occurred_at = %s, want the unit's created_at %s", e.OccurredAt, u.CreatedAt)
	}
	if err := events.Validate("UnitCreated", 1, e.Data); err != nil {
		t.Errorf("data: %v", err)
	}

	var data map[string]any
	dec := json.NewDecoder(bytes.NewReader(e.Data))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["unit_id"] != u.ID || data["plate_number"] != "AB 1234 XY" || data["status"] != "available" {
		t.Errorf("data = %v", data)
	}
	for field, want := range map[string]string{"daily_price": "85000", "hourly_price": "15000", "security_deposit": "300000"} {
		if n, ok := data[field].(json.Number); !ok || n.String() != want {
			t.Errorf("%s = %v, want integer %s", field, data[field], want)
		}
	}
}

func TestEveryEventHasItsOwnID(t *testing.T) {
	rentalID := testkit.NewRentalID()
	tap := testkit.TapRental(t, rentalID)
	fleet := startFleet(t, rentalID)

	second := validUnit()
	second["plate_number"] = "B 9 Z"
	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated)
	fleet.Do(http.MethodPost, "/units", second).Expect(http.StatusCreated)

	a, b := tap.Next("UnitCreated", relayTimeout), tap.Next("UnitCreated", relayTimeout)
	if a.Envelope.EventID == b.Envelope.EventID {
		t.Errorf("both events have id %s", a.Envelope.EventID)
	}
}

func TestRejectedChangesPublishNothing(t *testing.T) {
	rentalID := testkit.NewRentalID()
	tap := testkit.TapRental(t, rentalID)
	fleet := startFleet(t, rentalID)

	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated)
	tap.Next("UnitCreated", relayTimeout)

	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusConflict)
	invalid := validUnit()
	invalid["year"] = 1900
	fleet.Do(http.MethodPost, "/units", invalid).Expect(http.StatusBadRequest)

	// Any event from the rejected requests would be older than this one and arrive first.
	marker := validUnit()
	marker["plate_number"] = "D 1 M"
	var u unit
	fleet.Do(http.MethodPost, "/units", marker).Expect(http.StatusCreated).Decode(&u)
	got := tap.Next("UnitCreated", relayTimeout)
	if !strings.Contains(string(got.Envelope.Data), u.ID) {
		t.Errorf("next event is not for the marker unit %s: %s", u.ID, got.Envelope.Data)
	}
}

func TestEventsAreArchived(t *testing.T) {
	t.Parallel()
	rentalID := testkit.NewRentalID()
	fleet := startFleet(t, rentalID)
	var u unit
	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated).Decode(&u)

	client := s3.NewFromConfig(testkit.AWSConfig(t), func(o *s3.Options) { o.UsePathStyle = true })
	bucket := env.String("EVENT_ARCHIVE_BUCKET", "jemlih-event-archive")
	// Firehose flushes its buffer every 60 seconds locally.
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		if archiveContains(t, client, bucket, u.ID) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("UnitCreated for %s not found in s3://%s", u.ID, bucket)
}

func archiveContains(t *testing.T, client *s3.Client, bucket, needle string) bool {
	t.Helper()
	ctx := context.Background()
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: aws.String("events/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatalf("list archive: %v", err)
		}
		for _, obj := range page.Contents {
			out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: obj.Key})
			if err != nil {
				t.Fatalf("read %s: %v", aws.ToString(obj.Key), err)
			}
			body, err := io.ReadAll(out.Body)
			out.Body.Close()
			if err != nil {
				t.Fatalf("read %s: %v", aws.ToString(obj.Key), err)
			}
			if bytes.Contains(body, []byte(needle)) {
				return true
			}
		}
	}
	return false
}
