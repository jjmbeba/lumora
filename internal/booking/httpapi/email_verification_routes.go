package httpapi

import (
	"errors"
	"net/http"
)

// RegisterEmailVerificationRoutes uses one limiter for both endpoints.
func RegisterEmailVerificationRoutes(mux *http.ServeMux, handler *EmailVerificationHandler, limiter *EmailVerificationRateLimiter) error {
	if mux == nil || handler == nil || limiter == nil {
		return errors.New("email verification routes require a mux, handler, and rate limiter")
	}

	mux.Handle("POST /bookings/{reference}/email-verification-challenges", limiter.Limit(http.HandlerFunc(handler.RequestCode)))
	mux.Handle("POST /bookings/{reference}/email-verifications", limiter.Limit(http.HandlerFunc(handler.VerifyCode)))
	return nil
}
