// Command gostore is an htmx storefront and admin for a small catalog of
// physical goods, with PayFast as its payment gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// Two operational escape hatches, so migrations don't have to be a side
	// effect of starting the server: `-migrate` runs them as its own deploy
	// step, `-migrate-status` answers "is this database up to date?".
	migrateOnly := flag.Bool("migrate", false, "apply pending migrations and exit")
	migrateStatus := flag.Bool("migrate-status", false, "print migration status and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	// Signals cancel this context, which unblocks the wait below and triggers
	// a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *migrateStatus {
		return printMigrationStatus(ctx, pool, log)
	}

	// Migrations run before serving: the app must never handle a request
	// against a schema it does not expect.
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	if *migrateOnly {
		return nil
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           routes(pool, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "store", cfg.StoreName, "currency", cfg.Currency)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func printMigrationStatus(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	status, err := db.Status(ctx, pool, log)
	if err != nil {
		return err
	}
	for _, s := range status {
		applied := "pending"
		if !s.AppliedAt.IsZero() {
			applied = s.AppliedAt.Format(time.RFC3339)
		}
		fmt.Printf("%-6d %-10s %-25s %s\n", s.Source.Version, s.State, applied, s.Source.Path)
	}
	return nil
}

func routes(pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(pool, log))
	return mux
}

// healthz reports readiness, including the database, so a container platform
// can gate traffic on it.
func healthz(pool *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			log.Error("healthz: database unreachable", "error", err)
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	// JSON to stdout: the one log format every managed platform ingests without
	// configuration.
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
