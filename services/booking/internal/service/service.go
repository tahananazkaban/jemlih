// Package service runs the booking use cases: availability, booking with optimistic locking, and cancellation.
package service

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/services/booking/internal/domain"
	"github.com/tahananazkaban/jemlih/services/booking/internal/fleet"
	"github.com/tahananazkaban/jemlih/services/booking/internal/store"
)

// maxAttempts bounds retries when concurrent writers keep changing the same calendar.
const maxAttempts = 10

var (
	ErrUnitNotBookable = errors.New("unit is not available for booking")
	ErrReasonRequired  = errors.New("owners must give a reason to cancel")
	ErrBusy            = errors.New("too many concurrent changes, try again")
)

// Caller is the verified user and the token to forward to fleet.
type Caller struct {
	auth.Identity
	Token string
}

// Service implements booking on top of the store and the fleet API.
type Service struct {
	Store *store.Store
	Fleet *fleet.Client
	Hold  time.Duration
	Now   func() time.Time
	NewID func() string
}

// Offer is a Unit that is free for the requested window, with its price.
type Offer struct {
	Unit  fleet.Unit
	Price domain.Price
}

// Availability lists the Rental's bookable Units that are free for the whole window.
func (s *Service) Availability(ctx context.Context, c Caller, w domain.Window) ([]Offer, error) {
	units, err := s.Fleet.List(ctx, c.Token)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(units))
	for _, u := range units {
		ids = append(ids, u.ID)
	}
	cals, err := s.Store.Calendars(ctx, c.RentalID, ids)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	offers := []Offer{}
	for _, u := range units {
		if u.Bookable() && cals[u.ID].Free(w, now) {
			offers = append(offers, Offer{Unit: u, Price: domain.Quote(w, rates(u))})
		}
	}
	return offers, nil
}

// Book holds the Unit for the Renter, retrying while other bookings race for the same calendar.
func (s *Service) Book(ctx context.Context, c Caller, unitID string, w domain.Window) (domain.Booking, error) {
	unit, err := s.Fleet.Get(ctx, c.Token, unitID)
	if err != nil {
		return domain.Booking{}, err
	}
	if !unit.Bookable() {
		return domain.Booking{}, ErrUnitNotBookable
	}
	id := s.NewID()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		cal, err := s.Store.Calendar(ctx, c.RentalID, unitID)
		if err != nil {
			return domain.Booking{}, err
		}
		now := s.Now().UTC()
		b := domain.Booking{
			ID:            id,
			Unit:          domain.UnitSnapshot{ID: unit.ID, Model: unit.Model, PlateNumber: unit.PlateNumber},
			RenterID:      c.Subject,
			RenterEmail:   c.Email,
			Window:        w,
			Price:         domain.Quote(w, rates(unit)),
			Status:        domain.StatusHeld,
			HoldExpiresAt: now.Add(s.Hold),
			CreatedAt:     now,
		}
		next, err := cal.Reserve(b.ID, domain.Reservation{Window: w, HoldExpiresAt: b.HoldExpiresAt}, now)
		if err != nil {
			return domain.Booking{}, err
		}
		created, err := bookingCreated(c.RentalID, b)
		if err != nil {
			return domain.Booking{}, err
		}
		err = s.Store.Create(ctx, c.RentalID, b, next, created)
		if errors.Is(err, store.ErrCalendarChanged) {
			backoff(attempt)
			continue
		}
		return b, err
	}
	return domain.Booking{}, ErrBusy
}

// Cancel ends a held Booking: Renters their own, Owners any with a reason.
func (s *Service) Cancel(ctx context.Context, c Caller, id, reason string) (domain.Booking, error) {
	reason = strings.TrimSpace(reason)
	if c.Role == auth.Owner && reason == "" {
		return domain.Booking{}, ErrReasonRequired
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		b, err := s.Get(ctx, c, id)
		if err != nil {
			return domain.Booking{}, err
		}
		now := s.Now().UTC()
		cancelled, err := b.Cancel(string(c.Role), reason, now)
		if err != nil {
			return domain.Booking{}, err
		}
		cal, err := s.Store.Calendar(ctx, c.RentalID, b.Unit.ID)
		if err != nil {
			return domain.Booking{}, err
		}
		event, err := bookingCancelled(c.RentalID, cancelled)
		if err != nil {
			return domain.Booking{}, err
		}
		err = s.Store.Cancel(ctx, c.RentalID, cancelled, cal.Release(b.ID, now), event)
		if errors.Is(err, store.ErrCalendarChanged) {
			backoff(attempt)
			continue
		}
		return cancelled, err
	}
	return domain.Booking{}, ErrBusy
}

// Get returns a Booking the caller may see; Renters only see their own.
func (s *Service) Get(ctx context.Context, c Caller, id string) (domain.Booking, error) {
	b, err := s.Store.Get(ctx, c.RentalID, id)
	if err != nil {
		return domain.Booking{}, err
	}
	if c.Role == auth.Renter && b.RenterID != c.Subject {
		return domain.Booking{}, domain.ErrNotFound
	}
	return b, nil
}

// Filter narrows a booking list; zero values mean no filter.
type Filter struct {
	From, To time.Time
	Status   domain.Status
}

// List returns the caller's Bookings: Renters their own, back-office everyone's, newest first.
func (s *Service) List(ctx context.Context, c Caller, f Filter) ([]domain.Booking, error) {
	all, err := s.Store.List(ctx, c.RentalID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	return slices.DeleteFunc(all, func(b domain.Booking) bool {
		switch {
		case c.Role == auth.Renter && b.RenterID != c.Subject:
			return true
		case f.Status != "" && b.EffectiveStatus(now) != f.Status:
			return true
		case !f.From.IsZero() && !b.Window.ReturnAt.After(f.From):
			return true
		case !f.To.IsZero() && !b.Window.PickupAt.Before(f.To):
			return true
		}
		return false
	}), nil
}

func rates(u fleet.Unit) domain.Rates {
	return domain.Rates{DailyPrice: u.DailyPrice, HourlyPrice: u.HourlyPrice, SecurityDeposit: u.SecurityDeposit}
}

// backoff spreads retries so racing writers do not collide again in lockstep.
func backoff(attempt int) {
	time.Sleep(time.Duration(5+rand.IntN(20*(attempt+1))) * time.Millisecond)
}
