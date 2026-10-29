package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmailVerificationRateLimiterLimitAndExpiry(t *testing.T) {
	now := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	limiter, err := NewEmailVerificationRateLimiter(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var downstreamCalls int
	handler := limiter.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downstreamCalls++
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < MaxNumberRequestsPerHour; i++ {
		if status := serveRateLimitedRequest(handler, "192.0.2.1:1234"); status != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want 204", i+1, status)
		}
	}
	if status := serveRateLimitedRequest(handler, "192.0.2.1:9876"); status != http.StatusTooManyRequests {
		t.Errorf("21st request status = %d, want 429", status)
	}
	if status := serveRateLimitedRequest(handler, "192.0.2.2:1234"); status != http.StatusNoContent {
		t.Errorf("different IP status = %d, want 204", status)
	}

	now = now.Add(time.Hour - time.Nanosecond)
	if status := serveRateLimitedRequest(handler, "192.0.2.1:1234"); status != http.StatusTooManyRequests {
		t.Errorf("just before expiry status = %d, want 429", status)
	}
	now = now.Add(time.Nanosecond)
	if status := serveRateLimitedRequest(handler, "192.0.2.1:1234"); status != http.StatusNoContent {
		t.Errorf("at expiry status = %d, want 204", status)
	}
	if downstreamCalls != MaxNumberRequestsPerHour+2 {
		t.Errorf("downstream calls = %d, want %d", downstreamCalls, MaxNumberRequestsPerHour+2)
	}
}

func TestEmailVerificationRateLimiterCleansInactiveIPs(t *testing.T) {
	now := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	limiter, err := NewEmailVerificationRateLimiter(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	handler := limiter.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	serveRateLimitedRequest(handler, "192.0.2.1:1234")
	now = now.Add(time.Hour)
	serveRateLimitedRequest(handler, "192.0.2.2:1234")

	if _, exists := limiter.ips["192.0.2.1"]; exists {
		t.Error("inactive IP was not removed during cleanup")
	}
	if _, exists := limiter.ips["192.0.2.2"]; !exists {
		t.Error("current IP is missing from limiter")
	}
}

func TestEmailVerificationRateLimiterConcurrentRequests(t *testing.T) {
	now := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	limiter, err := NewEmailVerificationRateLimiter(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var passed atomic.Int32
	handler := limiter.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		passed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveRateLimitedRequest(handler, "192.0.2.1:1234")
		}()
	}
	wg.Wait()
	if got := passed.Load(); got != MaxNumberRequestsPerHour {
		t.Errorf("concurrent allowed requests = %d, want %d", got, MaxNumberRequestsPerHour)
	}
}

func TestEmailVerificationRoutesShareLimit(t *testing.T) {
	limiter, err := NewEmailVerificationRateLimiter(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler := newEmailVerificationTestHandler(t, &emailVerificationHTTPStoreFake{}, &emailVerificationHTTPSenderFake{})
	mux := http.NewServeMux()
	if err := RegisterEmailVerificationRoutes(mux, handler, limiter); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxNumberRequestsPerHour; i++ {
		request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verification-challenges", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("challenge %d status = %d, want 202", i+1, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verifications", strings.NewReader(`{"code":"123456"}`))
	request.RemoteAddr = "192.0.2.1:5678"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Errorf("verification status = %d, want 429 after challenge requests", response.Code)
	}
}

func TestEmailVerificationRateLimiterRejectsInvalidInputs(t *testing.T) {
	if _, err := NewEmailVerificationRateLimiter(nil); err == nil {
		t.Error("nil clock should be rejected")
	}
	limiter, err := NewEmailVerificationRateLimiter(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler := limiter.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if status := serveRateLimitedRequest(handler, "invalid"); status != http.StatusTooManyRequests {
		t.Errorf("invalid remote address status = %d, want 429", status)
	}
}

func serveRateLimitedRequest(handler http.Handler, remoteAddr string) int {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = remoteAddr
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}
