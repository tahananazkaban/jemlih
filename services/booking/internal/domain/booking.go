// Package domain holds the booking business rules, free of transport and storage concerns.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	minRental      = time.Hour
	maxRental      = 30 * 24 * time.Hour
	clockSkew      = 5 * time.Minute
	maxReasonChars = 500
)

var (
	ErrNotFound       = errors.New("booking not found")
	ErrUnitBooked     = errors.New("unit is already booked for part of this window")
	ErrNotCancellable = errors.New("booking can no longer be cancelled")
)

// Status is where a Booking is in its lifecycle; stored values only, see Booking.EffectiveStatus.
type Status string

const (
	StatusHeld      Status = "held"
	StatusCancelled Status = "cancelled"
	// StatusExpired is never stored: it is a held Booking whose hold has run out.
	StatusExpired Status = "expired"
)

// Window is the rented period, pickup inclusive and return exclusive.
type Window struct {
	PickupAt time.Time
	ReturnAt time.Time
}

// Overlaps reports whether two windows share any instant.
func (w Window) Overlaps(o Window) bool {
	return w.PickupAt.Before(o.ReturnAt) && o.PickupAt.Before(w.ReturnAt)
}

// ValidationError maps invalid input fields to human-readable reasons.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid booking: %d field(s) rejected", len(e.Fields))
}

// NewWindow validates a requested rental period against the current time.
func NewWindow(pickup, ret, now time.Time) (Window, error) {
	fields := map[string]string{}
	switch d := ret.Sub(pickup); {
	case pickup.Before(now.Add(-clockSkew)):
		fields["pickup_at"] = "must not be in the past"
	case d < minRental:
		fields["return_at"] = "must be at least 1 hour after pickup_at"
	case d > maxRental:
		fields["return_at"] = "must be at most 30 days after pickup_at"
	}
	if len(fields) > 0 {
		return Window{}, &ValidationError{Fields: fields}
	}
	return Window{PickupAt: pickup.UTC().Truncate(time.Minute), ReturnAt: ret.UTC().Truncate(time.Minute)}, nil
}

// Rates are a Unit's prices in integer rupiah, copied onto every Booking.
type Rates struct {
	DailyPrice      int64
	HourlyPrice     int64
	SecurityDeposit int64
}

// Price is what a window costs at given rates.
type Price struct {
	Rates
	Days       int64
	ExtraHours int64
	// ExtraHoursFee is ExtraHours at the hourly price, capped at one daily price.
	ExtraHoursFee int64
	RentalFee     int64
	TotalAmount   int64
}

// Quote prices full days at the daily price and the rest per started hour, never more than a day.
func Quote(w Window, r Rates) Price {
	total := w.ReturnAt.Sub(w.PickupAt)
	days := int64(total / (24 * time.Hour))
	rest := total % (24 * time.Hour)
	hours := int64((rest + time.Hour - 1) / time.Hour)
	extra := min(hours*r.HourlyPrice, r.DailyPrice)
	fee := days*r.DailyPrice + extra
	return Price{Rates: r, Days: days, ExtraHours: hours, ExtraHoursFee: extra, RentalFee: fee, TotalAmount: fee + r.SecurityDeposit}
}

// UnitSnapshot keeps the Unit as the Renter saw it when booking.
type UnitSnapshot struct {
	ID          string
	Model       string
	PlateNumber string
}

// Booking is one Renter's reservation of one Unit.
type Booking struct {
	ID            string
	Unit          UnitSnapshot
	RenterID      string
	RenterEmail   string
	Window        Window
	Price         Price
	Status        Status
	HoldExpiresAt time.Time
	CreatedAt     time.Time
	Cancellation  *Cancellation
}

// Cancellation records who ended a Booking early and why.
type Cancellation struct {
	By     string
	Reason string
	At     time.Time
}

// EffectiveStatus is the status at now, turning an elapsed hold into expired.
func (b Booking) EffectiveStatus(now time.Time) Status {
	if b.Status == StatusHeld && !now.Before(b.HoldExpiresAt) {
		return StatusExpired
	}
	return b.Status
}

// Cancel ends a held Booking; by is the role that cancelled it.
func (b Booking) Cancel(by, reason string, now time.Time) (Booking, error) {
	if b.EffectiveStatus(now) != StatusHeld {
		return Booking{}, ErrNotCancellable
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxReasonChars {
		return Booking{}, &ValidationError{Fields: map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonChars)}}
	}
	b.Status = StatusCancelled
	b.Cancellation = &Cancellation{By: by, Reason: reason, At: now.UTC()}
	return b, nil
}
