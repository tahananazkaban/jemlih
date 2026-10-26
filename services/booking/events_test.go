package booking_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
)

const relayTimeout = 20 * time.Second

func decodeData(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var data map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return data
}

func TestBookingPublishesBookingCreated(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	tap := testkit.TapRental(t, r.id)
	unit := r.addUnit("AB 20 S", 85000, 15000, 300000)
	tap.Next("UnitCreated", relayTimeout)

	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(26))).Expect(http.StatusCreated).Decode(&b)

	got := tap.Next("BookingCreated", relayTimeout)
	if got.Source != "jemlih.booking" {
		t.Errorf("source = %s", got.Source)
	}
	if err := events.ValidateAgainst(events.EnvelopeSchema, got.Detail); err != nil {
		t.Errorf("envelope: %v", err)
	}
	if err := events.Validate("BookingCreated", 1, got.Envelope.Data); err != nil {
		t.Errorf("data: %v", err)
	}
	data := decodeData(t, got.Envelope.Data)
	if data["booking_id"] != b.ID || data["unit_id"] != unit || data["renter_id"] != b.RenterID {
		t.Errorf("data = %v", data)
	}
	for field, want := range map[string]string{"rental_fee": "115000", "security_deposit": "300000", "total_amount": "415000"} {
		if n, ok := data[field].(json.Number); !ok || n.String() != want {
			t.Errorf("%s = %v, want integer %s", field, data[field], want)
		}
	}
	if !got.Envelope.OccurredAt.Equal(b.CreatedAt) {
		t.Errorf("occurred_at = %s, want created_at %s", got.Envelope.OccurredAt, b.CreatedAt)
	}
}

func TestCancellingPublishesBookingCancelled(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	tap := testkit.TapRental(t, r.id)
	unit := r.addUnit("AB 21 T", 85000, 15000, 300000)
	tap.Next("UnitCreated", relayTimeout)

	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(5))).Expect(http.StatusCreated).Decode(&b)
	tap.Next("BookingCreated", relayTimeout)
	r.as(r.owner).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", map[string]string{"reason": "Double booked by phone"}).Expect(http.StatusOK)

	got := tap.Next("BookingCancelled", relayTimeout)
	if err := events.Validate("BookingCancelled", 1, got.Envelope.Data); err != nil {
		t.Errorf("data: %v", err)
	}
	data := decodeData(t, got.Envelope.Data)
	if data["booking_id"] != b.ID || data["cancelled_by"] != "owner" || data["reason"] != "Double booked by phone" {
		t.Errorf("data = %v", data)
	}
}

func TestRejectedBookingsPublishNothing(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	tap := testkit.TapRental(t, r.id)
	unit := r.addUnit("AB 22 U", 85000, 15000, 300000)
	tap.Next("UnitCreated", relayTimeout)

	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(5))).Expect(http.StatusCreated)
	tap.Next("BookingCreated", relayTimeout)
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(1), at(3))).Expect(http.StatusConflict)

	tap.ExpectNone(5 * time.Second)
}
