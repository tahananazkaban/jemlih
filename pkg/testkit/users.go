package testkit

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/tahananazkaban/jemlih/pkg/auth"
)

// TestPassword satisfies the user pool's password policy.
const TestPassword = "Test-Passw0rd!"

// Pool is the local user pool and web app client created by `make up`.
type Pool struct {
	t        testing.TB
	Client   *cip.Client
	ID       string
	ClientID string
	Auth     auth.Config
}

// UserPool connects to the user pool named by the variables `make test` exports.
func UserPool(t testing.TB) *Pool {
	t.Helper()
	cfg := AWSConfig(t)
	authCfg, err := auth.ConfigFromEnv()
	poolID := os.Getenv("COGNITO_USER_POOL_ID")
	if err != nil || poolID == "" {
		t.Fatalf("user pool settings missing, run tests with `make test`: %v", err)
	}
	return &Pool{t: t, Client: cip.NewFromConfig(cfg), ID: poolID, ClientID: authCfg.ClientID, Auth: authCfg}
}

// User is a signed-in test account.
type User struct {
	Email   string
	IDToken string
}

// NewUser creates a confirmed account with role in rentalID, signs it in and deletes it after the test.
func (p *Pool) NewUser(rentalID string, role auth.Role) User {
	p.t.Helper()
	ctx := context.Background()
	email := NewRentalID() + "@example.com"
	_, err := p.Client.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId:    &p.ID,
		Username:      &email,
		MessageAction: types.MessageActionTypeSuppress,
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: &email},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
			{Name: aws.String("custom:rental_id"), Value: &rentalID},
		},
	})
	if err != nil {
		p.t.Fatalf("create user: %v", err)
	}
	p.DeleteAfterTest(email)
	if _, err := p.Client.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: &p.ID, Username: &email, Password: aws.String(TestPassword), Permanent: true,
	}); err != nil {
		p.t.Fatalf("set password: %v", err)
	}
	if role != "" {
		if _, err := p.Client.AdminAddUserToGroup(ctx, &cip.AdminAddUserToGroupInput{
			UserPoolId: &p.ID, Username: &email, GroupName: aws.String(string(role)),
		}); err != nil {
			p.t.Fatalf("add to group: %v", err)
		}
	}
	return User{Email: email, IDToken: p.SignIn(email, TestPassword)}
}

// SignIn returns the ID token for a confirmed user with a permanent password.
func (p *Pool) SignIn(email, password string) string {
	p.t.Helper()
	out, err := p.Client.InitiateAuth(context.Background(), &cip.InitiateAuthInput{
		ClientId:       &p.ClientID,
		AuthFlow:       types.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": email, "PASSWORD": password},
	})
	if err != nil {
		p.t.Fatalf("sign in %s: %v", email, err)
	}
	if out.AuthenticationResult == nil {
		p.t.Fatalf("sign in %s: unexpected challenge %s", email, out.ChallengeName)
	}
	return aws.ToString(out.AuthenticationResult.IdToken)
}

// DeleteAfterTest removes the account when the test ends.
func (p *Pool) DeleteAfterTest(email string) {
	p.t.Cleanup(func() {
		_, _ = p.Client.AdminDeleteUser(context.Background(), &cip.AdminDeleteUserInput{UserPoolId: &p.ID, Username: &email})
	})
}
