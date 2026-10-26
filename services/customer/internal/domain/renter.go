// Package domain holds the customer business rules: Renter profiles, identity documents and their verification.
package domain

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// DocumentType is one of the identity documents a Renter uploads.
type DocumentType string

const (
	IDCard         DocumentType = "id_card"
	DrivingLicence DocumentType = "driving_licence"
	Selfie         DocumentType = "selfie"
)

// DocumentTypes are the documents every Renter needs, in display order.
var DocumentTypes = []DocumentType{IDCard, DrivingLicence, Selfie}

// ParseDocumentType accepts only the known document types.
func ParseDocumentType(s string) (DocumentType, bool) {
	t := DocumentType(s)
	return t, slices.Contains(DocumentTypes, t)
}

// Status is where a Renter is in verification.
type Status string

const (
	StatusUnverified Status = "unverified"
	StatusPending    Status = "pending"
	StatusVerified   Status = "verified"
	StatusRejected   Status = "rejected"
)

// MaxDocumentSize bounds an uploaded photo; phone camera pictures stay well below it.
const MaxDocumentSize = 10 << 20

var documentContentTypes = []string{"image/jpeg", "image/png", "image/webp"}

// AllowedContentType reports whether a document may be stored with this media type.
func AllowedContentType(ct string) bool {
	return slices.Contains(documentContentTypes, ct)
}

const (
	maxNameLength   = 100
	maxReasonLength = 500
)

var (
	ErrNotFound        = errors.New("renter not found")
	ErrAlreadyVerified = errors.New("renter is already verified")
	ErrNotPending      = errors.New("verification is not waiting for review")
	ErrNameLocked      = errors.New("full name cannot change after verification")
)

// ValidationError maps each invalid field to a human-readable reason.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid input: %d field(s) rejected", len(e.Fields))
}

// Document is a stored identity document; Key locates it in the private bucket.
type Document struct {
	Key         string
	ContentType string
	Size        int64
	UploadedAt  time.Time
}

// Rejection is the Owner's reason and the documents the Renter must upload again.
type Rejection struct {
	Reason    string
	Documents []DocumentType
	By        string
	At        time.Time
}

// Renter is a renter's profile, documents and verification; ID is their user ID.
type Renter struct {
	ID              string
	Email           string
	FullName        string
	PhoneNumber     string
	Documents       map[DocumentType]Document
	Status          Status
	Rejection       *Rejection
	VerifiedBy      string
	VerifiedAt      time.Time
	StatusChangedAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Version is the stored version this value was read at; 0 means never saved.
	Version int64
}

// New is a Renter who has saved nothing yet.
func New(id, email string) Renter {
	return Renter{ID: id, Email: email, Status: StatusUnverified, Documents: map[DocumentType]Document{}}
}

// Missing lists the documents not uploaded yet, in display order.
func (r Renter) Missing() []DocumentType {
	missing := []DocumentType{}
	for _, t := range DocumentTypes {
		if _, ok := r.Documents[t]; !ok {
			missing = append(missing, t)
		}
	}
	return missing
}

// UpdateProfile sets the Renter's name and phone; a verified name stays as verified.
func (r Renter) UpdateProfile(fullName, phone string, now time.Time) (Renter, error) {
	fields := map[string]string{}
	name := strings.Join(strings.Fields(fullName), " ")
	switch n := utf8.RuneCountInString(name); {
	case n < 2:
		fields["full_name"] = "must be at least 2 characters"
	case n > maxNameLength:
		fields["full_name"] = fmt.Sprintf("must be at most %d characters", maxNameLength)
	}
	number, ok := NormalizePhone(phone)
	if !ok {
		fields["phone_number"] = "must be an Indonesian number starting with 0 or an international number starting with +"
	}
	if len(fields) > 0 {
		return Renter{}, &ValidationError{Fields: fields}
	}
	if r.Status == StatusVerified && name != r.FullName {
		return Renter{}, ErrNameLocked
	}
	next := r.clone()
	next.FullName, next.PhoneNumber = name, number
	next.touch(now)
	next.submitIfReady(now)
	return next, nil
}

