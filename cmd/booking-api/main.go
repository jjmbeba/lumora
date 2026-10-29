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

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/booking/httpapi"
	bookingpostgres "example.com/cinema-tickets-backend/internal/booking/postgres"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/config"
	"example.com/cinema-tickets-backend/internal/email/resend"
	paymentpostgres "example.com/cinema-tickets-backend/internal/payment/postgres"

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

	resendApiKey, err := config.RequireEnv("RESEND_API_KEY")
	if err != nil {
		return err
	}

	emailFrom, err := config.RequireEnv("EMAIL_FROM")
	if err != nil {
		return err
	}

	bookingHmacKey, err := config.RequireEnv("BOOKING_ACCESS_CODE_HMAC_KEY")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()

	return run(ctx, dsn, resendApiKey, emailFrom, bookingHmacKey, listenAddr)
}

func run(ctx context.Context, dsn, resendApiKey, emailFrom, bookingHmacKey, listenAddr string) error {
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

	store, err := bookingpostgres.NewPostgresStore(db)
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

	resendSender, err := resend.NewResendSender(resendApiKey, emailFrom)
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
	paymentInitializationHandler, err := httpapi.NewPaymentInitializationStatusHandler(checkoutService, verificationService)
	if err != nil {
		return err
	}

	verificationRateLimiter, err := httpapi.NewEmailVerificationRateLimiter(time.Now)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("POST /bookings", bookingHandler)
	mux.Handle("GET /bookings/{reference}", bookingDetailsHandler)
	mux.Handle("GET /bookings/{reference}/payment-initialization", paymentInitializationHandler)

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
