package config

import (
	"fmt"
	"os"
	"time"
)

func RequireEnv(key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return "", fmt.Errorf("required env variable %q is not set", key)
	}

	return value, nil
}

// ResendFromAddress reads the shared sender setting. RESEND_FROM_EMAIL remains
// accepted for compatibility with older worker deployments.
func ResendFromAddress() (string, error) {
	if value, ok := os.LookupEnv("RESEND_EMAIL_FROM"); ok && value != "" {
		return value, nil
	}
	return RequireEnv("RESEND_FROM_EMAIL")
}

func Duration(key string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}

	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}

	return duration, nil
}
