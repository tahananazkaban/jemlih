package service

import (
	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
)

const source = "jemlih.customer"

// Events stay free of names and document keys, since the archive keeps them forever.
type renterVerifiedV1 struct {
	RenterID   string `json:"renter_id"`
	Email      string `json:"email"`
	VerifiedBy string `json:"verified_by"`
}

type renterVerificationRejectedV1 struct {
	RenterID   string   `json:"renter_id"`
	Email      string   `json:"email"`
	Reason     string   `json:"reason"`
	Documents  []string `json:"documents"`
	RejectedBy string   `json:"rejected_by"`
}

func renterVerified(rentalID string, r domain.Renter) (events.Envelope, error) {
	return events.New(source, rentalID, "RenterVerified", 1, renterVerifiedV1{
		RenterID:   r.ID,
		Email:      r.Email,
		VerifiedBy: r.VerifiedBy,
	}, r.VerifiedAt)
}

func renterVerificationRejected(rentalID string, r domain.Renter) (events.Envelope, error) {
	redo := []string{}
	for _, t := range r.Rejection.Documents {
		redo = append(redo, string(t))
	}
	return events.New(source, rentalID, "RenterVerificationRejected", 1, renterVerificationRejectedV1{
		RenterID:   r.ID,
		Email:      r.Email,
		Reason:     r.Rejection.Reason,
		Documents:  redo,
		RejectedBy: r.Rejection.By,
	}, r.Rejection.At)
}
