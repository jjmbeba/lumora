package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type EmailVerificationRateLimiter struct {
	ips         map[string][]time.Time
	now         func() time.Time
	lastCleanup time.Time
	m           sync.Mutex
}

const MaxNumberRequestsPerHour = 20
const emailVerificationCleanupInterval = 5 * time.Minute

func NewEmailVerificationRateLimiter(now func() time.Time) (*EmailVerificationRateLimiter, error) {
	if now == nil {
		return nil, fmt.Errorf("now function must not be nil")
	}

	return &EmailVerificationRateLimiter{
		ips: make(map[string][]time.Time),
		now: now,
	}, nil
}

func (rl *EmailVerificationRateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(r) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func (rl *EmailVerificationRateLimiter) allow(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}

	rl.m.Lock()
	defer rl.m.Unlock()

	currentTimestamp := rl.now()
	cutoff := currentTimestamp.Add(-time.Hour)
	if rl.lastCleanup.IsZero() || currentTimestamp.Sub(rl.lastCleanup) >= emailVerificationCleanupInterval {
		for ip, records := range rl.ips {
			active := false

			for _, timestamp := range records {
				if timestamp.After(cutoff) {
					active = true
					break
				}
			}

			if !active {
				delete(rl.ips, ip)
			}

		}
		rl.lastCleanup = currentTimestamp
	}

	validIPRecords := []time.Time{}

	for _, timestamp := range rl.ips[host] {
		if timestamp.After(cutoff) {
			validIPRecords = append(validIPRecords, timestamp)
		}
	}

	rl.ips[host] = validIPRecords
	count := len(rl.ips[host])

	if count >= MaxNumberRequestsPerHour {
		return false
	}

	rl.ips[host] = append(rl.ips[host], currentTimestamp)

	return true
}
