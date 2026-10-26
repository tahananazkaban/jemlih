// Package api exposes the booking service over HTTP as described in its OpenAPI document.
package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/httpx"
	"github.com/tahananazkaban/jemlih/services/booking/internal/domain"
	"github.com/tahananazkaban/jemlih/services/booking/internal/fleet"
	"github.com/tahananazkaban/jemlih/services/booking/internal/service"
)

// Handler serves the booking endpoints; the Rental always comes from the caller's token.
type Handler struct {
	Bookings *service.Service
	Auth     *auth.Verifier
	Now      func() time.Time
}

// Routes returns the HTTP routes of the booking service.
func (h *Handler) Routes() http.Handler {
	anyRole := h.Auth.Require(auth.Owner, auth.Staff, auth.Renter)
	renterOnly := h.Auth.Require(auth.Renter)
	renterOrOwner := h.Auth.Require(auth.Renter, auth.Owner)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("GET /availability", anyRole(h.availability))
	mux.Handle("POST /bookings", renterOnly(h.createBooking))
	mux.Handle("GET /bookings", anyRole(h.listBookings))
	mux.Handle("GET /bookings/{id}", anyRole(h.getBooking))
	mux.Handle("POST /bookings/{id}/cancel", renterOrOwner(h.cancelBooking))
	return mux
}

func caller(r *http.Request) service.Caller {
	return service.Caller{Identity: auth.MustFromContext(r.Context()), Token: auth.BearerToken(r)}
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) availability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, ok := h.window(w, q.Get("pickup_at"), q.Get("return_at"))
	if !ok {
		return
	}
	offers, err := h.Bookings.Availability(r.Context(), caller(r), window)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	body := availabilityResponse{PickupAt: window.PickupAt, ReturnAt: window.ReturnAt, Units: make([]offerResponse, 0, len(offers))}
	for _, o := range offers {
		body.Units = append(body.Units, offerResponse{
			ID: o.Unit.ID, Model: o.Unit.Model, PlateNumber: o.Unit.PlateNumber, Transmission: o.Unit.Transmission,
			Price: toPrice(o.Price),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type bookingRequest struct {
	UnitID   *string `json:"unit_id"`
	PickupAt *string `json:"pickup_at"`
	ReturnAt *string `json:"return_at"`
}

func (h *Handler) createBooking(w http.ResponseWriter, r *http.Request) {
	var req bookingRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "invalid_body", Message: err.Error()})
		return
	}
	missing := map[string]string{}
	for name, absent := range map[string]bool{"unit_id": req.UnitID == nil || *req.UnitID == "", "pickup_at": req.PickupAt == nil, "return_at": req.ReturnAt == nil} {
		if absent {
			missing[name] = "is required"
		}
	}
	if len(missing) > 0 {
		validationFailed(w, missing)
		return
	}
	window, ok := h.window(w, *req.PickupAt, *req.ReturnAt)
	if !ok {
		return
	}

	b, err := h.Bookings.Book(r.Context(), caller(r), *req.UnitID, window)
	switch {
	case errors.Is(err, fleet.ErrUnitNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.Error{Code: "unit_not_found", Message: "unit not found"})
	case errors.Is(err, service.ErrUnitNotBookable):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "unit_not_bookable", Message: "this unit is not available for booking"})
	case errors.Is(err, domain.ErrUnitBooked):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "unit_unavailable", Message: "the unit is already booked for part of this window"})
	case errors.Is(err, service.ErrBusy):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.Error{Code: "busy", Message: err.Error()})
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		w.Header().Set("Location", "/bookings/"+b.ID)
		httpx.WriteJSON(w, http.StatusCreated, h.toResponse(b))
	}
}

func (h *Handler) listBookings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f service.Filter
	fields := map[string]string{}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				fields[name] = "must be an RFC 3339 timestamp"
			}
			*dst = t
		}
	}
	switch s := domain.Status(q.Get("status")); s {
	case "", domain.StatusHeld, domain.StatusCancelled, domain.StatusExpired:
		f.Status = s
	default:
		fields["status"] = "must be held, cancelled or expired"
	}
	if len(fields) > 0 {
		validationFailed(w, fields)
		return
	}
	bookings, err := h.Bookings.List(r.Context(), caller(r), f)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	body := bookingList{Bookings: make([]bookingResponse, 0, len(bookings))}
	for _, b := range bookings {
		body.Bookings = append(body.Bookings, h.toResponse(b))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) getBooking(w http.ResponseWriter, r *http.Request) {
	b, err := h.Bookings.Get(r.Context(), caller(r), r.PathValue("id"))
	if errors.Is(err, domain.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.toResponse(b))
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) cancelBooking(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "invalid_body", Message: err.Error()})
			return
		}
	}
	b, err := h.Bookings.Cancel(r.Context(), caller(r), r.PathValue("id"), req.Reason)
	var invalid *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		notFound(w)
	case errors.Is(err, service.ErrReasonRequired):
		validationFailed(w, map[string]string{"reason": "is required when an owner cancels"})
	case errors.As(err, &invalid):
		validationFailed(w, invalid.Fields)
	case errors.Is(err, domain.ErrNotCancellable):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "not_cancellable", Message: "only a held booking can be cancelled"})
	case errors.Is(err, service.ErrBusy):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.Error{Code: "busy", Message: err.Error()})
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, h.toResponse(b))
	}
}

// window parses and validates a rental period, writing a 400 when it is invalid.
func (h *Handler) window(w http.ResponseWriter, pickup, ret string) (domain.Window, bool) {
	fields := map[string]string{}
	p, err := time.Parse(time.RFC3339, pickup)
	if err != nil {
		fields["pickup_at"] = "must be an RFC 3339 timestamp"
	}
	rt, err := time.Parse(time.RFC3339, ret)
	if err != nil {
		fields["return_at"] = "must be an RFC 3339 timestamp"
	}
	if len(fields) > 0 {
		validationFailed(w, fields)
		return domain.Window{}, false
	}
	win, err := domain.NewWindow(p, rt, h.Now())
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		validationFailed(w, invalid.Fields)
		return domain.Window{}, false
	}
	return win, true
}

func validationFailed(w http.ResponseWriter, fields map[string]string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: fields})
}

func notFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, httpx.Error{Code: "not_found", Message: "booking not found"})
}
