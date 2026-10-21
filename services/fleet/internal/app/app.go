// Package app wires the fleet service together; main and the tests both start it through New.
package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"

	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/api"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/store"
)

// Config is everything the fleet service needs to run.
type Config struct {
	AWS       aws.Config
	UnitTable string
	RentalID  string
}

// ConfigFromEnv reads the fleet settings, leaving AWS to the caller.
func ConfigFromEnv(awsCfg aws.Config) Config {
	return Config{
		AWS:       awsCfg,
		UnitTable: env.String("FLEET_TABLE", "jemlih-fleet"),
		RentalID:  env.String("RENTAL_ID", "default"),
	}
}

// New returns the fleet HTTP handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.UnitTable == "" || cfg.RentalID == "" {
		return nil, errors.New("fleet: unit table and rental ID are required")
	}
	h := &api.Handler{
		Units:    store.NewUnits(dynamodb.NewFromConfig(cfg.AWS), cfg.UnitTable),
		RentalID: cfg.RentalID,
		Now:      time.Now,
		NewID:    func() string { return uuid.Must(uuid.NewV7()).String() },
	}
	return h.Routes(), nil
}
