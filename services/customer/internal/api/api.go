// Package api exposes the customer service over HTTP as described in its OpenAPI document.
package api

import (
	"errors"
	"net/http"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/httpx"
	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
	"github.com/tahananazkaban/jemlih/services/customer/internal/service"
)

// Handler serves the customer endpoints; the Rental always comes from the caller's token.
type Handler struct {
	Renters *service.Service
	Auth    *auth.Verifier
}

// Routes returns the HTTP routes of the customer service.
func (h *Handler) Routes() http.Handler {
	renterOnly := h.Auth.Require(auth.Renter)
	backOffice := h.Auth.Require(auth.Owner, auth.Staff)
	ownerOnly := h.Auth.Require(auth.Owner)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("GET /renters/me", renterOnly(h.me))
	mux.Handle("PUT /renters/me/profile", renterOnly(h.updateProfile))
	mux.Handle("POST /renters/me/documents/{type}/uploads", renterOnly(h.startUpload))
	mux.Handle("PUT /renters/me/documents/{type}", renterOnly(h.attachDocument))
	mux.Handle("GET /renters", backOffice(h.listRenters))
	mux.Handle("GET /renters/{id}", backOffice(h.getRenter))
	mux.Handle("GET /renters/{id}/documents", ownerOnly(h.documentLinks))
	mux.Handle("POST /renters/{id}/approve", ownerOnly(h.approve))
	mux.Handle("POST /renters/{id}/reject", ownerOnly(h.reject))
	return mux
}

func identity(r *http.Request) auth.Identity { return auth.MustFromContext(r.Context()) }

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	renter, err := h.Renters.Me(r.Context(), identity(r))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRenter(renter))
}

type profileRequest struct {
	FullName    *string `json:"full_name"`
	PhoneNumber *string `json:"phone_number"`
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(w, r, &req) {
		return
	}
	if missing := required(map[string]bool{"full_name": req.FullName == nil, "phone_number": req.PhoneNumber == nil}); len(missing) > 0 {
		validationFailed(w, missing)
		return
	}
	renter, err := h.Renters.UpdateProfile(r.Context(), identity(r), *req.FullName, *req.PhoneNumber)
	h.writeRenter(w, r, renter, err)
}

type uploadRequest struct {
	ContentType *string `json:"content_type"`
}

func (h *Handler) startUpload(w http.ResponseWriter, r *http.Request) {
	t, ok := documentType(w, r)
	if !ok {
		return
	}
	var req uploadRequest
	if !decode(w, r, &req) {
		return
	}
	if req.ContentType == nil {
		validationFailed(w, map[string]string{"content_type": "is required"})
		return
	}
	upload, err := h.Renters.StartUpload(r.Context(), identity(r), t, *req.ContentType)
	switch {
	case errors.Is(err, service.ErrUnsupportedType):
		validationFailed(w, map[string]string{"content_type": "must be image/jpeg, image/png or image/webp"})
	case err != nil:
		h.writeError(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusCreated, uploadResponse{
			UploadID: upload.ID, Method: http.MethodPut, URL: upload.URL, Headers: upload.Headers,
			ExpiresAt: upload.ExpiresAt, MaxSize: domain.MaxDocumentSize,
		})
	}
}

type attachRequest struct {
	UploadID *string `json:"upload_id"`
}

func (h *Handler) attachDocument(w http.ResponseWriter, r *http.Request) {
	t, ok := documentType(w, r)
	if !ok {
		return
	}
	var req attachRequest
	if !decode(w, r, &req) {
		return
	}
	if req.UploadID == nil {
		validationFailed(w, map[string]string{"upload_id": "is required"})
		return
	}
	renter, err := h.Renters.AttachDocument(r.Context(), identity(r), t, *req.UploadID)
	h.writeRenter(w, r, renter, err)
}

