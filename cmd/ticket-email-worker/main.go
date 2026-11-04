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
	"example.com/cinema-tickets-backend/internal/email/resend"
	"example.com/cinema-tickets-backend/internal/ticket"
	ticketpostgres "example.com/cinema-tickets-backend/internal/ticket/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const maxEmailAttempts = 10

func main() {
	if err := runMain(); err != nil {
		log.Printf("ticket email worker stopped: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, resendAPIKey, fromAddress string, cipher *ticket.CredentialCipher, workerConfig ticket.TicketEmailWorkerConfig) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}

	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	store, err := ticketpostgres.NewEmailOutboxStore(db, cipher)
	if err != nil {
		return err
	}

	sender, err := resend.NewResendSender(resendAPIKey, fromAddress)
	if err != nil {
		return err
	}

	workerConfig.MaxAttempts = maxEmailAttempts
	worker, err := ticket.NewTicketEmailWorker(store, sender, workerConfig)
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
	apiKey, err := config.RequireEnv("RESEND_API_KEY")
	if err != nil {
		return err
	}
	fromAddress, err := config.RequireEnv("RESEND_FROM_EMAIL")
	if err != nil {
		return err
	}

	pollingInterval, err := config.Duration("TICKET_EMAIL_WORKER_POLLING_INTERVAL", 2*time.Second)
	if err != nil {
		return err
	}

	leaseDuration, err := config.Duration("TICKET_EMAIL_WORKER_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return err
	}

	sendTimeout, err := config.Duration("TICKET_EMAIL_WORKER_SEND_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}

	keyring, err := config.TicketCredentialKeyringFromEnv()
	if err != nil {
		return err
	}

	defer clearCredentialKeys(keyring.Keys)

	cipher, err := ticket.NewCredentialCipher(keyring.Keys, keyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure ticket credential cipher: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = run(ctx, dsn, apiKey, fromAddress, cipher, ticket.TicketEmailWorkerConfig{
		PollingInterval: pollingInterval,
		LeaseDuration:   leaseDuration,
		SendTimeout:     sendTimeout,
	})
	if ctx.Err() != nil {
		return nil
	}

	return err
}

func clearCredentialKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
