package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/booking"
	"github.com/jjmbeba/lumora/backend/internal/booking/httpapi"
	bookingpostgres "github.com/jjmbeba/lumora/backend/internal/booking/postgres"
	"github.com/jjmbeba/lumora/backend/internal/checkout"
	"github.com/jjmbeba/lumora/backend/internal/config"
	"github.com/jjmbeba/lumora/backend/internal/email/resend"
	paymentpostgres "github.com/jjmbeba/lumora/backend/internal/payment/postgres"
	staffdomain "github.com/jjmbeba/lumora/backend/internal/staff"
	staffhttpapi "github.com/jjmbeba/lumora/backend/internal/staff/httpapi"
	staffpostgres "github.com/jjmbeba/lumora/backend/internal/staff/postgres"
	"github.com/jjmbeba/lumora/backend/internal/ticket"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := runMain(); err != nil {
		log.Printf("booking API stopped: %v", err)
		os.Exit(1)
	}
}

func runMain() error {
	dsn, err := config.RequireEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	listenAddr := os.Getenv("BOOKING_API_ADDR")
	if listenAddr == "" {
		listenAddr = ":8081"
	}
	staffFrontendOrigin, err := config.RequireEnv("STAFF_FRONTEND_ORIGIN")
	if err != nil {
		return err
	}

	resendApiKey, err := config.RequireEnv("RESEND_API_KEY")
	if err != nil {
		return err
	}

	emailFrom, err := config.RequireEnv("RESEND_EMAIL_FROM")
	if err != nil {
		return err
	}

	bookingHmacKey, err := config.RequireEnv("BOOKING_ACCESS_CODE_HMAC_KEY")
	if err != nil {
		return err
	}
	credentialKeyring, err := config.TicketCredentialKeyringFromEnv()
	if err != nil {
		return err
	}
	defer clearCredentialKeys(credentialKeyring.Keys)

	credentialCipher, err := ticket.NewCredentialCipher(credentialKeyring.Keys, credentialKeyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure ticket credential cipher: %w", err)
	}
	staffMFAKeyring, err := config.StaffMFAKeyringFromEnv()
	if err != nil {
		return err
	}
	defer clearStaffMFAKeys(staffMFAKeyring.Keys)
	staffMFACipher, err := staffdomain.NewMFACipher(staffMFAKeyring.Keys, staffMFAKeyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure staff MFA cipher: %w", err)
	}
	staffInvitationKeyring, err := config.StaffInvitationKeyringFromEnv()
	if err != nil {
		return err
	}
	defer clearStaffInvitationKeys(staffInvitationKeyring.Keys)
	staffInvitationCipher, err := staffdomain.NewInvitationTokenCipher(staffInvitationKeyring.Keys, staffInvitationKeyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure staff invitation token cipher: %w", err)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()

	return run(ctx, dsn, resendApiKey, emailFrom, bookingHmacKey, listenAddr, staffFrontendOrigin, credentialCipher, staffMFACipher, staffInvitationCipher)
}

func run(ctx context.Context, dsn, resendApiKey, emailFrom, bookingHmacKey, listenAddr, staffFrontendOrigin string, credentialCipher *ticket.CredentialCipher, staffMFACipher *staffdomain.MFACipher, staffInvitationCipher *staffdomain.InvitationTokenCipher) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}

	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	store, err := bookingpostgres.NewPostgresStore(db, credentialCipher)
	if err != nil {
		return err
	}
	initializationStore, err := paymentpostgres.NewInitializationStore(db)
	if err != nil {
		return err
	}

	service, err := booking.NewService(store)
	if err != nil {
		return err
	}
	checkoutService, err := checkout.NewService(service, initializationStore)
	if err != nil {
		return err
	}

	resendSender, err := resend.NewResendSender(resendApiKey, emailFrom, resend.Config{Environment: os.Getenv("APP_ENV"), TestRecipient: os.Getenv("RESEND_TEST_RECIPIENT")})
	if err != nil {
		return err
	}

	verificationService, err := booking.NewEmailVerificationService(store, resendSender, []byte(bookingHmacKey))
	if err != nil {
		return err
	}

	bookingHandler, err := httpapi.NewCreateBookingHandler(checkoutService)
	if err != nil {
		return err
	}

	emailHandler, err := httpapi.NewEmailVerificationHandler(verificationService)
	if err != nil {
		return err
	}

	bookingDetailsHandler, err := httpapi.NewBookingDetailsHandler(service, verificationService)
	if err != nil {
		return err
	}
	staffStore, err := staffpostgres.NewStore(db)
	if err != nil {
		return err
	}
	staffAuthService, err := staffdomain.NewAuthService(staffStore, staffMFACipher)
	if err != nil {
		return err
	}
	staffAuthHandler, err := staffhttpapi.NewAuthHandler(staffAuthService, true, staffFrontendOrigin)
	if err != nil {
		return err
	}
	staffManagementService, err := staffdomain.NewManagementService(staffStore, staffInvitationCipher)
	if err != nil {
		return err
	}

	staffManagementHandler, err := staffhttpapi.NewManagementHandler(staffManagementService)
	if err != nil {
		return err
	}

	paymentInitializationHandler, err := httpapi.NewPaymentInitializationStatusHandler(checkoutService, verificationService)
	if err != nil {
		return err
	}

	verificationRateLimiter, err := httpapi.NewEmailVerificationRateLimiter(time.Now)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/bookings", bookingHandler)
	mux.Handle("GET /api/v1/bookings/{reference}", bookingDetailsHandler)
	mux.Handle("GET /api/v1/bookings/{reference}/payment-initialization", paymentInitializationHandler)
	if err := staffhttpapi.RegisterAuthRoutes(mux, staffAuthHandler); err != nil {
		return err
	}
	if err := staffhttpapi.RegisterManagementRoutes(mux, staffAuthHandler, staffManagementHandler); err != nil {
		return err
	}

	if err := httpapi.RegisterEmailVerificationRoutes(mux, emailHandler, verificationRateLimiter); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for booking API on %s: %w", server.Addr, err)
	}
	log.Printf("booking API listening on %s", listener.Addr())

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve booking API: %w", err)

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down booking API: %w", err)
		}

		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve booking API: %w", err)
		}

		return nil
	}
}

func clearCredentialKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}

func clearStaffMFAKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}

func clearStaffInvitationKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
