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
