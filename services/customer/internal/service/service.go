// Package service runs the customer use cases: profiles, document uploads and verification review.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/services/customer/internal/documents"
	"github.com/tahananazkaban/jemlih/services/customer/internal/domain"
	"github.com/tahananazkaban/jemlih/services/customer/internal/store"
)

// maxAttempts bounds retries when the Renter and the Owner change the same record at once.
const maxAttempts = 5

var (
	ErrUnsupportedFile = errors.New("uploaded file is not an accepted image")
	ErrUploadNotFound  = errors.New("nothing was uploaded for this upload ID")
	ErrFileTooLarge    = errors.New("uploaded file is too large")
	ErrBusy            = errors.New("too many concurrent changes, try again")
)

// Service implements the customer use cases on top of the store and the document bucket.
type Service struct {
	Store  *store.Store
	Bucket *documents.Bucket
	Now    func() time.Time
	NewID  func() string
}

// Upload is where the browser sends one document, before it is attached to the Renter.
type Upload struct {
	ID string
	documents.Link
}

// Me returns the caller's record, or an empty unverified one when they have saved nothing.
func (s *Service) Me(ctx context.Context, c auth.Identity) (domain.Renter, error) {
	r, err := s.Store.Get(ctx, c.RentalID, c.Subject)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.New(c.Subject, c.Email), nil
	}
	return r, err
}

// UpdateProfile saves the caller's name and phone number.
func (s *Service) UpdateProfile(ctx context.Context, c auth.Identity, fullName, phone string) (domain.Renter, error) {
	return s.changeMine(ctx, c, func(r domain.Renter) (domain.Renter, error) {
		return r.UpdateProfile(fullName, phone, s.Now())
	})
}

// StartUpload returns a presigned URL for one file of exactly size bytes; it lands in a staging area until attached.
func (s *Service) StartUpload(ctx context.Context, c auth.Identity, t domain.DocumentType, contentType string, size int64) (Upload, error) {
	fields := map[string]string{}
	if !domain.AllowedContentType(contentType) {
		fields["content_type"] = "must be image/jpeg, image/png or image/webp"
	}
	if size <= 0 || size > domain.MaxDocumentSize {
		fields["size"] = "must be between 1 byte and 10 MiB"
	}
	if len(fields) > 0 {
		return Upload{}, &domain.ValidationError{Fields: fields}
	}
	if err := checkKeyParts(c); err != nil {
		return Upload{}, err
	}
	r, err := s.Me(ctx, c)
	if err != nil {
		return Upload{}, err
	}
	if r.Status == domain.StatusVerified {
		return Upload{}, domain.ErrAlreadyVerified
	}
	id := s.NewID()
	link, err := s.Bucket.UploadLink(ctx, stagingKey(c, t, id), contentType, size)
	if err != nil {
		return Upload{}, err
	}
	return Upload{ID: id, Link: link}, nil
}

// AttachDocument checks an uploaded file and makes it the caller's document of type t.
func (s *Service) AttachDocument(ctx context.Context, c auth.Identity, t domain.DocumentType, uploadID string) (domain.Renter, error) {
	if _, err := uuid.Parse(uploadID); err != nil {
		return domain.Renter{}, &domain.ValidationError{Fields: map[string]string{"upload_id": "is not an upload ID"}}
	}
	if err := checkKeyParts(c); err != nil {
		return domain.Renter{}, err
	}
	staged := stagingKey(c, t, uploadID)
	obj, err := s.Bucket.Head(ctx, staged)
	if errors.Is(err, documents.ErrNoObject) {
		return domain.Renter{}, ErrUploadNotFound
	}
	if err != nil {
		return domain.Renter{}, err
	}
	switch {
	case obj.Size > domain.MaxDocumentSize:
		s.discard(ctx, staged)
		return domain.Renter{}, ErrFileTooLarge
	case !domain.AllowedContentType(obj.ContentType):
		s.discard(ctx, staged)
		return domain.Renter{}, ErrUnsupportedFile
	}
	// Checked before copying so a verified Renter's upload costs nothing more.
	current, err := s.Me(ctx, c)
	if err != nil {
		return domain.Renter{}, err
	}
	if current.Status == domain.StatusVerified {
		s.discard(ctx, staged)
		return domain.Renter{}, domain.ErrAlreadyVerified
	}

	// Copied out of staging so the lifecycle rule never expires an attached document.
	kept := documentKey(c, t, uploadID)
	if err := s.Bucket.Copy(ctx, staged, kept); err != nil {
		return domain.Renter{}, err
	}
	var replaced domain.Document
	saved, err := s.changeMine(ctx, c, func(r domain.Renter) (domain.Renter, error) {
		now := s.Now()
		next, old, err := r.PutDocument(t, domain.Document{Key: kept, ContentType: obj.ContentType, Size: obj.Size, UploadedAt: now.UTC()}, now)
		replaced = old
		return next, err
	})
	if err != nil {
		s.discardUnless(ctx, c, kept)
		return domain.Renter{}, err
	}
	s.discard(ctx, staged)
	if replaced.Key != "" && replaced.Key != kept {
		s.discard(ctx, replaced.Key)
	}
	return saved, nil
}

// Get returns a Renter of the caller's Rental.
func (s *Service) Get(ctx context.Context, c auth.Identity, id string) (domain.Renter, error) {
	return s.Store.Get(ctx, c.RentalID, id)
}

