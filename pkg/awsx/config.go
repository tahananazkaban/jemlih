// Package awsx builds AWS SDK configuration that can target the local emulator.
package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/tahananazkaban/jemlih/pkg/env"
)

// EndpointVar overrides the AWS endpoint for every service client; unset means real AWS.
const EndpointVar = "JEMLIH_AWS_ENDPOINT"

// Load returns an AWS config honouring AWS_REGION and the endpoint override.
func Load(ctx context.Context) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(env.String("AWS_REGION", "us-east-1")))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load aws config: %w", err)
	}
	if endpoint := env.String(EndpointVar, ""); endpoint != "" {
		cfg.BaseEndpoint = aws.String(endpoint)
	}
	return cfg, nil
}
