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

	"example.com/cinema-tickets-backend/internal/config"
	"example.com/cinema-tickets-backend/internal/payment/postgres"
	"example.com/cinema-tickets-backend/internal/payment/webhook"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := runMain(); err != nil {
		log.Printf("payment API stopped: %v", err)
		os.Exit(1)
	}
}

func runMain() error {
	dsn, err := config.RequireEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	paystackSecret, err := config.RequireEnv("PAYSTACK_SECRET_KEY")
	if err != nil {
		return err
	}

	listenAddr := os.Getenv("PAYMENT_API_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()

	return run(ctx, dsn, paystackSecret, listenAddr)
}

func run(ctx context.Context, dsn, paystackSecretKey, listenAddr string) error {
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

	store, err := postgres.NewWebhookStore(db)
	if err != nil {
		return err
	}

	handler, err := webhook.NewHandler(paystackSecretKey, store)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/paystack", handler)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for payment API on %s: %w", server.Addr, err)
	}
	log.Printf("payment API listening on %s", listener.Addr())

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve payment API: %w", err)

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down payment API: %w", err)
		}

		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve payment API: %w", err)
		}

		return nil
	}
}
