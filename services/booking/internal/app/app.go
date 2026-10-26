// Package app wires the booking service together; main and the tests both start it through New.
package app

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/booking/internal/api"
	"github.com/tahananazkaban/jemlih/services/booking/internal/fleet"
	"github.com/tahananazkaban/jemlih/services/booking/internal/service"
	"github.com/tahananazkaban/jemlih/services/booking/internal/store"
)

// Config is everything the booking service needs to run.
type Config struct {
	AWS          aws.Config
	Auth         auth.Config
	BookingTable string
	FleetURL     string
	Hold         time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// ConfigFromEnv reads the booking settings, leaving AWS to the caller.
func ConfigFromEnv(awsCfg aws.Config) (Config, error) {
	authCfg, err := auth.ConfigFromEnv()
	if err != nil {
		return Config{}, err
	}
	hold, err := time.ParseDuration(env.String("BOOKING_HOLD", "2h"))
	if err != nil {
		return Config{}, fmt.Errorf("BOOKING_HOLD: %w", err)
	}
	return Config{
		AWS:          awsCfg,
		Auth:         authCfg,
		BookingTable: env.String("BOOKING_TABLE", "jemlih-booking"),
		FleetURL:     env.String("FLEET_URL", "http://localhost:8081"),
		Hold:         hold,
	}, nil
}

// New returns the booking HTTP handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.BookingTable == "" || cfg.FleetURL == "" || cfg.Hold <= 0 {
		return nil, errors.New("booking: table, fleet URL and a positive hold are required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := &http.Client{Timeout: 5 * time.Second}
	h := &api.Handler{
		Bookings: &service.Service{
			Store: store.New(dynamodb.NewFromConfig(cfg.AWS), cfg.BookingTable),
			Fleet: &fleet.Client{BaseURL: cfg.FleetURL, HTTP: client},
			Hold:  cfg.Hold,
			Now:   now,
			NewID: func() string { return uuid.Must(uuid.NewV7()).String() },
		},
		Auth: auth.NewVerifier(cfg.Auth, client),
		Now:  now,
	}
	return h.Routes(), nil
}
