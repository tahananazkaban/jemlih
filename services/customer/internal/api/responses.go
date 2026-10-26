package api

import (
	"time"

	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
)

type documentResponse struct {
	Type        string    `json:"type"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

type rejectionResponse struct {
	Reason     string    `json:"reason"`
	Documents  []string  `json:"documents"`
	RejectedAt time.Time `json:"rejected_at"`
}

type renterResponse struct {
	ID               string             `json:"id"`
	Email            string             `json:"email"`
	FullName         string             `json:"full_name"`
	PhoneNumber      string             `json:"phone_number"`
	Status           string             `json:"status"`
	Documents        []documentResponse `json:"documents"`
	MissingDocuments []string           `json:"missing_documents"`
	Rejection        *rejectionResponse `json:"rejection,omitempty"`
	VerifiedAt       *time.Time         `json:"verified_at,omitempty"`
	StatusChangedAt  *time.Time         `json:"status_changed_at,omitempty"`
	CreatedAt        *time.Time         `json:"created_at,omitempty"`
	UpdatedAt        *time.Time         `json:"updated_at,omitempty"`
}

type renterList struct {
	Renters []renterResponse `json:"renters"`
}

type uploadResponse struct {
	UploadID  string            `json:"upload_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	MaxSize   int64             `json:"max_size"`
}

type documentLinkResponse struct {
	documentResponse
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type documentLinkList struct {
	Documents []documentLinkResponse `json:"documents"`
}

func toDocument(t domain.DocumentType, d domain.Document) documentResponse {
	return documentResponse{Type: string(t), ContentType: d.ContentType, Size: d.Size, UploadedAt: d.UploadedAt}
}

// toRenter never includes storage keys; documents are reached only through presigned links.
func toRenter(r domain.Renter) renterResponse {
	body := renterResponse{
		ID:               r.ID,
		Email:            r.Email,
		FullName:         r.FullName,
		PhoneNumber:      r.PhoneNumber,
		Status:           string(r.Status),
		Documents:        []documentResponse{},
		MissingDocuments: []string{},
		VerifiedAt:       optional(r.VerifiedAt),
		StatusChangedAt:  optional(r.StatusChangedAt),
		CreatedAt:        optional(r.CreatedAt),
		UpdatedAt:        optional(r.UpdatedAt),
	}
	for _, t := range domain.DocumentTypes {
		if d, ok := r.Documents[t]; ok {
			body.Documents = append(body.Documents, toDocument(t, d))
		}
	}
	for _, t := range r.Missing() {
		body.MissingDocuments = append(body.MissingDocuments, string(t))
	}
	if rej := r.Rejection; rej != nil {
		body.Rejection = &rejectionResponse{Reason: rej.Reason, Documents: []string{}, RejectedAt: rej.At}
		for _, t := range rej.Documents {
			body.Rejection.Documents = append(body.Rejection.Documents, string(t))
		}
	}
	return body
}

func optional(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