func (h *Handler) listRenters(w http.ResponseWriter, r *http.Request) {
	status := domain.Status(r.URL.Query().Get("status"))
	switch status {
	case "", domain.StatusUnverified, domain.StatusPending, domain.StatusVerified, domain.StatusRejected:
	default:
		validationFailed(w, map[string]string{"status": "must be unverified, pending, verified or rejected"})
		return
	}
	renters, err := h.Renters.List(r.Context(), identity(r), status)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	body := renterList{Renters: make([]renterResponse, 0, len(renters))}
	for _, renter := range renters {
		body.Renters = append(body.Renters, toRenter(renter))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) getRenter(w http.ResponseWriter, r *http.Request) {
	renter, err := h.Renters.Get(r.Context(), identity(r), r.PathValue("id"))
	h.writeRenter(w, r, renter, err)
}

func (h *Handler) documentLinks(w http.ResponseWriter, r *http.Request) {
	links, err := h.Renters.DocumentLinks(r.Context(), identity(r), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := documentLinkList{Documents: make([]documentLinkResponse, 0, len(links))}
	for _, l := range links {
		body.Documents = append(body.Documents, documentLinkResponse{
			documentResponse: toDocument(l.Type, l.Document), URL: l.URL, ExpiresAt: l.ExpiresAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	renter, err := h.Renters.Approve(r.Context(), identity(r), r.PathValue("id"))
	h.writeRenter(w, r, renter, err)
}

type rejectRequest struct {
	Reason    *string  `json:"reason"`
	Documents []string `json:"documents"`
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	var req rejectRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Reason == nil {
		validationFailed(w, map[string]string{"reason": "is required"})
		return
	}
	redo := make([]domain.DocumentType, 0, len(req.Documents))
	for _, d := range req.Documents {
		redo = append(redo, domain.DocumentType(d))
	}
	renter, err := h.Renters.Reject(r.Context(), identity(r), r.PathValue("id"), *req.Reason, redo)
	h.writeRenter(w, r, renter, err)
}

func (h *Handler) writeRenter(w http.ResponseWriter, r *http.Request, renter domain.Renter, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toRenter(renter))
}

// writeError maps every error a use case can return to its response.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *domain.ValidationError
	switch {
	case errors.As(err, &invalid):
		validationFailed(w, invalid.Fields)
	case errors.Is(err, domain.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.Error{Code: "not_found", Message: "renter not found"})
	case errors.Is(err, domain.ErrAlreadyVerified):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "already_verified", Message: "verified documents cannot be replaced"})
	case errors.Is(err, domain.ErrNameLocked):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "name_locked", Message: "the full name cannot change after verification"})
	case errors.Is(err, domain.ErrNotPending):
		httpx.WriteError(w, http.StatusConflict, httpx.Error{Code: "not_pending", Message: "only a verification waiting for review can be approved or rejected"})
	case errors.Is(err, service.ErrUploadNotFound):
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "upload_not_found", Message: "no file was uploaded for this upload ID"})
	case errors.Is(err, service.ErrFileTooLarge):
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "file_too_large", Message: "the uploaded file is larger than 10 MiB"})
	case errors.Is(err, service.ErrUnsupportedType):
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "unsupported_file_type", Message: "the uploaded file must be a JPEG, PNG or WebP image"})
	case errors.Is(err, service.ErrBusy):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.Error{Code: "busy", Message: err.Error()})
	default:
		httpx.InternalError(w, r, err)
	}
}

func documentType(w http.ResponseWriter, r *http.Request) (domain.DocumentType, bool) {
	t, ok := domain.ParseDocumentType(r.PathValue("type"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, httpx.Error{Code: "unknown_document_type", Message: "document type must be id_card, driving_licence or selfie"})
	}
	return t, ok
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "invalid_body", Message: err.Error()})
		return false
	}
	return true
}

func required(absent map[string]bool) map[string]string {
	missing := map[string]string{}
	for name, isAbsent := range absent {
		if isAbsent {
			missing[name] = "is required"
		}
	}
	return missing
}

func validationFailed(w http.ResponseWriter, fields map[string]string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.Error{Code: "validation_failed", Message: "some fields are invalid", Fields: fields})
}
