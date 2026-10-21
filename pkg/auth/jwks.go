package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	// minRefreshInterval stops unknown key IDs from hammering the JWKS endpoint, even while it fails.
	minRefreshInterval = 30 * time.Second
	maxJWKSBytes       = 1 << 20
)

// keySet caches the pool's signing keys and refetches when an unknown key ID appears.
type keySet struct {
	url    string
	client *http.Client

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey

	// refresh serialises fetches without blocking lookups of cached keys.
	refresh     sync.Mutex
	lastAttempt time.Time
}

func newKeySet(url string, client *http.Client) *keySet {
	return &keySet{url: url, client: client, keys: map[string]*rsa.PublicKey{}}
}

func (s *keySet) get(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := s.cached(kid); ok {
		return key, nil
	}

	s.refresh.Lock()
	defer s.refresh.Unlock()
	// Another request may have refreshed while this one waited.
	if key, ok := s.cached(kid); ok {
		return key, nil
	}
	if time.Since(s.lastAttempt) < minRefreshInterval {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	s.lastAttempt = time.Now()

	keys, err := s.fetch(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "refresh signing keys", "url", s.url, "err", err)
		return nil, fmt.Errorf("signing key %q unavailable: %w", kid, err)
	}
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()

	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (s *keySet) cached(kid string) (*rsa.PublicKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.keys[kid]
	return key, ok
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (s *keySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", s.url, resp.Status)
	}
	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range body.Keys {
		// Only RSA signing keys meant for RS256 can verify our tokens.
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		key, err := rsaKey(k)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = key
	}
	return keys, nil
}

func rsaKey(k jwk) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	exponent := new(big.Int).SetBytes(e)
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || exponent.Bit(0) == 0 {
		return nil, fmt.Errorf("exponent %s is not a valid RSA public exponent", exponent)
	}
	modulus := new(big.Int).SetBytes(n)
	if modulus.BitLen() < 2048 {
		return nil, fmt.Errorf("modulus of %d bits is too short", modulus.BitLen())
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, nil
}
