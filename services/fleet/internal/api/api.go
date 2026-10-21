// Package api exposes the fleet service over HTTP as described in its OpenAPI document.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/httpx"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/domain"
)

// UnitStore is the persistence the API needs.
type UnitStore interface {
	Create(ctx context.Context, rentalID string, u domain.Unit) error
	Get(ctx context.Context, rentalID, id string) (domain.Unit, error)
	List(ctx context.Context, rentalID string) ([]domain.Unit, error)
}

// Handler serves the fleet endpoints for a single Rental.
type Handler struct {
	Units    UnitStore
	RentalID string
	Now      func() time.Time
	NewID    func() string
}

// Routes returns the HTTP routes of the fleet service.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /units", h.createUnit)
	mux.HandleFunc("GET /units", h.listUnits)
	mux.HandleFunc("GET /units/{id}", h.getUnit)
	return mux
}

// unitRequest uses pointers so a missing field is not mistaken for its zero value.
type unitRequest struct {
	Model           *string `json:"model"`
	PlateNumber     *string `json:"plate_number"`
	Year            *int    `json:"year"`
	Transmission    *string `json:"transmission"`
	DailyPrice      *int64  `json:"daily_price"`
	HourlyPrice     *int64  `json:"hourly_price"`
	SecurityDeposit *int64  `json:"security_deposit"`
}

func (r unitRequest) missingFields() map[string]string {
	missing := map[string]string{}
	for name, absent := range map[string]bool{
		"model":            r.Model == nil,
		"plate_number":     r.PlateNumber == nil,
		"year":             r.Year == nil,
		"transmission":     r.Transmission == nil,
		"daily_price":      r.DailyPrice == nil,
		"hourly_price":     r.HourlyPrice == nil,
		"security_deposit": r.SecurityDeposit == nil,
	} {
		if absent {
			missing[name] = "is required"
		}
	}
	return missing
}

type unitResponse struct {
	ID              string    `json:"id"`
	Model           string    `json:"model"`
	PlateNumber     string    `json:"plate_number"`
	Year            int       `json:"year"`
	Transmission    string    `json:"transmission"`
	DailyPrice      int64     `json:"daily_price"`
	HourlyPrice     int64     `json:"hourly_price"`
	SecurityDeposit int64     `json:"security_deposit"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

type unitList struct {
	Units []unitResponse `json:"units"`
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) createUnit(w http.ResponseWriter, r *http.Request) {
	var req unitRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "invalid_body", Message: err.Error()})
		return
	}
	if missing := req.missingFields(); len(missing) > 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: missing})
		return
	}
	unit, err := domain.NewUnit{
		Model:           *req.Model,
		PlateNumber:     *req.PlateNumber,
		Year:            *req.Year,
		Transmission:    domain.Transmission(*req.Transmission),
		DailyPrice:      *req.DailyPrice,
		HourlyPrice:     *req.HourlyPrice,
		SecurityDeposit: *req.SecurityDeposit,
	}.Build(h.NewID(), h.Now())
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: invalid.Fields})
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}

	err = h.Units.Create(r.Context(), h.RentalID, unit)
	if errors.Is(err, domain.ErrDuplicatePlate) {
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "duplicate_plate", Message: "a unit with this plate number already exists"})
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.Header().Set("Location", "/units/"+unit.ID)
	httpx.WriteJSON(w, http.StatusCreated, toResponse(unit))
}

func (h *Handler) listUnits(w http.ResponseWriter, r *http.Request) {
	units, err := h.Units.List(r.Context(), h.RentalID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	body := unitList{Units: make([]unitResponse, 0, len(units))}
	for _, u := range units {
		body.Units = append(body.Units, toResponse(u))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) getUnit(w http.ResponseWriter, r *http.Request) {
	unit, err := h.Units.Get(r.Context(), h.RentalID, r.PathValue("id"))
	if errors.Is(err, domain.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.Error{Code: "not_found", Message: "unit not found"})
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toResponse(unit))
}

func toResponse(u domain.Unit) unitResponse {
	return unitResponse{
		ID:              u.ID,
		Model:           u.Model,
		PlateNumber:     u.PlateNumber,
		Year:            u.Year,
		Transmission:    string(u.Transmission),
		DailyPrice:      u.DailyPrice,
		HourlyPrice:     u.HourlyPrice,
		SecurityDeposit: u.SecurityDeposit,
		Status:          string(u.Status),
		CreatedAt:       u.CreatedAt,
	}
}
