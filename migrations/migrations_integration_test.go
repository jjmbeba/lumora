package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

var migrationTestID atomic.Int64

func TestEmbeddedMigrationsApplyAndReportStatus(t *testing.T) {
	dsn := os.Getenv("PAYMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAYMENT_TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close migration test database: %v", err)
		}
	})
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to migration test database: %v", err)
	}
	schema := fmt.Sprintf("goose_test_%d", migrationTestID.Add(1)+time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop migration test schema: %v", err)
		}
	})
	if _, err := db.ExecContext(ctx, `SET search_path TO `+schema+`, public`); err != nil {
		t.Fatal(err)
	}

	provider, err := NewProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	if len(results) != 8 {
		t.Fatalf("migrations applied = %d, want 8", len(results))
	}
	results, err = provider.Up(ctx)
	if err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("second Up() applied %d migrations, want none", len(results))
	}
	statuses, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("read migration status: %v", err)
	}
	if len(statuses) != 8 {
		t.Fatalf("migration statuses = %d, want 8", len(statuses))
	}
	for _, status := range statuses {
		if status.State != goose.StateApplied {
			t.Errorf("migration %d state = %q, want applied", status.Source.Version, status.State)
		}
	}
}
