package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/staff"
)

const (
	staffSessionCookie = "staff_session"
	maxAuthBodyBytes   = 8 << 10
)

type AuthHandler struct {
	service        *staff.AuthService
	secureCookies  bool
	expectedOrigin string
}

type startLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type completeLoginRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

type startInvitationEnrollmentRequest struct {
	InvitationToken string `json:"invitation_token"`
}

type completeInvitationEnrollmentRequest struct {
	InvitationToken string `json:"invitation_token"`
	Challenge       string `json:"challenge"`
	Password        string `json:"password"`
	Code            string `json:"code"`
}

type challengeResponse struct {
	Challenge string    `json:"challenge"`
	Purpose   string    `json:"purpose"`
	ExpiresAt time.Time `json:"expires_at"`
}

type loginResponse struct {
	Account   identityResponse `json:"account"`
	ExpiresAt time.Time        `json:"expires_at"`
}

type sessionResponse struct {
	Account identityResponse `json:"account"`
}

type invitationEnrollmentChallengeResponse struct {
	Challenge  string    `json:"challenge"`
	TOTPSecret string    `json:"totp_secret"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type invitationEnrollmentResponse struct {
	Account       identityResponse `json:"account"`
	RecoveryCodes []string         `json:"recovery_codes"`
}

type identityResponse struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func NewAuthHandler(service *staff.AuthService, secureCookies bool, expectedOrigin string) (*AuthHandler, error) {
	if service == nil {
		return nil, errors.New("staff auth service is required")
	}
	origin, err := canonicalOrigin(expectedOrigin)
	if err != nil {
		return nil, errors.New("staff frontend origin must be an HTTPS origin (or loopback HTTP for development)")
	}

	return &AuthHandler{service: service, secureCookies: secureCookies, expectedOrigin: origin}, nil
}

func (h *AuthHandler) StartLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request startLoginRequest
	if !decodeAuthRequest(w, r, &request) {
		return
	}

	challenge, err := h.service.StartLogin(r.Context(), request.Email, request.Password)
	if errors.Is(err, staff.ErrInvalidCredentials) {
		http.Error(w, "email or password is invalid", http.StatusUnauthorized)
		return
	}

	if err != nil {
		http.Error(w, "unable to sign in", http.StatusInternalServerError)
		return
	}

	writeAuthJSON(w, http.StatusOK, challengeResponse{
		Challenge: challenge.Token, Purpose: string(challenge.Purpose), ExpiresAt: challenge.ExpiresAt,
	})
}

func (h *AuthHandler) CompleteLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request completeLoginRequest
	if !decodeAuthRequest(w, r, &request) {
		return
	}

	identity, session, err := h.service.CompleteLogin(r.Context(), request.Challenge, request.Code)
	if errors.Is(err, staff.ErrInvalidChallenge) || errors.Is(err, staff.ErrInvalidMFA) {
		http.Error(w, "MFA challenge or code is invalid or expired", http.StatusUnauthorized)
		return
	}

	if err != nil {
		http.Error(w, "unable to complete sign in", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: staffSessionCookie, Value: session.Token, Path: "/", Expires: session.ExpiresAt,
		MaxAge: int(time.Until(session.ExpiresAt).Seconds()), HttpOnly: true, Secure: h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})

	w.Header().Set("Cache-Control", "no-store")
	writeAuthJSON(w, http.StatusOK, loginResponse{Account: identityResponse{
		ID: identity.AccountID, Email: identity.Email, DisplayName: identity.DisplayName, Role: string(identity.Role),
	}, ExpiresAt: session.ExpiresAt})
}

func (h *AuthHandler) StartInvitationEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request startInvitationEnrollmentRequest
	if !decodeAuthRequest(w, r, &request) {
		return
	}

	challenge, err := h.service.StartInvitationEnrollment(r.Context(), request.InvitationToken)
	if errors.Is(err, staff.ErrInvalidStaffInvitation) {
		http.Error(w, "invitation is invalid or expired", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "unable to start staff account setup", http.StatusInternalServerError)
		return
	}

	writeAuthJSON(w, http.StatusOK, invitationEnrollmentChallengeResponse{
		Challenge: challenge.Token, TOTPSecret: challenge.TOTPSecret, ExpiresAt: challenge.ExpiresAt,
	})
}

func (h *AuthHandler) CompleteInvitationEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request completeInvitationEnrollmentRequest
	if !decodeAuthRequest(w, r, &request) {
		return
	}

	result, err := h.service.CompleteInvitationEnrollment(r.Context(), request.InvitationToken, request.Challenge, request.Password, request.Code)

	switch {
	case err == nil:
		writeAuthJSON(w, http.StatusOK, invitationEnrollmentResponse{
			Account: identityResponse{
				ID: result.Account.ID, Email: result.Account.Email,
				DisplayName: result.Account.DisplayName, Role: string(result.Account.Role),
			},
			RecoveryCodes: result.RecoveryCodes,
		})
	case errors.Is(err, staff.ErrInvalidPassword):
		http.Error(w, "password must be at least 8 characters and at most 1024 bytes", http.StatusBadRequest)
	case errors.Is(err, staff.ErrInvalidMFA):
		http.Error(w, "authenticator code is invalid", http.StatusUnauthorized)
	case errors.Is(err, staff.ErrInvalidStaffEnrollment):
		http.Error(w, "invitation setup is invalid or expired", http.StatusBadRequest)
	default:
		http.Error(w, "unable to complete staff account setup", http.StatusInternalServerError)
	}
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie(staffSessionCookie)
	if err == nil && cookie.Value != "" {
		if err := h.service.RevokeSession(r.Context(), cookie.Value); err != nil && !errors.Is(err, staff.ErrInvalidSession) {
			http.Error(w, "unable to sign out", http.StatusInternalServerError)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name: staffSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	identity, ok := IdentityFromContext(r)
	if !ok {
		http.Error(w, "staff authentication required", http.StatusUnauthorized)
		return
	}

	writeAuthJSON(w, http.StatusOK, sessionResponse{Account: identityResponse{
		ID: identity.AccountID, Email: identity.Email, DisplayName: identity.DisplayName, Role: string(identity.Role),
	}})
}

// RequireSession authenticates the opaque HttpOnly cookie on every protected
// request; the returned identity is stored in the request context.
func (h *AuthHandler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(staffSessionCookie)
		if err != nil || cookie.Value == "" {
			http.Error(w, "staff authentication required", http.StatusUnauthorized)
			return
		}

		identity, _, err := h.service.AuthenticateSession(r.Context(), cookie.Value)
		if errors.Is(err, staff.ErrInvalidSession) {
			http.Error(w, "staff authentication required", http.StatusUnauthorized)
			return
		}

		if err != nil {
			http.Error(w, "unable to authenticate staff session", http.StatusInternalServerError)
			return
		}

		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
	})
}

// RequireRole authenticates the session and allows the request only when the
// current account has the required role.
func (h *AuthHandler) RequireRole(required staff.Role, next http.Handler) http.Handler {
	return h.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFromContext(r)
		if !ok {
			http.Error(w, "staff authentication required", http.StatusUnauthorized)
			return
		}

		if identity.Role != required {
			http.Error(w, "staff role does not permit this action", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	}))
}

// RequireCSRF protects cookie-authenticated mutations with both an exact
// frontend Origin check and a header that cross-origin HTML forms cannot set.
func (h *AuthHandler) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, err := canonicalOrigin(r.Header.Get("Origin"))
		if err != nil || origin != h.expectedOrigin || r.Header.Get("X-Staff-Request") != "1" {
			http.Error(w, "request origin is not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func canonicalOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && isLoopbackOriginHost(u.Hostname())) {
		return "", errors.New("origin must use HTTPS except on loopback")
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

func isLoopbackOriginHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

type identityContextKey struct{}

func IdentityFromContext(r *http.Request) (staff.Identity, bool) {
	identity, ok := r.Context().Value(identityContextKey{}).(staff.Identity)
	return identity, ok
}

func withIdentity(ctx context.Context, identity staff.Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func decodeAuthRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, destination); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return false
	}
	return true
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "unable to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func RegisterAuthRoutes(mux *http.ServeMux, handler *AuthHandler) error {
	if mux == nil || handler == nil {
		return errors.New("staff auth routes require a mux and handler")
	}

	mux.HandleFunc("POST /api/v1/staff/auth/login", handler.StartLogin)
	mux.HandleFunc("POST /api/v1/staff/auth/mfa", handler.CompleteLogin)
	mux.Handle("GET /api/v1/staff/auth/session", handler.RequireSession(http.HandlerFunc(handler.Session)))
	mux.Handle("POST /api/v1/staff/auth/logout", handler.RequireCSRF(http.HandlerFunc(handler.Logout)))
	mux.HandleFunc("POST /api/v1/staff/invitations/setup/start", handler.StartInvitationEnrollment)
	mux.HandleFunc("POST /api/v1/staff/invitations/setup/complete", handler.CompleteInvitationEnrollment)

	return nil
}
