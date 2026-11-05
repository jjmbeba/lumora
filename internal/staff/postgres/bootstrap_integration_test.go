package postgres_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/staff"
	staffpostgres "example.com/cinema-tickets-backend/internal/staff/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestBootstrapOwnerAndMFAAuthentication(t *testing.T) {
	dsn := os.Getenv("STAFF_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set STAFF_TEST_DATABASE_URL to a disposable PostgreSQL database with migrations applied")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	store, err := staffpostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
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
	bootstrapRequest := staff.BootstrapOwnerRequest{
		Email: "Owner@Example.Test", DisplayName: "Test Owner", Password: "secure-test-password",
		TOTPSecret: secret, TOTPCode: totpCode(secret, time.Now()),
	}
	created, err := service.BootstrapFirstOwner(ctx, bootstrapRequest)
	if err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
	if created.Account.Email != "owner@example.test" || created.Account.Role != staff.RoleManager || !created.Account.MFAEnabled {
		t.Fatalf("unexpected bootstrap account: %+v", created.Account)
	}
	if len(created.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(created.RecoveryCodes))
	}

	var status, role string
	var ciphertext []byte
	var keyVersion int16
	if err := db.QueryRowContext(ctx, `
		SELECT status::text, role::text, mfa_secret_ciphertext, mfa_key_version
		FROM staff_accounts WHERE id = $1`, created.Account.ID,
	).Scan(&status, &role, &ciphertext, &keyVersion); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" || role != "MANAGER" || keyVersion != 1 {
		t.Fatalf("stored owner status/role/key version = %s/%s/%d", status, role, keyVersion)
	}
	if _, err := cipher.Decrypt(created.Account.Email, keyVersion, ciphertext); err != nil {
		t.Fatalf("decrypt stored TOTP secret: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT code_digest FROM staff_recovery_codes WHERE staff_account_id = $1`, created.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedDigests := make(map[[32]byte]bool)
	for rows.Next() {
		var digest []byte
		if err := rows.Scan(&digest); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if len(digest) != sha256.Size {
			rows.Close()
			t.Fatalf("stored recovery digest length = %d, want %d", len(digest), sha256.Size)
		}
		var fixed [32]byte
		copy(fixed[:], digest)
		storedDigests[fixed] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(storedDigests) != 10 {
		t.Fatalf("stored recovery digests = %d, want 10", len(storedDigests))
	}
	for _, code := range created.RecoveryCodes {
		digest := sha256.Sum256([]byte(code))
		if !storedDigests[digest] {
			t.Fatal("recovery code digest was not persisted")
		}
	}

	challenge, err := service.StartLogin(ctx, " OWNER@example.test ", "secure-test-password")
	if err != nil {
		t.Fatalf("start login: %v", err)
	}
	identity, session, err := service.CompleteLogin(ctx, challenge.Token, totpCode(secret, time.Now()))
	if err != nil {
		t.Fatalf("complete TOTP login: %v", err)
	}
	if identity.AccountID != created.Account.ID || session.Token == "" {
		t.Fatalf("unexpected authenticated result: identity=%+v session=%+v", identity, session)
	}
	if _, _, err := service.AuthenticateSession(ctx, session.Token); err != nil {
		t.Fatalf("authenticate created session: %v", err)
	}

	recoveryChallenge, err := service.StartLogin(ctx, created.Account.Email, "secure-test-password")
	if err != nil {
		t.Fatalf("start recovery-code login: %v", err)
	}
	_, recoverySession, err := service.CompleteLogin(ctx, recoveryChallenge.Token, created.RecoveryCodes[0])
	if err != nil {
		t.Fatalf("complete recovery-code login: %v", err)
	}
	var consumedAt sql.NullTime
	recoveryDigest := sha256.Sum256([]byte(created.RecoveryCodes[0]))
	if err := db.QueryRowContext(ctx, `
		SELECT consumed_at FROM staff_recovery_codes
		WHERE staff_account_id = $1 AND code_digest = $2`, created.Account.ID,
		recoveryDigest[:],
	).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if !consumedAt.Valid {
		t.Fatal("recovery code was not marked consumed")
	}

	replayChallenge, err := service.StartLogin(ctx, created.Account.Email, "secure-test-password")
	if err != nil {
		t.Fatalf("start recovery replay login: %v", err)
	}
	if _, _, err := service.CompleteLogin(ctx, replayChallenge.Token, created.RecoveryCodes[0]); !errors.Is(err, staff.ErrInvalidMFA) {
		t.Fatalf("reused recovery code error = %v, want ErrInvalidMFA", err)
	}

	if err := service.RevokeSession(ctx, session.Token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, _, err := service.AuthenticateSession(ctx, session.Token); !errors.Is(err, staff.ErrInvalidSession) {
		t.Fatalf("revoked session error = %v, want ErrInvalidSession", err)
	}
	if _, err := service.BootstrapFirstOwner(ctx, bootstrapRequest); !errors.Is(err, staff.ErrBootstrapUnavailable) {
		t.Fatalf("second bootstrap error = %v, want ErrBootstrapUnavailable", err)
	}
	if _, _, err := service.AuthenticateSession(ctx, recoverySession.Token); err != nil {
		t.Fatalf("authenticate recovery-code session: %v", err)
	}
}

func totpCode(secret string, now time.Time) string {
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		panic(fmt.Sprintf("decode generated TOTP secret: %v", err))
	}
	defer clear(key)
	step := uint64(now.Unix() / 30)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], step)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	index := sum[len(sum)-1] & 0x0f
	value := (binary.BigEndian.Uint32(sum[index:index+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", value)
}
