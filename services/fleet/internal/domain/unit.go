// Package domain holds the fleet business rules, free of transport and storage concerns.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Transmission is how a Unit changes gears.
type Transmission string

const (
	Automatic Transmission = "automatic"
	Manual    Transmission = "manual"
)

// Status is where a Unit is in its life in the fleet.
type Status string

const StatusAvailable Status = "available"

const (
	minYear        = 1990
	maxModelLength = 100
)

var (
	ErrNotFound       = errors.New("unit not found")
	ErrDuplicatePlate = errors.New("plate number already registered")
)

// Unit is one physical motorbike; money fields are integer rupiah.
type Unit struct {
	ID              string
	Model           string
	PlateNumber     string
	Year            int
	Transmission    Transmission
	DailyPrice      int64
	HourlyPrice     int64
	SecurityDeposit int64
	Status          Status
	CreatedAt       time.Time
}

// NewUnit is the Owner's input for adding a Unit to the fleet.
type NewUnit struct {
	Model           string
	PlateNumber     string
	Year            int
	Transmission    Transmission
	DailyPrice      int64
	HourlyPrice     int64
	SecurityDeposit int64
}

// ValidationError maps each invalid field to a human-readable reason.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid unit: %d field(s) rejected", len(e.Fields))
}

// Build validates the input and returns an available Unit with normalised fields.
func (n NewUnit) Build(id string, now time.Time) (Unit, error) {
	fields := map[string]string{}

	model := strings.TrimSpace(n.Model)
	switch {
	case model == "":
		fields["model"] = "is required"
	case len(model) > maxModelLength:
		fields["model"] = fmt.Sprintf("must be at most %d characters", maxModelLength)
	}

	plate, ok := NormalizePlate(n.PlateNumber)
	if !ok {
		fields["plate_number"] = "must look like an Indonesian plate, for example AB 1234 XY"
	}

	if maxYear := now.Year() + 1; n.Year < minYear || n.Year > maxYear {
		fields["year"] = fmt.Sprintf("must be between %d and %d", minYear, maxYear)
	}

	if n.Transmission != Automatic && n.Transmission != Manual {
		fields["transmission"] = "must be automatic or manual"
	}

	if n.DailyPrice <= 0 {
		fields["daily_price"] = "must be greater than 0"
	}
	switch {
	case n.HourlyPrice <= 0:
		fields["hourly_price"] = "must be greater than 0"
	case n.DailyPrice > 0 && n.HourlyPrice > n.DailyPrice:
		fields["hourly_price"] = "must not exceed daily_price"
	}
	if n.SecurityDeposit < 0 {
		fields["security_deposit"] = "must not be negative"
	}

	if len(fields) > 0 {
		return Unit{}, &ValidationError{Fields: fields}
	}
	return Unit{
		ID:              id,
		Model:           model,
		PlateNumber:     plate,
		Year:            n.Year,
		Transmission:    n.Transmission,
		DailyPrice:      n.DailyPrice,
		HourlyPrice:     n.HourlyPrice,
		SecurityDeposit: n.SecurityDeposit,
		Status:          StatusAvailable,
		CreatedAt:       now.UTC(),
	}, nil
}

// Region letters, number and optional suffix letters, e.g. "AB 1234 XY" or "B 1 A".
var platePattern = regexp.MustCompile(`^([A-Z]{1,2})([0-9]{1,4})([A-Z]{0,3})$`)

// NormalizePlate formats a plate as "AB 1234 XY" regardless of case and spacing.
func NormalizePlate(raw string) (string, bool) {
	compact := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
	m := platePattern.FindStringSubmatch(compact)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1] + " " + m[2] + " " + m[3]), true
}
