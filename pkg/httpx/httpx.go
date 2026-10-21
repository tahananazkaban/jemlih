// Package httpx holds the JSON request and error conventions shared by all HTTP services.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const maxBodyBytes = 1 << 20

// Error is the body of every non-2xx response.
type Error struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type errorBody struct {
	Error Error `json:"error"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

// WriteError writes an error envelope with the given status.
func WriteError(w http.ResponseWriter, status int, e Error) {
	WriteJSON(w, status, errorBody{Error: e})
}

// InternalError logs err and hides its details from the client.
func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	WriteError(w, http.StatusInternalServerError, Error{Code: "internal", Message: "internal server error"})
}

// DecodeJSON reads a single JSON object into dst; its errors are safe to show to clients.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return describeDecodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

// describeDecodeError turns decoder errors into messages without Go type names.
func describeDecodeError(err error) error {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return errors.New("body is empty")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("body is not valid JSON")
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Errorf("field %q must be a %s", typeErr.Field, jsonKind(typeErr.Type.Kind().String()))
	case errors.As(err, &typeErr):
		return errors.New("body must be a JSON object")
	case errors.As(err, &tooLarge):
		return fmt.Errorf("body must not exceed %d bytes", tooLarge.Limit)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return fmt.Errorf("unknown field %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
	default:
		return errors.New("body is not a valid JSON object")
	}
}

func jsonKind(goKind string) string {
	switch {
	case strings.HasPrefix(goKind, "int"), strings.HasPrefix(goKind, "uint"), strings.HasPrefix(goKind, "float"):
		return "number"
	case goKind == "string":
		return "string"
	case goKind == "bool":
		return "boolean"
	case goKind == "slice", goKind == "array":
		return "array"
	default:
		return "object"
	}
}
