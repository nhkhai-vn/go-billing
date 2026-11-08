package postgres

import (
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	billing "github.com/nhkhai-vn/go-billing"
	"github.com/nhkhai-vn/go-billing/storetest"
)

// migratedPool connects to BILLING_POSTGRES_URL and migrates it twice, so every test also checks that Migrate is
// idempotent.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("BILLING_POSTGRES_URL")
	if url == "" {
		t.Skip("BILLING_POSTGRES_URL is not set; set it to a PostgreSQL URL to run the store tests")
	}
	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	for range 2 {
		if err := Migrate(t.Context(), pool); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	return pool
}

func TestStore(t *testing.T) {
	pool := migratedPool(t)
	storetest.Run(t, func(*testing.T) billing.Store { return NewStore(pool) })
}

func TestMigrateRecordsVersions(t *testing.T) {
	pool := migratedPool(t)
	list, err := listMigrations()
	if err != nil {
		t.Fatalf("listMigrations: %v", err)
	}
	want := make([]int64, 0, len(list))
	for _, m := range list {
		want = append(want, m.version)
	}
	rows, err := pool.Query(t.Context(), `SELECT version FROM billing_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query versions: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("collect versions: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("recorded versions %v, want %v", got, want)
	}
}
