package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/cinema-tickets-backend/internal/config"
	"example.com/cinema-tickets-backend/internal/payment"
	"example.com/cinema-tickets-backend/internal/payment/paystack"
	"example.com/cinema-tickets-backend/internal/payment/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := runMain(); err != nil {
		log.Printf("payment worker stopped: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn string, leaseDuration time.Duration, pollingInterval time.Duration, paystackSecretKey string) error {
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

	store, err := postgres.NewPostgresStore(db, leaseDuration)
	if err != nil {
		return err
	}

	provider, err := paystack.NewPaystackProvider(paystackSecretKey)
	if err != nil {
		return err
	}

	worker, err := payment.NewWorker(store, provider, pollingInterval)
	if err != nil {
		return err
	}

	return worker.Run(ctx)
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

	leaseDuration, err := config.Duration("PAYMENT_WORKER_LEASE_DURATION", time.Second*30)
	if err != nil {
		return err
	}

	pollingInterval, err := config.Duration("PAYMENT_WORKER_POLLING_INTERVAL", time.Second*2)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()

	err = run(ctx, dsn, leaseDuration, pollingInterval, paystackSecret)
	if ctx.Err() != nil {
		// Cancellation comes from Ctrl+C or SIGTERM via NotifyContext, so an
		// interrupted worker loop is an expected process shutdown.
		return nil
	}
	return err
}
