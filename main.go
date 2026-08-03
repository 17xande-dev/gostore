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

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/db"
	"github.com/17xande-dev/gostore/internal/handler"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
	"github.com/17xande-dev/gostore/internal/payment/payfast"
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

	// Templates are parsed once at startup, so a broken override fails the boot
	// rather than the first request that happens to hit it.
	tmpl, err := handler.ParseTemplates(cfg.TemplateDir)
	if err != nil {
		return err
	}
	sessions, err := auth.NewSessions(cfg.SessionSecret, cfg.SessionSecretPrevious, cfg.SessionTTL)
	if err != nil {
		return err
	}

	gateway, err := newGateway(cfg, log)
	if err != nil {
		return err
	}
	h := handler.New(cfg, log, tmpl, catalog.NewStore(pool), cart.NewStore(pool),
		orders.NewStore(pool), gateway, sessions)

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           routes(cfg, h, gateway, sessions, pool, log),
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

// newGateway builds the payment gateway. PayFast is the only one, so this is a
// constructor rather than a registry — but it is the one place a second gateway
// would be chosen, which is why the rest of the server only ever sees the
// payment.Gateway interface.
func newGateway(cfg config.Config, log *slog.Logger) (payment.Gateway, error) {
	// PayFast settles in ZAR and nothing else. Discovering that at the first
	// checkout, after an order row already exists, is worse than at boot.
	if cfg.Currency != payfast.Currency {
		return nil, fmt.Errorf("config: CURRENCY is %q, but PayFast settles in %s only",
			cfg.Currency, payfast.Currency)
	}

	// The gateway's URLs are derived from BASE_URL, because three URLs that have
	// to agree with each other and with the deployment are three chances to get
	// one wrong. NotifyURL is the exception: PayFast's own servers have to reach
	// it, which during development means a tunnel's hostname rather than whatever
	// BASE_URL says.
	notify := cfg.BaseURL + "/payments/payfast/callback"
	if cfg.PayFast.NotifyURL != "" {
		notify = cfg.PayFast.NotifyURL
	}

	return payfast.New(payfast.Config{
		MerchantID:       cfg.PayFast.MerchantID,
		MerchantKey:      cfg.PayFast.MerchantKey,
		Passphrase:       cfg.PayFast.Passphrase,
		Sandbox:          cfg.PayFast.Sandbox,
		ReturnURL:        cfg.BaseURL + "/cart/checkout/success",
		CancelURL:        cfg.BaseURL + "/cart/checkout/cancel",
		NotifyURL:        notify,
		AllowedCIDRs:     cfg.PayFast.AllowedCIDRs,
		AllowAnySourceIP: cfg.PayFast.AllowAnySourceIP,
		Log:              log,
	})
}

func routes(cfg config.Config, h *handler.Handler, gateway payment.Gateway, sessions *auth.Sessions, pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(pool, log))

	h.RegisterStorefront(mux)

	// The gateway callback is mounted on this mux and not the first-party one, so
	// it sits outside CSRF protection by not being in the group — a payment
	// provider cannot carry a token. It authenticates itself instead; see
	// internal/handler/webhook.go.
	h.RegisterPayments(mux)

	// Everything that changes state is mounted here, behind CSRF protection and
	// the cookie nosurf needs to set for it. The catalog reads stay outside:
	// they are embeddable cross-origin, which means cookie-free.
	firstParty := h.FirstPartyHandler(middleware.RequireAdmin(sessions, log))
	mux.Handle("/admin/", firstParty)
	// Both patterns: one matches /cart exactly, the other everything below it —
	// which includes /cart/checkout, where the cart cookie is in scope.
	mux.Handle("/cart", firstParty)
	mux.Handle("/cart/", firstParty)

	// Security headers wrap everything, including 404s and /healthz. The origins
	// allowed to fetch the catalog are also the ones allowed to frame it, and the
	// gateway's origin has to be allowed as a form target or the browser blocks
	// the hand-over to payment.
	return middleware.Chain(mux, middleware.SecurityHeaders(middleware.Policy{
		FrameAncestors: cfg.EmbedOrigins,
		FormActions:    []string{gateway.FormActionOrigin()},
	}))
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
