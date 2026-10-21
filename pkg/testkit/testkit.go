// Package testkit drives services through their HTTP API against the local emulator.
package testkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/tahananazkaban/jemlih/pkg/awsx"
	"github.com/tahananazkaban/jemlih/pkg/httpx"
)

const defaultEndpoint = "http://localhost:4566"

// AWSConfig targets the running emulator and fails fast with a hint when it is down.
func AWSConfig(t testing.TB) aws.Config {
	t.Helper()
	for key, def := range map[string]string{
		awsx.EndpointVar:        defaultEndpoint,
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION":            "us-east-1",
	} {
		if os.Getenv(key) == "" {
			t.Setenv(key, def)
		}
	}
	endpoint, err := url.Parse(os.Getenv(awsx.EndpointVar))
	if err != nil {
		t.Fatalf("parse %s: %v", awsx.EndpointVar, err)
	}
	conn, err := net.DialTimeout("tcp", endpoint.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("emulator not reachable at %s, run `make up` first: %v", endpoint, err)
	}
	conn.Close()

	cfg, err := awsx.Load(context.Background())
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return cfg
}

// NewRentalID returns a fresh Rental so each test sees only its own data.
func NewRentalID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "test-" + hex.EncodeToString(b)
}

// Client sends JSON requests to a service started in-process.
type Client struct {
	t     testing.TB
	srv   *httptest.Server
	token string
}

// As returns a client that sends token as its Bearer credentials.
func (c *Client) As(token string) *Client {
	clone := *c
	clone.token = token
	return &clone
}

// Serve starts handler on a local port for the duration of the test.
func Serve(t testing.TB, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{t: t, srv: srv}
}

// Response is a fully read HTTP response.
type Response struct {
	t      testing.TB
	Status int
	Header http.Header
	Body   []byte
}

// Do sends body as JSON (a string is sent verbatim) and reads the whole response.
func (c *Client) Do(method, path string, body any) *Response {
	c.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = bytes.NewBufferString(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.srv.URL+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return &Response{t: c.t, Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// Expect fails the test unless the status matches, showing the body for context.
func (r *Response) Expect(status int) *Response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("status = %d, want %d; body: %s", r.Status, status, r.Body)
	}
	return r
}

// Decode unmarshals the body into v.
func (r *Response) Decode(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// ErrorBody is the shared error envelope.
type ErrorBody struct {
	Error httpx.Error `json:"error"`
}
