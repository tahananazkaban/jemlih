// Package env reads configuration from environment variables with defaults.
package env

import "os"

// String returns the variable's value, or def when it is unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
