// Package app wires the fleet service together; main and the tests both start it through New.
package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/api"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/store"
)

// Config is everything the fleet service needs to run.
type Config struct {
	AWS       aws.Config
	Auth      auth.Config
	UnitTable string
}

// ConfigFromEnv reads the fleet settings, leaving AWS to the caller.
func ConfigFromEnv(awsCfg aws.Config) (Config, error) {
	authCfg, err := auth.ConfigFromEnv()
	if err != nil {
		return Config{}, err
	}
	return Config{
		AWS:       awsCfg,
		Auth:      authCfg,
		UnitTable: env.String("FLEET_TABLE", "jemlih-fleet"),
	}, nil
}

// New returns the fleet HTTP handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.UnitTable == "" {
		return nil, errors.New("fleet: unit table is required")
	}
	h := &api.Handler{
		Units: store.NewUnits(dynamodb.NewFromConfig(cfg.AWS), cfg.UnitTable),
		Auth:  auth.NewVerifier(cfg.Auth, &http.Client{Timeout: 5 * time.Second}),
		Now:   time.Now,
		NewID: func() string { return uuid.Must(uuid.NewV7()).String() },
	}
	return h.Routes(), nil
}
