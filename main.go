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
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/db"
	"github.com/17xande-dev/gostore/internal/email"
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
	// Operational escape hatches, so migrations don't have to be a side effect of
	// starting the server: `-migrate` runs them as its own deploy step,
	// `-migrate-status` answers "is this database up to date?", and
	// `-check-config` validates the environment without touching anything.
	migrateOnly := flag.Bool("migrate", false, "apply pending migrations and exit")
	migrateStatus := flag.Bool("migrate-status", false, "print migration status and exit")
	checkConfig := flag.Bool("check-config", false, "validate the full server configuration and exit")
	flag.Parse()

	// The migration modes load only DATABASE_URL. A schema change has no gateway
	// and no session, so a migration job should not have to be trusted with the
	// merchant key and the session secret to run one — see config.LoadTool.
	//
	// What that gives up is the accident that a broken payment config used to
	// fail at the migration step, before the schema moved. -check-config is that
	// same check, asked for on purpose: run it in the deploy alongside -migrate
	// to fail before the database changes rather than after.
	// -check-config wins over the migration modes, so that asking for the full
	// check and a migration in one command cannot report "ok" having validated
	// nothing but DATABASE_URL.
	load := config.Load
	if (*migrateOnly || *migrateStatus) && !*checkConfig {
		load = config.LoadTool
	}
	cfg, err := load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	if *checkConfig {
		fmt.Println("config: ok")
		return nil
	}

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

	sessions, err := auth.NewSessions(cfg.SessionSecret, cfg.SessionSecretPrevious, cfg.SessionTTL)
	if err != nil {
		return err
	}

	gateway, err := newGateway(cfg, log)
	if err != nil {
		return err
	}
	mail, err := newMailer(cfg, log)
	if err != nil {
		return err
	}
	// Image storage is built before the templates because a template resolves a
	// product's image key through it.
	images, err := newBlobStorage(cfg, log)
	if err != nil {
		return err
	}

	// Assets and templates are both read once at startup, so a broken override
	// fails the boot rather than the first request that happens to hit it.
	handler.SetStaticDir(cfg.StaticDir)
	if err := handler.CheckAssets(); err != nil {
		return err
	}
	if cfg.StaticDir != "" {
		log.Info("static assets may be overridden from disk", "dir", cfg.StaticDir)
	}
	tmpl, err := handler.ParseTemplates(cfg.TemplateDir, images)
	if err != nil {
		return err
	}
	carts := cart.NewStore(pool)
	h := handler.New(cfg, log, tmpl, catalog.NewStore(pool), carts,
		orders.NewStore(pool), gateway, mail, images, sessions)

	// Abandoned carts are swept in-process, on this context, so it stops with the
	// server rather than outliving it.
	startCartCleanup(ctx, carts, cfg.CartTTLDays, log)

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

// newMailer builds the mail sender, or a Discard that logs what it drops.
//
// Mail being unconfigured is a warning and not a boot failure: the shop's job is to
// take an order and record it, and that does not depend on a mail server. A store
// that refused to start because SMTP was missing would trade a working shop for a
// missing receipt.
func newMailer(cfg config.Config, log *slog.Logger) (email.Sender, error) {
	if !cfg.SMTP.Configured() {
		log.Warn("no SMTP configured: order confirmations will be logged and dropped, not sent",
			"fix", "set SMTP_HOST and EMAIL_FROM")
		return email.Discard{Log: log}, nil
	}

	policy, err := email.ParseTLSPolicy(cfg.SMTP.TLS)
	if err != nil {
		return nil, err
	}
	if policy == email.TLSNone {
		log.Warn("SMTP TLS is disabled: credentials and order details go over the network in the clear",
			"host", cfg.SMTP.Host)
	}

	sender, err := email.NewSMTPSender(email.SMTPConfig{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		ReplyTo:  cfg.SMTP.ReplyTo,
		TLS:      policy,
	})
	if err != nil {
		return nil, err
	}
	log.Info("email configured", "host", cfg.SMTP.Host, "port", cfg.SMTP.Port,
		"from", cfg.SMTP.From, "tls", policy, "notify", cfg.OrderNotifyEmail)
	return sender, nil
}

