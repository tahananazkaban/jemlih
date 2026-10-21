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

func main() {
	email := flag.String("email", "owner@jemlih.local", "Owner email, also the sign-in username")
	name := flag.String("name", "Local Owner", "Owner display name")
	password := flag.String("password", "", "permanent password; empty keeps the generated temporary one")
	flag.Parse()

	if err := run(*email, *name, *password); err != nil {
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
		fmt.Printf("Owner %s created in rental %q\nTemporary password (change at first sign-in): %s\n", input.Email, rentalID, temporary)
		return nil
	}
	if err := dir.SetPermanentPassword(ctx, input.Email, password); err != nil {
		return err
	}
	fmt.Printf("Owner %s created in rental %q with the given password\n", input.Email, rentalID)
	return nil
}
