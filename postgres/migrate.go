package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLockKey is the advisory lock that serializes Migrate across processes.
const migrateLockKey = 0x62696c6c696e67

const (
	lockMigrations        = `SELECT pg_advisory_xact_lock($1)`
	createMigrationsTable = `CREATE TABLE IF NOT EXISTS billing_schema_migrations (
	version    bigint PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`
	migrationApplied = `SELECT EXISTS (SELECT 1 FROM billing_schema_migrations WHERE version = $1)`
	recordMigration  = `INSERT INTO billing_schema_migrations (version) VALUES ($1)`
)

type migration struct {
	version int64
	name    string
}

// Migrate creates or updates the billing tables. Each migration runs once, in its own transaction, so Migrate is safe
// to call on every start and from several processes at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	list, err := listMigrations()
	if err != nil {
		return err
	}
	for _, m := range list {
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return apply(ctx, tx, m) }); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
	}
	return nil
}

func listMigrations() ([]migration, error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	list := make([]migration, 0, len(names))
	for _, name := range names {
		prefix, _, _ := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", name, err)
		}
		list = append(list, migration{version: version, name: name})
	}
	return list, nil
}

func apply(ctx context.Context, tx pgx.Tx, m migration) error {
	if _, err := tx.Exec(ctx, lockMigrations, migrateLockKey); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	var applied bool
	if err := tx.QueryRow(ctx, migrationApplied, m.version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration: %w", err)
	}
	if applied {
		return nil
	}
	sql, err := migrations.ReadFile(m.name)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("run migration: %w", err)
	}
	if _, err := tx.Exec(ctx, recordMigration, m.version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return nil
}