// newBlobStorage builds object storage for product images, or an Unconfigured that
// refuses uploads.
//
// Refusing is right, where email drops silently: an upload is something an operator
// is doing and watching, so it should fail with a message. Nothing else in the store
// depends on this — the catalog, cart, checkout and payment path never touch it — so
// a deployment without object storage is a complete shop that pastes image URLs.
func newBlobStorage(cfg config.Config, log *slog.Logger) (blob.Storage, error) {
	// A directory this server serves itself: one binary, one volume, working
	// photographs, and no object storage to run. Not for a deployment behind a load
	// balancer or one that scales to zero — two instances do not share a directory.
	if cfg.ImageDir != "" {
		storage, err := blob.NewDisk(cfg.ImageDir)
		if err != nil {
			return nil, err
		}
		log.Info("product images stored on disk", "dir", storage.Dir(), "served_at", blob.ImagePrefix,
			"note", "a single instance with a persistent volume; use BLOB_* for anything scaled out")
		return storage, nil
	}

	if !cfg.Blob.Configured() {
		log.Warn("no image storage configured: products cannot have images",
			"enable", "set IMAGE_DIR for a local directory, or the BLOB_* variables for object storage")
		return blob.Unconfigured{}, nil
	}

	storage, err := blob.NewS3(blob.S3Config{
		Endpoint:      cfg.Blob.Endpoint,
		Bucket:        cfg.Blob.Bucket,
		AccessKey:     cfg.Blob.AccessKey,
		SecretKey:     cfg.Blob.SecretKey,
		Region:        cfg.Blob.Region,
		UseTLS:        cfg.Blob.UseTLS,
		PublicBaseURL: cfg.Blob.PublicBaseURL,
	})
	if err != nil {
		return nil, err
	}
	if !cfg.Blob.UseTLS {
		log.Warn("object storage TLS is disabled: credentials go over the network in the clear",
			"endpoint", cfg.Blob.Endpoint)
	}
	log.Info("object storage configured", "endpoint", cfg.Blob.Endpoint,
		"bucket", cfg.Blob.Bucket, "public_base", cfg.Blob.PublicBaseURL)
	return storage, nil
}

func routes(cfg config.Config, h *handler.Handler, gateway payment.Gateway, sessions *auth.Sessions, pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(pool, log))

	h.RegisterStorefront(mux)

	// Disk-backed images are served by this server, from the store's own origin —
	// which is why they need no CSP allowance beyond 'self'. A bucket-backed store
	// serves its own and this route does not exist.
	if cfg.ImageDir != "" {
		if err := h.RegisterImages(mux, cfg.ImageDir); err != nil {
			// The directory was checked at startup by blob.NewDisk, so reaching this
			// means it changed underneath us between then and now.
			log.Error("cannot serve product images", "dir", cfg.ImageDir, "error", err)
		}
	}

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
	policy := middleware.Policy{
		FrameAncestors: cfg.EmbedOrigins,
		FormActions:    []string{gateway.FormActionOrigin()},
		// Only on an https deployment: a browser ignores HSTS over plain HTTP
		// anyway, and sending it from localhost would pin a rule that makes the
		// next plain-HTTP project on this port unreachable.
		HSTS: cfg.CookieSecure,
	}
	if cfg.Blob.Configured() {
		// The origin, not the base URL: a CSP source carrying a path matches that
		// path exactly, which would permit the bucket root and refuse every image
		// under it. See Blob.PublicOrigin.
		policy.ImgSources = []string{cfg.Blob.PublicOrigin()}
	}
	return middleware.Chain(mux, middleware.SecurityHeaders(policy))
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
