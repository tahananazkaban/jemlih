// Package auth verifies Cognito ID tokens and enforces roles on HTTP handlers.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/httpx"
)

// Role is a user's job in a Rental, taken from their Cognito group.
type Role string

const (
	Owner  Role = "owner"
	Staff  Role = "staff"
	Renter Role = "renter"
)

// Identity is the verified caller of a request.
type Identity struct {
	Subject  string
	Email    string
	RentalID string
	Role     Role
}

// Config locates the user pool that issues tokens for this deployment.
type Config struct {
	Issuer   string
	JWKSURL  string
	ClientID string
}

// ConfigFromEnv reads COGNITO_ISSUER, COGNITO_CLIENT_ID and the optional COGNITO_JWKS_URL.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Issuer:   strings.TrimRight(env.String("COGNITO_ISSUER", ""), "/"),
		ClientID: env.String("COGNITO_CLIENT_ID", ""),
	}
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return Config{}, errors.New("COGNITO_ISSUER and COGNITO_CLIENT_ID are required")
	}
	cfg.JWKSURL = env.String("COGNITO_JWKS_URL", cfg.Issuer+"/.well-known/jwks.json")
	return cfg, nil
}

var (
	errUnauthenticated = errors.New("unauthenticated")
	errNoRole          = errors.New("user has no role in a rental")
)

// rolePrecedence picks the strongest role when a user is in several groups.
var rolePrecedence = []Role{Owner, Staff, Renter}

// Verifier checks tokens against one user pool and app client.
type Verifier struct {
	cfg  Config
	keys *keySet
}

func NewVerifier(cfg Config, client *http.Client) *Verifier {
	return &Verifier{cfg: cfg, keys: newKeySet(cfg.JWKSURL, client)}
}

type idClaims struct {
	jwt.RegisteredClaims
	TokenUse string   `json:"token_use"`
	Email    string   `json:"email"`
	RentalID string   `json:"custom:rental_id"`
	Groups   []string `json:"cognito:groups"`
}

// Verify returns the Identity in a valid ID token issued to this app client.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	var claims idClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			return v.keys.get(ctx, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.ClientID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", errUnauthenticated, err)
	}
	if claims.TokenUse != "id" {
		return Identity{}, fmt.Errorf("%w: token_use is %q, want id", errUnauthenticated, claims.TokenUse)
	}
	id := Identity{Subject: claims.Subject, Email: claims.Email, RentalID: claims.RentalID}
	for _, role := range rolePrecedence {
		if slices.Contains(claims.Groups, string(role)) {
			id.Role = role
			break
		}
	}
	if id.Role == "" || id.RentalID == "" {
		return Identity{}, errNoRole
	}
	return id, nil
}

type contextKey struct{}

// FromContext returns the Identity stored by Require.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// MustFromContext is for handlers wrapped by Require, where the Identity always exists.
func MustFromContext(ctx context.Context) Identity {
	id, ok := FromContext(ctx)
	if !ok {
		panic("auth: handler is not wrapped by Require")
	}
	return id
}

// Require lets the request through only for a verified caller holding one of roles.
func (v *Verifier) Require(roles ...Role) func(http.HandlerFunc) http.Handler {
	return func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				unauthorized(w)
				return
			}
			id, err := v.Verify(r.Context(), token)
			switch {
			case errors.Is(err, errNoRole):
				httpx.WriteError(w, http.StatusForbidden, httpx.Error{Code: "forbidden", Message: "account has no role in a rental"})
				return
			case errors.Is(err, errKeySetUnavailable):
				httpx.InternalError(w, r, err)
				return
			case err != nil:
				unauthorized(w)
				return
			}
			if !slices.Contains(roles, id.Role) {
				httpx.WriteError(w, http.StatusForbidden, httpx.Error{Code: "forbidden", Message: "this action is not allowed for your role"})
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="jemlih"`)
	httpx.WriteError(w, http.StatusUnauthorized, httpx.Error{Code: "unauthenticated", Message: "a valid ID token is required"})
}
