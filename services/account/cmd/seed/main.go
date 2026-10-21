// Command seed creates the first Owner of the local Rental so someone can sign in.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/awsx"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/services/account/internal/directory"
	"github.com/tahananazkaban/jemlih/services/account/internal/domain"
)

// passwordVar holds the optional permanent password, kept out of argv so it does not show in ps.
const passwordVar = "SEED_OWNER_PASSWORD"

func main() {
	email := flag.String("email", "owner@jemlih.local", "Owner email, also the sign-in username")
	name := flag.String("name", "Local Owner", "Owner display name")
	flag.Parse()

	if err := run(*email, *name, os.Getenv(passwordVar)); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(email, name, password string) error {
	ctx := context.Background()
	poolID := env.String("COGNITO_USER_POOL_ID", "")
	rentalID := env.String("RENTAL_ID", "default")
	if poolID == "" {
		return errors.New("COGNITO_USER_POOL_ID is required")
	}
	input, err := domain.NewMember{Email: email, Name: name}.Normalize()
	if err != nil {
		return err
	}
	awsCfg, err := awsx.Load(ctx)
	if err != nil {
		return err
	}
	dir := directory.New(cip.NewFromConfig(awsCfg), poolID)

	_, temporary, err := dir.Create(ctx, rentalID, auth.Owner, input)
	if errors.Is(err, domain.ErrEmailTaken) {
		fmt.Printf("Owner %s already exists in rental %q\n", input.Email, rentalID)
		return nil
	}
	if err != nil {
		return err
	}
	if password == "" {
		fmt.Printf("Owner %s created in rental %q\nTemporary password: %s\n", input.Email, rentalID, temporary)
		fmt.Println("The first sign-in answers a NEW_PASSWORD_REQUIRED challenge to replace it.")
	} else {
		if err := dir.SetPermanentPassword(ctx, input.Email, password); err != nil {
			return err
		}
		fmt.Printf("Owner %s created in rental %q with the password from %s\n", input.Email, rentalID, passwordVar)
	}
	printSignIn(input.Email)
	return nil
}

func printSignIn(email string) {
	fmt.Printf(`
Sign in locally and call a service with the ID token:
  export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1
  token=$(aws --endpoint-url %s cognito-idp initiate-auth --client-id %s \
    --auth-flow USER_PASSWORD_AUTH --auth-parameters 'USERNAME=%s,PASSWORD=<password>' \
    --query AuthenticationResult.IdToken --output text)
  curl -s localhost:8082/me -H "Authorization: Bearer $token"
`, env.String(awsx.EndpointVar, "http://localhost:4566"), env.String("COGNITO_CLIENT_ID", "<web client id>"), email)
}
