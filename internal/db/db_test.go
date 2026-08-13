package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These two need no database: they catch the mistake of adding a migration
// file that goose will silently ignore or refuse to parse.

func TestEmbeddedMigrations_AreNamedForGoose(t *testing.T) {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found")
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		version, _, found := strings.Cut(base, "_")
		if !found {
			t.Errorf("%s: must be named NNNN_name.sql", base)
			continue
		}
		if strings.TrimLeft(version, "0123456789") != "" {
			t.Errorf("%s: version prefix %q is not numeric", base, version)
		}
	}
}

func TestEmbeddedMigrations_HaveUpAnnotations(t *testing.T) {
	names, _ := fs.Glob(migrationsFS, "migrations/*.sql")
	for _, name := range names {
		b, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Without this annotation goose applies nothing at all, which looks
		// exactly like a migration that ran and did its job.
		if !strings.Contains(string(b), "-- +goose Up") {
			t.Errorf("%s: missing a `-- +goose Up` annotation", name)
		}
	}
}

func TestMigrate_AppliesAndIsIdempotent(t *testing.T) {
	pool, log := testPool(t)
	ctx := t.Context()

	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id > 0").Scan(&applied); err != nil {
		t.Fatalf("count goose_db_version: %v", err)
	}
	embedded, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if applied != len(embedded) {
		t.Errorf("%d migrations recorded, want %d", applied, len(embedded))
	}

	// A table from 0001 must exist and be usable.
	var products int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&products); err != nil {
		t.Fatalf("query products: %v", err)
	}
}

func TestStatus_ReportsAppliedAndPending(t *testing.T) {
	pool, log := testPool(t)
	ctx := t.Context()

	before, err := Status(ctx, pool, log)
	if err != nil {
		t.Fatalf("Status before: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("Status returned no migrations")
	}
	for _, s := range before {
		if s.State == "applied" {
			t.Errorf("migration %d reported applied before Migrate ran", s.Source.Version)
		}
	}

	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	after, err := Status(ctx, pool, log)
	if err != nil {
		t.Fatalf("Status after: %v", err)
	}
	for _, s := range after {
		if s.State != "applied" {
			t.Errorf("migration %d is %q after Migrate, want applied", s.Source.Version, s.State)
		}
	}
}

func TestMigrate_RejectsOutOfOrderMigration(t *testing.T) {
	pool, log := testPool(t)
	ctx := t.Context()

	fsys := fstest.MapFS{
		"m/0005_first.sql": {Data: []byte("-- +goose Up\nCREATE TABLE a (id INT);\n")},
	}
	if err := MigrateFS(ctx, pool, fsys, "m", log); err != nil {
		t.Fatalf("MigrateFS: %v", err)
	}

	// Someone branches, numbers a migration below what production already ran,
	// and merges. That must fail loudly rather than apply out of order.
	fsys["m/0003_sneaked_in.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE b (id INT);\n")}
	if err := MigrateFS(ctx, pool, fsys, "m", log); err == nil {
		t.Fatal("expected an error for a migration numbered below the applied version, got nil")
	}
}

func TestMigrate_RollsBackAFailedMigration(t *testing.T) {
	pool, log := testPool(t)
	ctx := t.Context()

	fsys := fstest.MapFS{
		"m/0001_ok.sql":     {Data: []byte("-- +goose Up\nCREATE TABLE a (id INT);\n")},
		"m/0002_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE b (id INT);\nTHIS IS NOT SQL;\n")},
	}
	if err := MigrateFS(ctx, pool, fsys, "m", log); err == nil {
		t.Fatal("expected an error from the broken migration, got nil")
	}

	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('b') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("check table b: %v", err)
	}
	if exists {
		t.Error("table b exists; the failed migration was not rolled back")
	}

	var recorded int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id = 2").Scan(&recorded); err != nil {
		t.Fatalf("count goose_db_version: %v", err)
	}
	if recorded != 0 {
		t.Error("the failed migration was recorded as applied")
	}

	// The migration before it still applied, so a rerun resumes rather than
	// starting over.
	if err := pool.QueryRow(ctx, "SELECT to_regclass('a') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("check table a: %v", err)
	}
	if !exists {
		t.Error("table a is missing; an earlier successful migration was rolled back too")
	}
}

func TestMigrate_LeavesPoolUsable(t *testing.T) {
	pool, log := testPool(t)
	ctx := t.Context()

	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// goose runs through a database/sql handle wrapped around this pool;
	// closing that handle must not close the pool the server goes on to use.
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("pool unusable after Migrate: %v", err)
	}
}

// testPool connects to TEST_DATABASE_URL and gives the test its own schema, so
// tests never see each other's tables. The package skips entirely when the env
// var is unset, so `go test ./...` works without any infrastructure.
func testPool(t *testing.T) (*pgxpool.Pool, *slog.Logger) {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}

	ctx := t.Context()
	schema := "test_" + sanitize(t.Name())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE; CREATE SCHEMA %s", schema, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	// public is on the path as well as the test's own schema, because pg_trgm's
	// operators live there: the migration installs the extension into public
	// explicitly, and an extension *name* is database-global, so only the first
	// test schema to run would otherwise own it and every other one would fail
	// with "operator does not exist: text <% text".
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ", public"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect with search_path: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, err := pgxpool.New(context.WithoutCancel(ctx), url)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.Exec(context.WithoutCancel(ctx), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	return pool, slog.New(slog.DiscardHandler)
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
