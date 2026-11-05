package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/staff"
	staffhttpapi "example.com/cinema-tickets-backend/internal/staff/httpapi"
	staffpostgres "example.com/cinema-tickets-backend/internal/staff/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	staffHTTPTestEmail    = "staff-http-auth-integration@example.test"
	staffHTTPTestPassword = "staff-http-auth-integration-password"
)

func TestAuthHTTPFlowPostgres(t *testing.T) {
	dsn := os.Getenv("STAFF_HTTP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set STAFF_HTTP_TEST_DATABASE_URL to a disposable PostgreSQL database with migrations applied")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	store, err := staffpostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	// This test's database is dedicated. Remove only its known fixture account
	// first so a prior interrupted run can be repeated safely.
	if err := deleteStaffHTTPFixture(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := deleteStaffHTTPFixture(cleanupCtx, db); err != nil {
			t.Errorf("delete staff HTTP test fixture: %v", err)
		}
	})

	exists, err := store.BootstrapOwnerExists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("STAFF_HTTP_TEST_DATABASE_URL must point to a disposable database without a bootstrap owner")
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	defer clear(key)
	cipher, err := staff.NewMFACipher(map[int16][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	service, err := staff.NewAuthService(store, cipher)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := staff.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BootstrapFirstOwner(ctx, staff.BootstrapOwnerRequest{
		Email: staffHTTPTestEmail, DisplayName: "HTTP Auth Test Owner",
		Password: staffHTTPTestPassword, TOTPSecret: secret, TOTPCode: totpCodeForTest(secret, time.Now()),
	}); err != nil {
		t.Fatalf("create first owner fixture: %v", err)
	}

	handler, err := staffhttpapi.NewAuthHandler(service, true)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := staffhttpapi.RegisterAuthRoutes(mux, handler); err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /staff/test-protected", handler.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := staffhttpapi.IdentityFromContext(r)
		if !ok {
			http.Error(w, "missing identity", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, identity.Email)
	})))

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/staff/test-protected", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("protected request without cookie status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	loginResponse := httptest.NewRecorder()
	mux.ServeHTTP(loginResponse, jsonRequest(t, http.MethodPost, "/staff/auth/login", map[string]string{
		"email": strings.ToUpper(staffHTTPTestEmail), "password": staffHTTPTestPassword,
	}))
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("password login status = %d, body = %s", loginResponse.Code, loginResponse.Body.String())
	}
	if got := loginResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("password login Cache-Control = %q, want no-store", got)
	}
	if got := loginResponse.Result().Cookies(); len(got) != 0 {
		t.Fatalf("password-only login issued %d cookie(s), want no session cookie", len(got))
	}
	var challenge struct {
		Challenge string    `json:"challenge"`
		Purpose   string    `json:"purpose"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Challenge == "" || challenge.Purpose != string(staff.ChallengePurposeSignIn) || !challenge.ExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected MFA challenge response: %+v", challenge)
	}

	mfaResponse := httptest.NewRecorder()
	mux.ServeHTTP(mfaResponse, jsonRequest(t, http.MethodPost, "/staff/auth/mfa", map[string]string{
		"challenge": challenge.Challenge, "code": totpCodeForTest(secret, time.Now()),
	}))
	if mfaResponse.Code != http.StatusOK {
		t.Fatalf("MFA completion status = %d, body = %s", mfaResponse.Code, mfaResponse.Body.String())
	}
	if got := mfaResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("MFA completion Cache-Control = %q, want no-store", got)
	}
	cookies := mfaResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("MFA completion set %d cookies, want exactly one session cookie", len(cookies))
	}
	sessionCookie := cookies[0]
	if sessionCookie.Name != "staff_session" || sessionCookie.Value == "" || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Path != "/" {
		t.Fatalf("session cookie has unexpected attributes: %+v", sessionCookie)
	}
	if strings.Contains(mfaResponse.Body.String(), sessionCookie.Value) {
		t.Fatal("session token was returned in the JSON response as well as the cookie")
	}
	var login struct {
		Account struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"account"`
	}
	if err := json.Unmarshal(mfaResponse.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if login.Account.Email != staffHTTPTestEmail || login.Account.Role != string(staff.RoleManager) {
		t.Fatalf("unexpected authenticated identity: %+v", login.Account)
	}

	authorized := httptest.NewRecorder()
	protectedRequest := httptest.NewRequest(http.MethodGet, "/staff/test-protected", nil)
	protectedRequest.AddCookie(sessionCookie)
	mux.ServeHTTP(authorized, protectedRequest)
	if authorized.Code != http.StatusOK || authorized.Body.String() != staffHTTPTestEmail {
		t.Fatalf("authenticated protected request = %d %q, want 200 %q", authorized.Code, authorized.Body.String(), staffHTTPTestEmail)
	}

	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/staff/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	mux.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d", logout.Code, http.StatusNoContent)
	}
	clearedCookies := logout.Result().Cookies()
	if len(clearedCookies) != 1 || clearedCookies[0].Name != sessionCookie.Name || clearedCookies[0].MaxAge >= 0 || clearedCookies[0].Value != "" {
		t.Fatalf("logout did not clear the session cookie: %+v", clearedCookies)
	}

	revoked := httptest.NewRecorder()
	revokedRequest := httptest.NewRequest(http.MethodGet, "/staff/test-protected", nil)
	revokedRequest.AddCookie(sessionCookie)
	mux.ServeHTTP(revoked, revokedRequest)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("protected request after logout status = %d, want %d", revoked.Code, http.StatusUnauthorized)
	}
}

func jsonRequest(t *testing.T, method, target string, value any) *http.Request {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func deleteStaffHTTPFixture(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		DELETE FROM staff_sessions
		WHERE staff_account_id = (SELECT id FROM staff_accounts WHERE email = $1)
	`, staffHTTPTestEmail); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM staff_accounts WHERE email = $1`, staffHTTPTestEmail); err != nil {
		return err
	}
	return nil
}

func totpCodeForTest(secret string, now time.Time) string {
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		panic(fmt.Sprintf("decode generated test TOTP secret: %v", err))
	}
	defer clear(key)
	step := uint64(now.Unix() / 30)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], step)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", value)
}
