package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/config"
	"github.com/jjmbeba/lumora/backend/internal/staff"
	staffpostgres "github.com/jjmbeba/lumora/backend/internal/staff/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		log.Printf("staff bootstrap failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	dsn, err := config.RequireEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	bootstrapSecret, err := config.RequireEnv("STAFF_BOOTSTRAP_SECRET")
	if err != nil {
		return err
	}

	if len(bootstrapSecret) < 32 {
		return errors.New("STAFF_BOOTSTRAP_SECRET must contain at least 32 bytes")
	}

	email, err := config.RequireEnv("STAFF_BOOTSTRAP_EMAIL")
	if err != nil {
		return err
	}

	displayName, err := config.RequireEnv("STAFF_BOOTSTRAP_DISPLAY_NAME")
	if err != nil {
		return err
	}

	password, err := config.RequireEnv("STAFF_BOOTSTRAP_PASSWORD")
	if err != nil {
		return err
	}

	keyring, err := config.StaffMFAKeyringFromEnv()
	if err != nil {
		return err
	}

	defer clearStaffMFAKeys(keyring.Keys)
	mfaCipher, err := staff.NewMFACipher(keyring.Keys, keyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure staff MFA cipher: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	store, err := staffpostgres.NewStore(db)
	if err != nil {
		return err
	}
	exists, err := store.BootstrapOwnerExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return staff.ErrBootstrapUnavailable
	}
	providedSecret, err := readBootstrapSecret()
	if err != nil {
		return err
	}
	defer clear(providedSecret)
	if !bootstrapSecretMatches(bootstrapSecret, providedSecret) {
		return errors.New("bootstrap authorization failed")
	}
	authService, err := staff.NewAuthService(store, mfaCipher)
	if err != nil {
		return err
	}

	secret, err := staff.NewTOTPSecret()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Add this account to the staff owner's authenticator app before continuing:")
	fmt.Fprintf(os.Stdout, "Setup key: %s\n", secret)
	fmt.Fprintf(os.Stdout, "Setup URI: %s\n", totpProvisioningURI(email, secret))
	fmt.Fprintln(os.Stdout, "Enter the current six-digit authenticator code:")
	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read authenticator code: %w", err)
	}
	code = strings.TrimSpace(code)
	result, err := authService.BootstrapFirstOwner(ctx, staff.BootstrapOwnerRequest{
		Email: email, DisplayName: displayName, Password: password,
		TOTPSecret: secret, TOTPCode: code,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Created manager account %s (id %d). Save these recovery codes now; they will not be shown again:\n", result.Account.Email, result.Account.ID)
	for _, recoveryCode := range result.RecoveryCodes {
		fmt.Fprintln(os.Stdout, recoveryCode)
	}
	return nil
}

func readBootstrapSecret() ([]byte, error) {
	stdinFD := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFD) {
		return nil, errors.New("staff bootstrap must be run from an interactive terminal")
	}
	fmt.Fprint(os.Stderr, "Bootstrap authorization secret: ")
	secret, err := term.ReadPassword(stdinFD)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		clear(secret)
		return nil, fmt.Errorf("read bootstrap authorization secret: %w", err)
	}
	return secret, nil
}

func bootstrapSecretMatches(expected string, provided []byte) bool {
	if len(expected) != len(provided) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), provided) == 1
}

func totpProvisioningURI(email, secret string) string {
	label := url.PathEscape("Cinema Ticketing:" + strings.TrimSpace(email))
	query := url.Values{
		"secret": {secret}, "issuer": {"Cinema Ticketing"}, "algorithm": {"SHA1"},
		"digits": {"6"}, "period": {"30"},
	}
	return "otpauth://totp/" + label + "?" + query.Encode()
}

func clearStaffMFAKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