// List returns the Rental's Renters, optionally in one status, longest in that status first.
func (s *Service) List(ctx context.Context, c auth.Identity, status domain.Status) ([]domain.Renter, error) {
	all, err := s.Store.List(ctx, c.RentalID)
	if err != nil {
		return nil, err
	}
	renters := []domain.Renter{}
	for _, r := range all {
		if status == "" || r.Status == status {
			renters = append(renters, r)
		}
	}
	slices.SortStableFunc(renters, func(a, b domain.Renter) int { return a.StatusChangedAt.Compare(b.StatusChangedAt) })
	return renters, nil
}

// DocumentLink is a short-lived read URL for one stored document.
type DocumentLink struct {
	Type     domain.DocumentType
	Document domain.Document
	documents.Link
}

// DocumentLinks are read URLs for the Renter's documents at one version of the Renter.
type DocumentLinks struct {
	Version   int64
	Documents []DocumentLink
}

// DocumentLinks returns read URLs for every document the Renter has uploaded.
func (s *Service) DocumentLinks(ctx context.Context, c auth.Identity, id string) (DocumentLinks, error) {
	r, err := s.Get(ctx, c, id)
	if err != nil {
		return DocumentLinks{}, err
	}
	links := DocumentLinks{Version: r.Version, Documents: []DocumentLink{}}
	for _, t := range domain.DocumentTypes {
		d, ok := r.Documents[t]
		if !ok {
			continue
		}
		link, err := s.Bucket.ReadLink(ctx, d.Key)
		if err != nil {
			return DocumentLinks{}, err
		}
		links.Documents = append(links.Documents, DocumentLink{Type: t, Document: d, Link: link})
	}
	return links, nil
}

// Approve verifies a Renter waiting for review, at the version the Owner reviewed, and publishes RenterVerified.
func (s *Service) Approve(ctx context.Context, c auth.Identity, id string, reviewed int64) (domain.Renter, error) {
	return s.review(ctx, c, id, func(r domain.Renter) (domain.Renter, error) {
		next, err := r.Approve(c.Subject, reviewed, s.Now())
		if err != nil {
			return domain.Renter{}, err
		}
		event, err := renterVerified(c.RentalID, next)
		if err != nil {
			return domain.Renter{}, err
		}
		return s.Store.Save(ctx, c.RentalID, next, event)
	})
}

// Reject returns a Renter waiting for review with a reason and publishes RenterVerificationRejected.
func (s *Service) Reject(ctx context.Context, c auth.Identity, id string, reviewed int64, reason string, redo []domain.DocumentType) (domain.Renter, error) {
	return s.review(ctx, c, id, func(r domain.Renter) (domain.Renter, error) {
		next, err := r.Reject(c.Subject, reviewed, reason, redo, s.Now())
		if err != nil {
			return domain.Renter{}, err
		}
		event, err := renterVerificationRejected(c.RentalID, next)
		if err != nil {
			return domain.Renter{}, err
		}
		return s.Store.Save(ctx, c.RentalID, next, event)
	})
}

// review reads the Renter and lets decideAndSave record the Owner's decision, retrying a lost race.
func (s *Service) review(ctx context.Context, c auth.Identity, id string, decideAndSave func(domain.Renter) (domain.Renter, error)) (domain.Renter, error) {
	var saved domain.Renter
	err := retry(func() error {
		r, err := s.Get(ctx, c, id)
		if err != nil {
			return err
		}
		saved, err = decideAndSave(r)
		return err
	})
	return saved, err
}

// changeMine applies change to the caller's record, creating it on first save.
func (s *Service) changeMine(ctx context.Context, c auth.Identity, change func(domain.Renter) (domain.Renter, error)) (domain.Renter, error) {
	var saved domain.Renter
	err := retry(func() error {
		r, err := s.Me(ctx, c)
		if err != nil {
			return err
		}
		next, err := change(r)
		if err != nil {
			return err
		}
		next.Email = c.Email
		saved, err = s.Store.Save(ctx, c.RentalID, next)
		return err
	})
	return saved, err
}

// retry runs attempt again after a lost race, with a growing random pause, up to maxAttempts times.
func retry(attempt func() error) error {
	for i := 0; i < maxAttempts; i++ {
		err := attempt()
		if !errors.Is(err, store.ErrConcurrentChange) {
			return err
		}
		time.Sleep(time.Duration(5+rand.IntN(20*(i+1))) * time.Millisecond)
	}
	return ErrBusy
}

// discard deletes an object that is no longer needed; a failure only leaves an orphan behind.
func (s *Service) discard(ctx context.Context, key string) {
	if err := s.Bucket.Delete(ctx, key); err != nil {
		slog.WarnContext(ctx, "discard document", "key", key, "err", err)
	}
}

// discardUnless deletes key unless the caller's record already points at it, as after a racing attach of the same upload.
func (s *Service) discardUnless(ctx context.Context, c auth.Identity, key string) {
	r, err := s.Me(ctx, c)
	if err != nil {
		return
	}
	for _, d := range r.Documents {
		if d.Key == key {
			return
		}
	}
	s.discard(ctx, key)
}

var keyPart = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// checkKeyParts refuses token claims that could reshape an object key, such as "/" or "..".
func checkKeyParts(c auth.Identity) error {
	if !keyPart.MatchString(c.RentalID) || !keyPart.MatchString(c.Subject) {
		return fmt.Errorf("rental %q or user %q cannot be part of an object key", c.RentalID, c.Subject)
	}
	return nil
}

// stagingKey is where an upload waits; the "uploads/" prefix expires by lifecycle rule.
func stagingKey(c auth.Identity, t domain.DocumentType, uploadID string) string {
	return "uploads/" + c.RentalID + "/" + c.Subject + "/" + string(t) + "/" + uploadID
}

func documentKey(c auth.Identity, t domain.DocumentType, uploadID string) string {
	return "documents/" + c.RentalID + "/" + c.Subject + "/" + string(t) + "/" + uploadID
}
