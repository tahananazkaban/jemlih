package customer_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
)

const relayTimeout = 20 * time.Second

func TestApprovalPublishesRenterVerified(t *testing.T) {
	r := newRental(t, 0)
	tap := testkit.TapRental(t, r.id)
	_, me := r.pendingRenter()

	var got renter
	r.approve(r.owner, me.ID, me.Version).Expect(http.StatusOK).Decode(&got)

	event := tap.Next("RenterVerified", relayTimeout)
	if event.Source != "jemlih.customer" {
		t.Errorf("source = %s", event.Source)
	}
	if err := events.ValidateAgainst(events.EnvelopeSchema, event.Detail); err != nil {
		t.Errorf("envelope: %v", err)
	}
	if err := events.Validate("RenterVerified", 1, event.Envelope.Data); err != nil {
		t.Errorf("data: %v", err)
	}
	var data struct {
		RenterID    string `json:"renter_id"`
		RenterEmail string `json:"renter_email"`
		VerifiedBy  string `json:"verified_by"`
	}
	if err := json.Unmarshal(event.Envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.RenterID != me.ID || data.RenterEmail != me.Email || data.VerifiedBy == "" {
		t.Errorf("data = %+v", data)
	}
	if !event.Envelope.OccurredAt.Equal(*got.VerifiedAt) {
		t.Errorf("occurred_at = %s, want verified_at %s", event.Envelope.OccurredAt, got.VerifiedAt)
	}
}

func TestRejectionPublishesRenterVerificationRejected(t *testing.T) {
	r := newRental(t, 0)
	tap := testkit.TapRental(t, r.id)
	token, me := r.pendingRenter()

	r.reject(r.owner, me.ID, me.Version, "Licence has expired", "driving_licence", "id_card").Expect(http.StatusOK)

	event := tap.Next("RenterVerificationRejected", relayTimeout)
	if event.Source != "jemlih.customer" {
		t.Errorf("source = %s", event.Source)
	}
	if err := events.ValidateAgainst(events.EnvelopeSchema, event.Detail); err != nil {
		t.Errorf("envelope: %v", err)
	}
	if err := events.Validate("RenterVerificationRejected", 1, event.Envelope.Data); err != nil {
		t.Errorf("data: %v", err)
	}
	var data struct {
		RenterID  string   `json:"renter_id"`
		Reason    string   `json:"reason"`
		Documents []string `json:"documents"`
	}
	if err := json.Unmarshal(event.Envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.RenterID != me.ID || data.Reason != "Licence has expired" || !slices.Equal(data.Documents, []string{"id_card", "driving_licence"}) {
		t.Errorf("data = %+v", data)
	}

	// The Renter fixes both documents and is approved: a second, distinct event.
	r.uploadDocument(token, "id_card", photo("new id card"))
	again := r.uploadDocument(token, "driving_licence", photo("new licence"))
	r.approve(r.owner, me.ID, again.Version).Expect(http.StatusOK)
	verified := tap.Next("RenterVerified", relayTimeout)
	if verified.Envelope.EventID == event.Envelope.EventID {
		t.Errorf("both events have event_id %s", event.Envelope.EventID)
	}
}

func TestRenterChangesPublishNothing(t *testing.T) {
	r := newRental(t, 0)
	tap := testkit.TapRental(t, r.id)

	r.pendingRenter()
	tap.ExpectNone(5 * time.Second)
}
