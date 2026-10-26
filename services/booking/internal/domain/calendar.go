package domain

import "time"

// Reservation is the part of a Booking that blocks its Unit.
type Reservation struct {
	Window        Window
	HoldExpiresAt time.Time
}

// Calendar lists the reservations that may still block one Unit; Version guards concurrent changes.
type Calendar struct {
	UnitID       string
	Version      int64
	Reservations map[string]Reservation
}

// blocks reports whether a reservation still holds the Unit at now.
func (r Reservation) blocks(now time.Time) bool {
	held := r.HoldExpiresAt.IsZero() || now.Before(r.HoldExpiresAt)
	return held && now.Before(r.Window.ReturnAt)
}

// Free reports whether no live reservation overlaps w.
func (c Calendar) Free(w Window, now time.Time) bool {
	for _, r := range c.Reservations {
		if r.blocks(now) && r.Window.Overlaps(w) {
			return false
		}
	}
	return true
}

// Reserve adds a Booking to the calendar and drops reservations that can no longer block.
func (c Calendar) Reserve(bookingID string, r Reservation, now time.Time) (Calendar, error) {
	if !c.Free(r.Window, now) {
		return Calendar{}, ErrUnitBooked
	}
	next := c.pruned(now)
	next.Reservations[bookingID] = r
	return next, nil
}

// Release removes a Booking from the calendar.
func (c Calendar) Release(bookingID string, now time.Time) Calendar {
	next := c.pruned(now)
	delete(next.Reservations, bookingID)
	return next
}

func (c Calendar) pruned(now time.Time) Calendar {
	next := Calendar{UnitID: c.UnitID, Version: c.Version, Reservations: map[string]Reservation{}}
	for id, r := range c.Reservations {
		if r.blocks(now) {
			next.Reservations[id] = r
		}
	}
	return next
}
