// Command postconfirm is the Cognito post-confirmation trigger that turns a self-registered user into a Renter.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/awsx"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/account/internal/directory"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "account-postconfirm"))
	rentalID := env.String("RENTAL_ID", "")
	if rentalID == "" {
		slog.Error("RENTAL_ID is required")
		os.Exit(1)
	}
	awsCfg, err := awsx.Load(context.Background())
	if err != nil {
		slog.Error("load aws config", "err", err)
		os.Exit(1)
	}
	client := cip.NewFromConfig(awsCfg)

	lambda.Start(func(ctx context.Context, e events.CognitoEventUserPoolsPostConfirmation) (events.CognitoEventUserPoolsPostConfirmation, error) {
		// Password resets also confirm users; only a fresh sign-up becomes a Renter.
		if e.TriggerSource != "PostConfirmation_ConfirmSignUp" {
			return e, nil
		}
		dir := directory.New(client, e.UserPoolID)
		if err := dir.AssignRental(ctx, e.UserName, rentalID); err != nil {
			return e, fmt.Errorf("user %s: %w", e.UserName, err)
		}
		if err := dir.AddToGroup(ctx, e.UserName, auth.Renter); err != nil {
			return e, fmt.Errorf("user %s: %w", e.UserName, err)
		}
		slog.InfoContext(ctx, "renter registered", "user", e.UserName, "rental_id", rentalID)
		return e, nil
	})
}
