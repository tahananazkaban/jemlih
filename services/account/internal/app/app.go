// Package app wires the account service together; main and the tests both start it through New.
package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/account/internal/api"
	"github.com/tahananazkaban/jemlih/services/account/internal/directory"
)

// Config is everything the account service needs to run.
type Config struct {
	AWS        aws.Config
	Auth       auth.Config
	UserPoolID string
}

// ConfigFromEnv reads the account settings, leaving AWS to the caller.
func ConfigFromEnv(awsCfg aws.Config) (Config, error) {
	authCfg, err := auth.ConfigFromEnv()
	if err != nil {
		return Config{}, err
	}
	return Config{AWS: awsCfg, Auth: authCfg, UserPoolID: env.String("COGNITO_USER_POOL_ID", "")}, nil
}

// New returns the account HTTP handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.UserPoolID == "" {
		return nil, errors.New("account: COGNITO_USER_POOL_ID is required")
	}
	h := &api.Handler{
		Members: directory.New(cip.NewFromConfig(cfg.AWS), cfg.UserPoolID),
		Auth:    auth.NewVerifier(cfg.Auth, &http.Client{Timeout: 5 * time.Second}),
	}
	return h.Routes(), nil
}
