package service

import (
	"time"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/booking/internal/domain"
)

const source = "jemlih.booking"

type bookingCreatedV1 struct {
	BookingID       string    `json:"booking_id"`
	UnitID          string    `json:"unit_id"`
	RenterID        string    `json:"renter_id"`
	RenterEmail     string    `json:"renter_email"`
	PickupAt        time.Time `json:"pickup_at"`
	ReturnAt        time.Time `json:"return_at"`
	HoldExpiresAt   time.Time `json:"hold_expires_at"`
	RentalFee       int64     `json:"rental_fee"`
	SecurityDeposit int64     `json:"security_deposit"`
	TotalAmount     int64     `json:"total_amount"`
}

type bookingCancelledV1 struct {
	BookingID   string `json:"booking_id"`
	UnitID      string `json:"unit_id"`
	RenterID    string `json:"renter_id"`
	CancelledBy string `json:"cancelled_by"`
	Reason      string `json:"reason"`
}

func bookingCreated(rentalID string, b domain.Booking) (events.Envelope, error) {
	return events.New(source, rentalID, "BookingCreated", 1, bookingCreatedV1{
		BookingID:       b.ID,
		UnitID:          b.Unit.ID,
		RenterID:        b.RenterID,
		RenterEmail:     b.RenterEmail,
		PickupAt:        b.Window.PickupAt,
		ReturnAt:        b.Window.ReturnAt,
		HoldExpiresAt:   b.HoldExpiresAt,
		RentalFee:       b.Price.RentalFee,
		SecurityDeposit: b.Price.SecurityDeposit,
		TotalAmount:     b.Price.TotalAmount,
	}, b.CreatedAt)
}

func bookingCancelled(rentalID string, b domain.Booking) (events.Envelope, error) {
	return events.New(source, rentalID, "BookingCancelled", 1, bookingCancelledV1{
		BookingID:   b.ID,
		UnitID:      b.Unit.ID,
		RenterID:    b.RenterID,
		CancelledBy: b.Cancellation.By,
		Reason:      b.Cancellation.Reason,
	}, b.Cancellation.At)
}
