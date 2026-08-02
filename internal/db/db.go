// Package db owns the Postgres connection pool and runs migrations on boot.
//
// Migrations are goose-managed .sql files embedded into the binary, applied in
// version order, one transaction each. goose is used as a library rather than
// through its CLI so a deploy is always a single binary with its schema
// changes travelling inside it — but the files are ordinary goose migrations,
// so the `goose` CLI works against this directory unchanged when a migration
// needs to be inspected, resumed, or applied by hand.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockID is an arbitrary but fixed key for the Postgres advisory lock
// goose holds while migrating, so concurrent boots of a scaled-out deployment
// queue instead of racing.
const migrationLockID int64 = 8_675_309_001

// Connect opens a pool and verifies it can reach the database.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse DATABASE_URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Migrate applies every embedded migration that has not been applied yet.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	return MigrateFS(ctx, pool, migrationsFS, "migrations", log)
}

// MigrateFS applies migrations from an arbitrary filesystem. Migrate uses the
// embedded set; tests use this directly.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, log *slog.Logger) error {
	provider, closeDB, err := newProvider(pool, fsys, dir, log)
	if err != nil {
		return err
	}
	defer closeDB()

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("db: apply migrations: %w", err)
	}
	for _, r := range results {
		log.Info("applied migration", "version", r.Source.Version, "name", r.Source.Path, "duration", r.Duration)
	}
	return nil
}

// Status returns each known migration and whether it has been applied, for
// operators asking "is this database up to date?" without a psql session.
func Status(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) ([]*goose.MigrationStatus, error) {
	provider, closeDB, err := newProvider(pool, migrationsFS, "migrations", log)
	if err != nil {
		return nil, err
	}
	defer closeDB()

	status, err := provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: migration status: %w", err)
	}
	return status, nil
}

// newProvider adapts the pgx pool to the database/sql handle goose wants. The
// returned func closes only that adapter, never the pool.
func newProvider(pool *pgxpool.Pool, fsys fs.FS, dir string, log *slog.Logger) (*goose.Provider, func(), error) {
	// goose resolves migrations from the root of the FS it is given.
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, nil, fmt.Errorf("db: migrations dir %q: %w", dir, err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)

	// A session-level advisory lock: goose holds it on one connection for the
	// whole run, so a second instance booting at the same time waits rather
	// than applying the same migration twice.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID))
	if err != nil {
		sqlDB.Close()
		return nil, nil, fmt.Errorf("db: create migration locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub,
		goose.WithSessionLocker(locker),
		goose.WithSlog(log),
		// Forward-only: a migration numbered below one already applied would
		// produce a different schema depending on when you first ran it, which
		// a published project must never do to an adopter's database.
		goose.WithAllowOutofOrder(false),
	)
	if err != nil {
		sqlDB.Close()
		return nil, nil, fmt.Errorf("db: create migration provider: %w", err)
	}

	return provider, func() { closeQuietly(sqlDB, log) }, nil
}

func closeQuietly(db *sql.DB, log *slog.Logger) {
	if err := db.Close(); err != nil {
		log.Warn("close migration database handle", "error", err)
	}
}
