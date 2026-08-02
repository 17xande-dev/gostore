// Package config loads all runtime configuration from the environment.
//
// Everything the server needs comes from env vars, so the same binary and image
// run unchanged on a VM, in Compose, or on a managed container platform.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

// Config is the fully resolved configuration for one server process.
type Config struct {
	// Server
	Port            string
	BaseURL         string
	ShutdownTimeout time.Duration

	// Storage
	DatabaseURL string

	// Store presentation. No organisation-specific defaults live in the code;
	// adopters set these.
	StoreName string
	Currency  string

	// TemplateDir, when set, overlays same-named templates from disk over the
	// embedded defaults, so adopters can restyle without forking.
	TemplateDir string

	// Admin authentication. The password is stored only as a bcrypt hash, so a
	// leaked env file or a process listing does not hand over the credential —
	// which matters more than usual for a project others will copy as an
	// example. Generate one with `go run ./cmd/hashpw`.
	AdminPasswordHash string
	SessionSecret     []byte
	SessionTTL        time.Duration

	// CookieSecure is derived from BaseURL rather than configured separately:
	// an HTTPS deployment always wants Secure cookies, and localhost
	// development cannot use them.
	CookieSecure bool

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from the environment, applying defaults and
// returning an error listing every missing or malformed required value.
func Load() (Config, error) {
	c := Config{
		Port:              env("PORT", "8080"),
		BaseURL:           strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		StoreName:         env("STORE_NAME", "gostore"),
		Currency:          env("CURRENCY", "ZAR"),
		TemplateDir:       os.Getenv("TEMPLATE_DIR"),
		LogLevel:          env("LOG_LEVEL", "info"),
		AdminPasswordHash: os.Getenv("ADMIN_PASSWORD_HASH"),
		SessionTTL:        24 * time.Hour,
		ShutdownTimeout:   15 * time.Second,
	}
	c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	// The admin credentials are required rather than optional-with-a-default:
	// a store whose admin is reachable without a password is worse than a store
	// that refuses to start.
	if c.AdminPasswordHash == "" {
		missing = append(missing, "ADMIN_PASSWORD_HASH")
	}
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		missing = append(missing, "SESSION_SECRET")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("config: required env vars not set: %s", strings.Join(missing, ", "))
	}

	decoded, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return Config{}, fmt.Errorf("config: SESSION_SECRET must be base64: %w", err)
	}
	if len(decoded) < auth.MinSecretLen {
		return Config{}, fmt.Errorf("config: SESSION_SECRET decodes to %d bytes, want at least %d "+
			"(generate one with `openssl rand -base64 32`)", len(decoded), auth.MinSecretLen)
	}
	c.SessionSecret = decoded

	if h, ok := os.LookupEnv("SESSION_TTL_HOURS"); ok {
		n, err := strconv.Atoi(h)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: SESSION_TTL_HOURS must be a positive integer, got %q", h)
		}
		c.SessionTTL = time.Duration(n) * time.Hour
	}

	if d, ok := os.LookupEnv("SHUTDOWN_TIMEOUT_SECONDS"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("config: SHUTDOWN_TIMEOUT_SECONDS must be a non-negative integer, got %q", d)
		}
		c.ShutdownTimeout = time.Duration(n) * time.Second
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("config: LOG_LEVEL must be one of debug, info, warn, error; got %q", c.LogLevel)
	}

	if c.TemplateDir != "" {
		if fi, err := os.Stat(c.TemplateDir); err != nil {
			return Config{}, fmt.Errorf("config: TEMPLATE_DIR %q: %w", c.TemplateDir, err)
		} else if !fi.IsDir() {
			return Config{}, fmt.Errorf("config: TEMPLATE_DIR %q is not a directory", c.TemplateDir)
		}
	}

	return c, nil
}

// LoadTool loads what a database-only command-line tool needs, which is just
// DATABASE_URL. Tools like cmd/seed serve no HTTP and hold no session, so
// requiring the server's admin secrets before they will load a JSON file would
// be an obstacle with nothing behind it.
func LoadTool() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		LogLevel:    env("LOG_LEVEL", "info"),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: required env vars not set: DATABASE_URL")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
