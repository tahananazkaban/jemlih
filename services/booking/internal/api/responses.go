package api

import (
	"time"

	"github.com/tahananazkaban/jemlih/services/booking/internal/domain"
)

type priceResponse struct {
	DailyPrice      int64 `json:"daily_price"`
	HourlyPrice     int64 `json:"hourly_price"`
	Days            int64 `json:"days"`
	ExtraHours      int64 `json:"extra_hours"`
	RentalFee       int64 `json:"rental_fee"`
	SecurityDeposit int64 `json:"security_deposit"`
	TotalAmount     int64 `json:"total_amount"`
}

type offerResponse struct {
	ID           string        `json:"id"`
	Model        string        `json:"model"`
	PlateNumber  string        `json:"plate_number"`
	Transmission string        `json:"transmission"`
	Price        priceResponse `json:"price"`
}

type availabilityResponse struct {
	PickupAt time.Time       `json:"pickup_at"`
	ReturnAt time.Time       `json:"return_at"`
	Units    []offerResponse `json:"units"`
}

type unitResponse struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	PlateNumber string `json:"plate_number"`
}

type cancellationResponse struct {
	By     string    `json:"by"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

type bookingResponse struct {
	ID            string                `json:"id"`
	Unit          unitResponse          `json:"unit"`
	RenterID      string                `json:"renter_id"`
	PickupAt      time.Time             `json:"pickup_at"`
	ReturnAt      time.Time             `json:"return_at"`
	Status        domain.Status         `json:"status"`
	HoldExpiresAt time.Time             `json:"hold_expires_at"`
	Price         priceResponse         `json:"price"`
	CreatedAt     time.Time             `json:"created_at"`
	Cancellation  *cancellationResponse `json:"cancellation,omitempty"`
}

type bookingList struct {
	Bookings []bookingResponse `json:"bookings"`
}

func toPrice(p domain.Price) priceResponse {
	return priceResponse{
		DailyPrice: p.DailyPrice, HourlyPrice: p.HourlyPrice, Days: p.Days, ExtraHours: p.ExtraHours,
		RentalFee: p.RentalFee, SecurityDeposit: p.SecurityDeposit, TotalAmount: p.TotalAmount,
	}
}

// toResponse shows the status at the time of the request, so an elapsed hold reads as expired.
func (h *Handler) toResponse(b domain.Booking) bookingResponse {
	res := bookingResponse{
		ID:            b.ID,
		Unit:          unitResponse{ID: b.Unit.ID, Model: b.Unit.Model, PlateNumber: b.Unit.PlateNumber},
		RenterID:      b.RenterID,
		PickupAt:      b.Window.PickupAt,
		ReturnAt:      b.Window.ReturnAt,
		Status:        b.EffectiveStatus(h.Now()),
		HoldExpiresAt: b.HoldExpiresAt,
		Price:         toPrice(b.Price),
		CreatedAt:     b.CreatedAt,
	}
	if c := b.Cancellation; c != nil {
		res.Cancellation = &cancellationResponse{By: c.By, Reason: c.Reason, At: c.At}
	}
	return res
}
