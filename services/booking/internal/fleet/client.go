// Package fleet reads Units through the fleet service's HTTP API, never its table.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var ErrUnitNotFound = errors.New("unit not found")

// Unit is the part of a fleet Unit that booking needs.
type Unit struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	PlateNumber     string `json:"plate_number"`
	Transmission    string `json:"transmission"`
	Status          string `json:"status"`
	DailyPrice      int64  `json:"daily_price"`
	HourlyPrice     int64  `json:"hourly_price"`
	SecurityDeposit int64  `json:"security_deposit"`
}

// Bookable reports whether fleet lets the Unit be rented at all.
func (u Unit) Bookable() bool { return u.Status == "available" }

// Client calls fleet on behalf of the caller, forwarding their ID token so fleet scopes the Rental.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Get returns one Unit, or ErrUnitNotFound.
func (c *Client) Get(ctx context.Context, token, unitID string) (Unit, error) {
	var u Unit
	err := c.get(ctx, token, "/units/"+url.PathEscape(unitID), &u)
	return u, err
}

// List returns every Unit of the caller's Rental.
func (c *Client) List(ctx context.Context, token string) ([]Unit, error) {
	var body struct {
		Units []Unit `json:"units"`
	}
	err := c.get(ctx, token, "/units", &body)
	if errors.Is(err, ErrUnitNotFound) {
		// The collection always exists, so a 404 means fleet is misrouted.
		return nil, fmt.Errorf("fleet GET /units: unexpected 404")
	}
	return body.Units, err
}

func (c *Client) get(ctx context.Context, token, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fleet GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrUnitNotFound
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("fleet GET %s: %s: %s", path, resp.Status, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("fleet GET %s: decode: %w", path, err)
	}
	return nil
}
