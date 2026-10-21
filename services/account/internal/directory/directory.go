// Package directory manages Rental accounts in the Cognito user pool.
package directory

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/services/account/internal/domain"
)

// rentalAttribute is the custom user attribute tying an account to its Rental.
const rentalAttribute = "custom:rental_id"

// Directory creates and lists accounts in one user pool.
type Directory struct {
	client *cip.Client
	poolID string
}

func New(client *cip.Client, poolID string) *Directory {
	return &Directory{client: client, poolID: poolID}
}

// Create adds a back-office account with a temporary password the user must change at first sign-in.
func (d *Directory) Create(ctx context.Context, rentalID string, role auth.Role, m domain.NewMember) (domain.Member, string, error) {
	password, err := temporaryPassword()
	if err != nil {
		return domain.Member{}, "", err
	}
	out, err := d.client.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId:        &d.poolID,
		Username:          &m.Email,
		TemporaryPassword: &password,
		MessageAction:     types.MessageActionTypeSuppress,
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: &m.Email},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
			{Name: aws.String("name"), Value: &m.Name},
			{Name: aws.String(rentalAttribute), Value: &rentalID},
		},
	})
	// Email is the username, so a taken email can surface as either error.
	var usernameTaken *types.UsernameExistsException
	var aliasTaken *types.AliasExistsException
	if errors.As(err, &usernameTaken) || errors.As(err, &aliasTaken) {
		return domain.Member{}, "", domain.ErrEmailTaken
	}
	if err != nil {
		return domain.Member{}, "", fmt.Errorf("create user: %w", err)
	}

	if err := d.AddToGroup(ctx, m.Email, role); err != nil {
		// A user without a group could sign in with no role, so undo the creation.
		if _, delErr := d.client.AdminDeleteUser(ctx, &cip.AdminDeleteUserInput{UserPoolId: &d.poolID, Username: &m.Email}); delErr != nil {
			err = errors.Join(err, fmt.Errorf("roll back user: %w", delErr))
		}
		return domain.Member{}, "", err
	}
	return toMember(out.User.Attributes, out.User.UserStatus, aws.ToTime(out.User.UserCreateDate)), password, nil
}

// AddToGroup gives the user the Cognito group of role.
func (d *Directory) AddToGroup(ctx context.Context, username string, role auth.Role) error {
	_, err := d.client.AdminAddUserToGroup(ctx, &cip.AdminAddUserToGroupInput{
		UserPoolId: &d.poolID,
		Username:   &username,
		GroupName:  aws.String(string(role)),
	})
	if err != nil {
		return fmt.Errorf("add user to group %s: %w", role, err)
	}
	return nil
}

// SetPermanentPassword skips the first sign-in password change, for seeded accounts only.
func (d *Directory) SetPermanentPassword(ctx context.Context, username, password string) error {
	_, err := d.client.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: &d.poolID,
		Username:   &username,
		Password:   &password,
		Permanent:  true,
	})
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// AssignRental overwrites the user's Rental, ignoring anything the user supplied at sign-up.
func (d *Directory) AssignRental(ctx context.Context, username, rentalID string) error {
	_, err := d.client.AdminUpdateUserAttributes(ctx, &cip.AdminUpdateUserAttributesInput{
		UserPoolId:     &d.poolID,
		Username:       &username,
		UserAttributes: []types.AttributeType{{Name: aws.String(rentalAttribute), Value: &rentalID}},
	})
	if err != nil {
		return fmt.Errorf("assign rental: %w", err)
	}
	return nil
}

// ListGroup returns the members of role's group that belong to the Rental.
func (d *Directory) ListGroup(ctx context.Context, rentalID string, role auth.Role) ([]domain.Member, error) {
	members := []domain.Member{}
	paginator := cip.NewListUsersInGroupPaginator(d.client, &cip.ListUsersInGroupInput{
		UserPoolId: &d.poolID,
		GroupName:  aws.String(string(role)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list group %s: %w", role, err)
		}
		// Cognito cannot filter on custom attributes, so the Rental is matched here.
		for _, u := range page.Users {
			if attribute(u.Attributes, rentalAttribute) == rentalID {
				members = append(members, toMember(u.Attributes, u.UserStatus, aws.ToTime(u.UserCreateDate)))
			}
		}
	}
	return members, nil
}

func toMember(attrs []types.AttributeType, status types.UserStatusType, created time.Time) domain.Member {
	return domain.Member{
		ID:        attribute(attrs, "sub"),
		Email:     attribute(attrs, "email"),
		Name:      attribute(attrs, "name"),
		Status:    string(status),
		CreatedAt: created.UTC(),
	}
}

func attribute(attrs []types.AttributeType, name string) string {
	for _, a := range attrs {
		if aws.ToString(a.Name) == name {
			return aws.ToString(a.Value)
		}
	}
	return ""
}

const (
	lower   = "abcdefghijkmnopqrstuvwxyz"
	upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	digits  = "23456789"
	symbols = "!@#$%^&*-_=+"
)

// temporaryPassword returns 16 random characters with every character class the pool requires.
func temporaryPassword() (string, error) {
	classes := []string{lower, upper, digits, symbols}
	all := lower + upper + digits + symbols
	out := make([]byte, 16)
	for i := range out {
		set := all
		if i < len(classes) {
			set = classes[i]
		}
		c, err := randomChar(set)
		if err != nil {
			return "", err
		}
		out[i] = c
	}
	// Shuffle so the guaranteed classes are not always at the front.
	for i := len(out) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	return string(out), nil
}

func randomChar(set string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, fmt.Errorf("generate password: %w", err)
	}
	return set[n.Int64()], nil
}
