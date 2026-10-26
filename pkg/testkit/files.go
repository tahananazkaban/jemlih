package testkit

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

var fileClient = &http.Client{Timeout: 30 * time.Second}

// PutFile uploads body to a presigned URL with the headers it was signed for, as a browser would, and returns the status.
func PutFile(t testing.TB, url string, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upload: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := fileClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// GetFile downloads a presigned URL and returns the status and body.
func GetFile(t testing.TB, url string) (int, []byte) {
	t.Helper()
	resp, err := fileClient.Get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	return resp.StatusCode, body
}
