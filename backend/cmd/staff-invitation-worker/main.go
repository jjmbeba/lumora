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

	"github.com/jjmbeba/lumora/backend/internal/config"
	"github.com/jjmbeba/lumora/backend/internal/email/resend"
	"github.com/jjmbeba/lumora/backend/internal/staff"
	staffpostgres "github.com/jjmbeba/lumora/backend/internal/staff/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const maxInvitationEmailAttempts = 10

func main() {
	if err := runMain(); err != nil {
		log.Printf("staff invitation email worker stopped: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, apiKey, fromAddress, setupURL string, cipher *staff.InvitationTokenCipher, workerConfig staff.InvitationEmailWorkerConfig) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}

	defer db.Close()

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	store, err := staffpostgres.NewInvitationEmailOutboxStore(db)
	if err != nil {
		return err
	}

	sender, err := resend.NewResendSender(apiKey, fromAddress)
	if err != nil {
		return err
	}

	workerConfig.SetupURL = setupURL
	workerConfig.MaxAttempts = maxInvitationEmailAttempts
	worker, err := staff.NewInvitationEmailWorker(store, sender, cipher, workerConfig)
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

	setupURL, err := config.RequireEnv("STAFF_INVITATION_SETUP_URL")
	if err != nil {
		return err
	}

	pollingInterval, err := config.Duration("STAFF_INVITATION_WORKER_POLLING_INTERVAL", 2*time.Second)
	if err != nil {
		return err
	}

	leaseDuration, err := config.Duration("STAFF_INVITATION_WORKER_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return err
	}

	sendTimeout, err := config.Duration("STAFF_INVITATION_WORKER_SEND_TIMEOUT", 15*time.Second)
	if err != nil {
		return err
	}

	keyring, err := config.StaffInvitationKeyringFromEnv()
	if err != nil {
		return err
	}

	defer clearInvitationKeys(keyring.Keys)

	cipher, err := staff.NewInvitationTokenCipher(keyring.Keys, keyring.CurrentVersion)
	if err != nil {
		return fmt.Errorf("configure staff invitation token cipher: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = run(ctx, dsn, apiKey, fromAddress, setupURL, cipher, staff.InvitationEmailWorkerConfig{
		PollingInterval: pollingInterval,
		LeaseDuration:   leaseDuration,
		SendTimeout:     sendTimeout,
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func clearInvitationKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
