package service

import (
	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
)

const source = "jemlih.customer"

// Events carry only the contact email: names, phone numbers and document keys stay out of the permanent archive.
type renterVerifiedV1 struct {
	RenterID    string `json:"renter_id"`
	RenterEmail string `json:"renter_email"`
	VerifiedBy  string `json:"verified_by"`
}

type renterVerificationRejectedV1 struct {
	RenterID    string   `json:"renter_id"`
	RenterEmail string   `json:"renter_email"`
	Reason      string   `json:"reason"`
	Documents   []string `json:"documents"`
	RejectedBy  string   `json:"rejected_by"`
}

func renterVerified(rentalID string, r domain.Renter) (events.Envelope, error) {
	return events.New(source, rentalID, "RenterVerified", 1, renterVerifiedV1{
		RenterID:    r.ID,
		RenterEmail: r.Email,
		VerifiedBy:  r.VerifiedBy,
	}, r.VerifiedAt)
}

func renterVerificationRejected(rentalID string, r domain.Renter) (events.Envelope, error) {
	return events.New(source, rentalID, "RenterVerificationRejected", 1, renterVerificationRejectedV1{
		RenterID:    r.ID,
		RenterEmail: r.Email,
		Reason:      r.Rejection.Reason,
		Documents:   domain.Names(r.Rejection.Documents),
		RejectedBy:  r.Rejection.By,
	}, r.Rejection.At)
}
