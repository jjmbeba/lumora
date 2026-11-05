package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"time"

	"example.com/cinema-tickets-backend/internal/staff"
)

const (
	staffSessionCookie = "staff_session"
	maxAuthBodyBytes   = 8 << 10
)

type AuthHandler struct {
	service       *staff.AuthService
	secureCookies bool
}

type startLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type completeLoginRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
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

type identityResponse struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func NewAuthHandler(service *staff.AuthService, secureCookies bool) (*AuthHandler, error) {
	if service == nil {
		return nil, errors.New("staff auth service is required")
	}

	return &AuthHandler{service: service, secureCookies: secureCookies}, nil
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

	mux.HandleFunc("POST /staff/auth/login", handler.StartLogin)
	mux.HandleFunc("POST /staff/auth/mfa", handler.CompleteLogin)
	mux.HandleFunc("POST /staff/auth/logout", handler.Logout)

	return nil
}