// PutDocument stores d as the Renter's document of type t and returns the one it replaces, if any.
func (r Renter) PutDocument(t DocumentType, d Document, now time.Time) (Renter, Document, error) {
	if r.Status == StatusVerified {
		return Renter{}, Document{}, ErrAlreadyVerified
	}
	next := r.clone()
	replaced := next.Documents[t]
	next.Documents[t] = d
	if next.Rejection != nil {
		next.Rejection.Documents = slices.DeleteFunc(next.Rejection.Documents, func(x DocumentType) bool { return x == t })
	}
	next.touch(now)
	next.submitIfReady(now)
	return next, replaced, nil
}

// Approve verifies a Renter waiting for review.
func (r Renter) Approve(by string, now time.Time) (Renter, error) {
	if r.Status != StatusPending {
		return Renter{}, ErrNotPending
	}
	next := r.clone()
	next.VerifiedBy, next.VerifiedAt = by, now.UTC()
	next.setStatus(StatusVerified, now)
	next.touch(now)
	return next, nil
}

// Reject sends a Renter waiting for review back with a reason and the documents to upload again.
func (r Renter) Reject(by, reason string, documents []DocumentType, now time.Time) (Renter, error) {
	fields := map[string]string{}
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		fields["reason"] = "is required"
	case utf8.RuneCountInString(reason) > maxReasonLength:
		fields["reason"] = fmt.Sprintf("must be at most %d characters", maxReasonLength)
	}
	for _, t := range documents {
		if _, ok := ParseDocumentType(string(t)); !ok {
			fields["documents"] = "must list only id_card, driving_licence or selfie"
		}
	}
	redo := []DocumentType{}
	for _, t := range DocumentTypes {
		if slices.Contains(documents, t) {
			redo = append(redo, t)
		}
	}
	if len(fields) > 0 {
		return Renter{}, &ValidationError{Fields: fields}
	}
	if r.Status != StatusPending {
		return Renter{}, ErrNotPending
	}
	next := r.clone()
	next.Rejection = &Rejection{Reason: reason, Documents: redo, By: by, At: now.UTC()}
	next.setStatus(StatusRejected, now)
	next.touch(now)
	return next, nil
}

// submitIfReady moves a complete Renter to review once nothing the Owner asked for is outstanding.
func (r *Renter) submitIfReady(now time.Time) {
	if r.Status != StatusUnverified && r.Status != StatusRejected {
		return
	}
	if r.FullName == "" || r.PhoneNumber == "" || len(r.Missing()) > 0 {
		return
	}
	if r.Rejection != nil && len(r.Rejection.Documents) > 0 {
		return
	}
	r.Rejection = nil
	r.setStatus(StatusPending, now)
}

func (r *Renter) setStatus(s Status, now time.Time) {
	r.Status, r.StatusChangedAt = s, now.UTC()
}

func (r *Renter) touch(now time.Time) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now.UTC()
		r.StatusChangedAt = now.UTC()
	}
	r.UpdatedAt = now.UTC()
}

// clone copies the maps and slices so a change never leaks into the value it came from.
func (r Renter) clone() Renter {
	r.Documents = maps.Clone(r.Documents)
	if r.Documents == nil {
		r.Documents = map[DocumentType]Document{}
	}
	if r.Rejection != nil {
		rej := *r.Rejection
		rej.Documents = slices.Clone(rej.Documents)
		r.Rejection = &rej
	}
	return r
}

var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// NormalizePhone returns the number in E.164 form; a leading 0 means Indonesia (+62).
func NormalizePhone(raw string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -().", r) {
			return -1
		}
		return r
	}, raw)
	if rest, ok := strings.CutPrefix(compact, "0"); ok {
		compact = "+62" + rest
	}
	if !phonePattern.MatchString(compact) {
		return "", false
	}
	return compact, true
}
