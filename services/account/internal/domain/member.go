// Package domain holds the account business rules, free of transport and storage concerns.
package domain

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const maxNameLength = 100

var ErrEmailTaken = errors.New("email already registered")

// Member is a back-office account (Owner or Staff) of a Rental.
type Member struct {
	ID        string
	Email     string
	Name      string
	Status    string
	CreatedAt time.Time
}

// NewMember is the input for creating a back-office account.
type NewMember struct {
	Email string
	Name  string
}

// ValidationError maps each invalid field to a human-readable reason.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid member: %d field(s) rejected", len(e.Fields))
}

// Normalize validates the input and returns it with a lower-case email and trimmed name.
func (n NewMember) Normalize() (NewMember, error) {
	fields := map[string]string{}

	email := strings.ToLower(strings.TrimSpace(n.Email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		fields["email"] = "must be a valid email address"
	}

	name := strings.TrimSpace(n.Name)
	switch {
	case name == "":
		fields["name"] = "is required"
	case len(name) > maxNameLength:
		fields["name"] = fmt.Sprintf("must be at most %d characters", maxNameLength)
	}

	if len(fields) > 0 {
		return NewMember{}, &ValidationError{Fields: fields}
	}
	return NewMember{Email: email, Name: name}, nil
}
