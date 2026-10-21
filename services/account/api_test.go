package account_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
	"github.com/tahananazkaban/jemlih/services/account/internal/app"
)

type me struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	RentalID string `json:"rental_id"`
}

type member struct {
	ID                string `json:"id"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	TemporaryPassword string `json:"temporary_password"`
}

// serveAccount runs the real account service against the emulator, with no credentials attached.
func serveAccount(t *testing.T, pool *testkit.Pool) *testkit.Client {
	t.Helper()
	handler, err := app.New(app.Config{AWS: testkit.AWSConfig(t), Auth: pool.Auth, UserPoolID: pool.ID})
	if err != nil {
		t.Fatalf("start account: %v", err)
	}
	return testkit.Serve(t, handler)
}

func TestMeDescribesTheCaller(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	rentalID := testkit.NewRentalID()

	for _, role := range []auth.Role{auth.Owner, auth.Staff, auth.Renter} {
		t.Run(string(role), func(t *testing.T) {
			user := pool.NewUser(rentalID, role)
			var got me
			account.As(user.IDToken).Do(http.MethodGet, "/me", nil).Expect(http.StatusOK).Decode(&got)
			if got.ID == "" || got.Email != user.Email || got.Role != string(role) || got.RentalID != rentalID {
				t.Errorf("me = %+v, want %s %s in %s", got, user.Email, role, rentalID)
			}
		})
	}
}

func TestTokensFromOtherClientsOrTamperedAreRejected(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	user := pool.NewUser(testkit.NewRentalID(), auth.Owner)

	other, err := pool.Client.CreateUserPoolClient(context.Background(), &cip.CreateUserPoolClientInput{
		UserPoolId:        &pool.ID,
		ClientName:        aws.String("other-app"),
		ExplicitAuthFlows: []types.ExplicitAuthFlowsType{types.ExplicitAuthFlowsTypeAllowUserPasswordAuth},
	})
	if err != nil {
		t.Fatalf("create other client: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Client.DeleteUserPoolClient(context.Background(), &cip.DeleteUserPoolClientInput{UserPoolId: &pool.ID, ClientId: other.UserPoolClient.ClientId})
	})
	otherPool := *pool
	otherPool.ClientID = aws.ToString(other.UserPoolClient.ClientId)

	parts := strings.Split(user.IDToken, ".")
	tampered := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))

	for name, token := range map[string]string{
		"missing":           "",
		"other user pool":   foreignPoolToken(t, pool),
		"other app client":  otherPool.SignIn(user.Email, testkit.TestPassword),
		"tampered":          tampered,
		"access token":      accessToken(t, pool, user.Email),
		"bearer of garbage": "garbage",
	} {
		t.Run(name, func(t *testing.T) {
			resp := account.As(token).Do(http.MethodGet, "/me", nil).Expect(http.StatusUnauthorized)
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}
}

func TestOwnerCreatesStaffWhoCanSignIn(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	rentalID := testkit.NewRentalID()
	owner := account.As(pool.NewUser(rentalID, auth.Owner).IDToken)

	email := strings.ToUpper(testkit.NewRentalID()) + "@Example.com"
	var created member
	owner.Do(http.MethodPost, "/staff", map[string]string{"email": "  " + email + " ", "name": " Budi "}).
		Expect(http.StatusCreated).Decode(&created)
	pool.DeleteAfterTest(strings.ToLower(email))

	if created.ID == "" || created.Email != strings.ToLower(email) || created.Name != "Budi" || created.Status != "FORCE_CHANGE_PASSWORD" {
		t.Errorf("created = %+v", created)
	}
	if len(created.TemporaryPassword) < 12 {
		t.Errorf("temporary password %q is too short", created.TemporaryPassword)
	}

	token := completeFirstSignIn(t, pool, created.Email, created.TemporaryPassword, "Staff-New-Passw0rd!")
	var got me
	account.As(token).Do(http.MethodGet, "/me", nil).Expect(http.StatusOK).Decode(&got)
	if got.Role != "staff" || got.RentalID != rentalID {
		t.Errorf("staff me = %+v, want staff in %s", got, rentalID)
	}

	var list struct {
		Staff []member `json:"staff"`
	}
	owner.Do(http.MethodGet, "/staff", nil).Expect(http.StatusOK).Decode(&list)
	if len(list.Staff) != 1 || list.Staff[0].ID != created.ID || list.Staff[0].TemporaryPassword != "" {
		t.Errorf("staff list = %+v, want only %s without password", list.Staff, created.ID)
	}
}

func TestStaffListIsScopedToTheRental(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	ownerA := account.As(pool.NewUser(testkit.NewRentalID(), auth.Owner).IDToken)
	ownerB := account.As(pool.NewUser(testkit.NewRentalID(), auth.Owner).IDToken)

	email := testkit.NewRentalID() + "@example.com"
	ownerA.Do(http.MethodPost, "/staff", map[string]string{"email": email, "name": "Sari"}).Expect(http.StatusCreated)
	pool.DeleteAfterTest(email)

	resp := ownerB.Do(http.MethodGet, "/staff", nil).Expect(http.StatusOK)
	if got := string(resp.Body); got != "{\"staff\":[]}\n" {
		t.Errorf("other rental sees %s", got)
	}
}

func TestOnlyOwnersManageStaff(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	rentalID := testkit.NewRentalID()

	for _, role := range []auth.Role{auth.Staff, auth.Renter} {
		t.Run(string(role), func(t *testing.T) {
			caller := account.As(pool.NewUser(rentalID, role).IDToken)
			caller.Do(http.MethodGet, "/staff", nil).Expect(http.StatusForbidden)
			caller.Do(http.MethodPost, "/staff", map[string]string{"email": "x@example.com", "name": "X"}).Expect(http.StatusForbidden)
		})
	}
}

func TestCreateStaffRejectsInvalidInput(t *testing.T) {
	pool := testkit.UserPool(t)
	owner := serveAccount(t, pool).As(pool.NewUser(testkit.NewRentalID(), auth.Owner).IDToken)

	cases := map[string]struct {
		body  map[string]any
		field string
	}{
		"missing email": {map[string]any{"name": "Budi"}, "email"},
		"missing name":  {map[string]any{"email": "a@example.com"}, "name"},
		"bad email":     {map[string]any{"email": "not-an-email", "name": "Budi"}, "email"},
		"display email": {map[string]any{"email": "Budi <budi@example.com>", "name": "Budi"}, "email"},
		"blank name":    {map[string]any{"email": "a@example.com", "name": "   "}, "name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var e testkit.ErrorBody
			owner.Do(http.MethodPost, "/staff", tc.body).Expect(http.StatusBadRequest).Decode(&e)
			if _, ok := e.Error.Fields[tc.field]; !ok || len(e.Error.Fields) != 1 {
				t.Errorf("fields = %v, want only %q", e.Error.Fields, tc.field)
			}
		})
	}
}

func TestCreateStaffRejectsTakenEmail(t *testing.T) {
	pool := testkit.UserPool(t)
	rentalID := testkit.NewRentalID()
	existing := pool.NewUser(rentalID, auth.Renter)
	owner := serveAccount(t, pool).As(pool.NewUser(rentalID, auth.Owner).IDToken)

	var e testkit.ErrorBody
	owner.Do(http.MethodPost, "/staff", map[string]string{"email": existing.Email, "name": "Dup"}).
		Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "email_taken" {
		t.Errorf("code = %q, want email_taken", e.Error.Code)
	}
}

func TestSelfRegisteredUserBecomesRenterOfTheConfiguredRental(t *testing.T) {
	pool := testkit.UserPool(t)
	account := serveAccount(t, pool)
	ctx := context.Background()
	email := testkit.NewRentalID() + "@example.com"

	// The sign-up tries to claim another Rental; the post-confirmation trigger must override it.
	_, err := pool.Client.SignUp(ctx, &cip.SignUpInput{
		ClientId: &pool.ClientID,
		Username: &email,
		Password: aws.String(testkit.TestPassword),
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: &email},
			{Name: aws.String("custom:rental_id"), Value: aws.String("someone-elses-rental")},
		},
	})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	pool.DeleteAfterTest(email)
	if _, err := pool.Client.AdminConfirmSignUp(ctx, &cip.AdminConfirmSignUpInput{UserPoolId: &pool.ID, Username: &email}); err != nil {
		t.Fatalf("confirm sign up: %v", err)
	}

	var got me
	account.As(pool.SignIn(email, testkit.TestPassword)).Do(http.MethodGet, "/me", nil).Expect(http.StatusOK).Decode(&got)
	if want := env.String("RENTAL_ID", "default"); got.Role != "renter" || got.RentalID != want {
		t.Errorf("me = %+v, want renter in %q", got, want)
	}
}

// foreignPoolToken signs an Owner into a throwaway user pool, so the token has a different issuer.
func foreignPoolToken(t *testing.T, pool *testkit.Pool) string {
	t.Helper()
	ctx := context.Background()
	created, err := pool.Client.CreateUserPool(ctx, &cip.CreateUserPoolInput{
		PoolName: aws.String("foreign-" + testkit.NewRentalID()),
		Schema: []types.SchemaAttributeType{{
			Name: aws.String("rental_id"), AttributeDataType: types.AttributeDataTypeString, Mutable: aws.Bool(true),
		}},
	})
	if err != nil {
		t.Fatalf("create foreign pool: %v", err)
	}
	foreign := *pool
	foreign.ID = aws.ToString(created.UserPool.Id)
	t.Cleanup(func() {
		_, _ = pool.Client.DeleteUserPool(context.Background(), &cip.DeleteUserPoolInput{UserPoolId: &foreign.ID})
	})
	client, err := pool.Client.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId:        &foreign.ID,
		ClientName:        aws.String("web"),
		ExplicitAuthFlows: []types.ExplicitAuthFlowsType{types.ExplicitAuthFlowsTypeAllowUserPasswordAuth},
	})
	if err != nil {
		t.Fatalf("create foreign client: %v", err)
	}
	foreign.ClientID = aws.ToString(client.UserPoolClient.ClientId)
	if _, err := pool.Client.CreateGroup(ctx, &cip.CreateGroupInput{UserPoolId: &foreign.ID, GroupName: aws.String("owner")}); err != nil {
		t.Fatalf("create foreign group: %v", err)
	}
	return foreign.NewUser(testkit.NewRentalID(), auth.Owner).IDToken
}

func accessToken(t *testing.T, pool *testkit.Pool, email string) string {
	t.Helper()
	out, err := pool.Client.InitiateAuth(context.Background(), &cip.InitiateAuthInput{
		ClientId:       &pool.ClientID,
		AuthFlow:       types.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": email, "PASSWORD": testkit.TestPassword},
	})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return aws.ToString(out.AuthenticationResult.AccessToken)
}

// completeFirstSignIn answers the new-password challenge of an account created with a temporary password.
func completeFirstSignIn(t *testing.T, pool *testkit.Pool, email, temporary, permanent string) string {
	t.Helper()
	ctx := context.Background()
	out, err := pool.Client.InitiateAuth(ctx, &cip.InitiateAuthInput{
		ClientId:       &pool.ClientID,
		AuthFlow:       types.AuthFlowTypeUserPasswordAuth,
		AuthParameters: map[string]string{"USERNAME": email, "PASSWORD": temporary},
	})
	if err != nil {
		t.Fatalf("first sign in: %v", err)
	}
	if out.ChallengeName != types.ChallengeNameTypeNewPasswordRequired {
		t.Fatalf("challenge = %q, want NEW_PASSWORD_REQUIRED", out.ChallengeName)
	}
	answer, err := pool.Client.RespondToAuthChallenge(ctx, &cip.RespondToAuthChallengeInput{
		ClientId:           &pool.ClientID,
		ChallengeName:      types.ChallengeNameTypeNewPasswordRequired,
		Session:            out.Session,
		ChallengeResponses: map[string]string{"USERNAME": email, "NEW_PASSWORD": permanent},
	})
	if err != nil {
		t.Fatalf("set new password: %v", err)
	}
	return aws.ToString(answer.AuthenticationResult.IdToken)
}
