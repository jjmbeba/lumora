package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestInitializationReceiptMigration(t *testing.T) {
	db, _ := integrationStore(t)
	migration, err := os.ReadFile("../../../migrations/05-payment-initialization-receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh=%t", fresh), func(t *testing.T) {
			ctx := context.Background()
			schema := fmt.Sprintf("receipt_migration_%d", integrationID.Add(1))
			if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
					t.Errorf("drop temporary schema: %v", err)
				}
			})
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(ctx, `SET search_path TO public`)
			if fresh {
				baseline, err := os.ReadFile("../../../migrations/migration.sql")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(ctx, string(baseline)); err != nil {
					t.Fatalf("apply fresh baseline: %v", err)
				}
			} else {
				_, err = conn.ExecContext(ctx, `
					CREATE TYPE "PaymentMethod" AS ENUM ('CARD', 'MPESA');
					CREATE TABLE payment_initializations (
						id integer PRIMARY KEY, idempotency_key varchar UNIQUE NOT NULL,
						request_fingerprint varchar NOT NULL, payment_method "PaymentMethod" NOT NULL,
						created_at timestamptz NOT NULL
					);
					INSERT INTO payment_initializations VALUES (1, 'legacy-key', 'legacy-fingerprint', 'MPESA', '2026-01-01T00:00:00Z');
				`)
				if err != nil {
					t.Fatalf("prepare legacy schema: %v", err)
				}
			}
			for i := 0; i < 2; i++ {
				if _, err := conn.ExecContext(ctx, string(migration)); err != nil {
					t.Fatalf("apply receipt migration %d: %v", i+1, err)
				}
			}
			var legacyColumns int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'payment_initializations' AND column_name IN ('idempotency_key', 'request_fingerprint')`).Scan(&legacyColumns); err != nil || legacyColumns != 0 {
				t.Fatalf("legacy columns = %d, error = %v", legacyColumns, err)
			}
			if !fresh {
				var fingerprint, method string
				var initializationID int
				if err := conn.QueryRowContext(ctx, `SELECT request_fingerprint, requested_payment_method::text, payment_initialization_id FROM payment_initialization_receipts WHERE idempotency_key = 'legacy-key'`).Scan(&fingerprint, &method, &initializationID); err != nil || fingerprint != "legacy-fingerprint" || method != "MPESA" || initializationID != 1 {
					t.Fatalf("backfilled receipt = %q %q %d, error = %v", fingerprint, method, initializationID, err)
				}
			}
		})
	}
}
