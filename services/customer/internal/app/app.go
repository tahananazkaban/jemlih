// Package app wires the customer service together; main and the tests both start it through New.
package app

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/customer/internal/api"
	"github.com/tahananazkaban/jemlih/services/customer/internal/documents"
	"github.com/tahananazkaban/jemlih/services/customer/internal/service"
	"github.com/tahananazkaban/jemlih/services/customer/internal/store"
)

// Config is everything the customer service needs to run.
type Config struct {
	AWS            aws.Config
	Auth           auth.Config
	CustomerTable  string
	DocumentBucket string
	// PublicEndpoint is the S3 address browsers use, when it differs from the service's (as in compose).
	PublicEndpoint string
	UploadTTL      time.Duration
	ReadTTL        time.Duration
}

// ConfigFromEnv reads the customer settings, leaving AWS to the caller.
func ConfigFromEnv(awsCfg aws.Config) (Config, error) {
	authCfg, err := auth.ConfigFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AWS:            awsCfg,
		Auth:           authCfg,
		CustomerTable:  env.String("CUSTOMER_TABLE", "jemlih-customer"),
		DocumentBucket: env.String("DOCUMENT_BUCKET", "jemlih-customer-documents"),
		PublicEndpoint: env.String("DOCUMENT_PUBLIC_ENDPOINT", ""),
	}
	for name, dst := range map[string]*time.Duration{"DOCUMENT_UPLOAD_TTL": &cfg.UploadTTL, "DOCUMENT_READ_TTL": &cfg.ReadTTL} {
		if *dst, err = time.ParseDuration(env.String(name, "0s")); err != nil {
			return Config{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return cfg, nil
}

// New returns the customer HTTP handler; zero TTLs mean 15 minutes to upload and 5 minutes to read.
func New(cfg Config) (http.Handler, error) {
	if cfg.CustomerTable == "" || cfg.DocumentBucket == "" {
		return nil, errors.New("customer: table and document bucket are required")
	}
	if cfg.UploadTTL == 0 {
		cfg.UploadTTL = 15 * time.Minute
	}
	if cfg.ReadTTL == 0 {
		cfg.ReadTTL = 5 * time.Minute
	}
	signer := s3Client(cfg.AWS)
	if cfg.PublicEndpoint != "" {
		signer = s3Client(cfg.AWS, func(o *s3.Options) { o.BaseEndpoint = aws.String(cfg.PublicEndpoint) })
	}
	h := &api.Handler{
		Renters: &service.Service{
			Store: store.New(dynamodb.NewFromConfig(cfg.AWS), cfg.CustomerTable),
			Bucket: &documents.Bucket{
				S3: s3Client(cfg.AWS), Presign: s3.NewPresignClient(signer), Name: cfg.DocumentBucket,
				UploadTTL: cfg.UploadTTL, ReadTTL: cfg.ReadTTL,
			},
			Now:   time.Now,
			NewID: func() string { return uuid.Must(uuid.NewV7()).String() },
		},
		Auth: auth.NewVerifier(cfg.Auth, &http.Client{Timeout: 5 * time.Second}),
	}
	return h.Routes(), nil
}

// s3Client uses path-style URLs with an endpoint override, since the emulator has no per-bucket hostnames.
func s3Client(cfg aws.Config, opts ...func(*s3.Options)) *s3.Client {
	return s3.NewFromConfig(cfg, append([]func(*s3.Options){func(o *s3.Options) { o.UsePathStyle = cfg.BaseEndpoint != nil }}, opts...)...)
}
