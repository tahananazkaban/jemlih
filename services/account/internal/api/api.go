// Package api exposes the account service over HTTP as described in its OpenAPI document.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/httpx"
	"github.com/tahananazkaban/jemlih/services/account/internal/domain"
)

// Members is the account directory the API needs.
type Members interface {
	Create(ctx context.Context, rentalID string, role auth.Role, m domain.NewMember) (domain.Member, string, error)
	ListGroup(ctx context.Context, rentalID string, role auth.Role) ([]domain.Member, error)
}

// Handler serves the account endpoints; the Rental always comes from the caller's token.
type Handler struct {
	Members Members
	Auth    *auth.Verifier
}

// Routes returns the HTTP routes of the account service.
func (h *Handler) Routes() http.Handler {
	anyRole := h.Auth.Require(auth.Owner, auth.Staff, auth.Renter)
	ownerOnly := h.Auth.Require(auth.Owner)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("GET /me", anyRole(h.me))
	mux.Handle("POST /staff", ownerOnly(h.createStaff))
	mux.Handle("GET /staff", ownerOnly(h.listStaff))
	return mux
}

type meResponse struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	RentalID string `json:"rental_id"`
}

type staffRequest struct {
	Email *string `json:"email"`
	Name  *string `json:"name"`
}

type memberResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type createdStaffResponse struct {
	memberResponse
	TemporaryPassword string `json:"temporary_password"`
}

type staffList struct {
	Staff []memberResponse `json:"staff"`
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id := auth.MustFromContext(r.Context())
	httpx.WriteJSON(w, http.StatusOK, meResponse{ID: id.Subject, Email: id.Email, Role: string(id.Role), RentalID: id.RentalID})
}

func (h *Handler) createStaff(w http.ResponseWriter, r *http.Request) {
	var req staffRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "invalid_body", Message: err.Error()})
		return
	}
	missing := map[string]string{}
	if req.Email == nil {
		missing["email"] = "is required"
	}
	if req.Name == nil {
		missing["name"] = "is required"
	}
	if len(missing) > 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: missing})
		return
	}

	input, err := domain.NewMember{Email: *req.Email, Name: *req.Name}.Normalize()
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: invalid.Fields})
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}

	rentalID := auth.MustFromContext(r.Context()).RentalID
	member, password, err := h.Members.Create(r.Context(), rentalID, auth.Staff, input)
	if errors.Is(err, domain.ErrEmailTaken) {
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "email_taken", Message: "an account with this email already exists"})
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createdStaffResponse{memberResponse: toResponse(member), TemporaryPassword: password})
}

func (h *Handler) listStaff(w http.ResponseWriter, r *http.Request) {
	members, err := h.Members.ListGroup(r.Context(), auth.MustFromContext(r.Context()).RentalID, auth.Staff)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	body := staffList{Staff: make([]memberResponse, 0, len(members))}
	for _, m := range members {
		body.Staff = append(body.Staff, toResponse(m))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func toResponse(m domain.Member) memberResponse {
	return memberResponse{ID: m.ID, Email: m.Email, Name: m.Name, Status: m.Status, CreatedAt: m.CreatedAt}
}
