// Package config loads all runtime configuration from the environment.
//
// Everything the server needs comes from env vars, so the same binary and image
// run unchanged on a VM, in Compose, or on a managed container platform.
package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
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

	// SessionSecretPrevious, when set, is still accepted for verifying existing
	// sessions but never used to sign new ones — so SESSION_SECRET can be
	// rotated without signing the operator out. Remove it once every session
	// signed with it has expired.
	SessionSecretPrevious []byte

	// PayFast is the payment gateway's configuration. It is a flat struct here
	// rather than the gateway package's own Config so that config depends on no
	// gateway: main assembles the two, which is also where a second gateway
	// would be chosen.
	PayFast PayFast

	// TrustProxyIP makes the server believe X-Forwarded-For. It must be false
	// unless something in front of the server is actually setting that header,
	// because a client can otherwise claim any IP it likes — and the payment
	// callback's source-IP check is one of the things that would then be
	// trivially bypassed.
	TrustProxyIP bool

	// CookieSecure is derived from BaseURL rather than configured separately:
	// an HTTPS deployment always wants Secure cookies, and localhost
	// development cannot use them.
	CookieSecure bool

	// EmbedOrigins are the origins allowed to fetch the read-only catalog
	// fragments cross-origin, for dropping the catalog into a page hosted
	// elsewhere. Empty means no CORS headers at all, which is the right default
	// for a store that is only ever browsed on its own domain.
	EmbedOrigins []string

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// PayFast is what the PayFast gateway needs from the environment. The merchant
// id and key are required: a store that cannot take a payment is not a store, and
// discovering that at the first checkout is worse than discovering it at boot.
//
// Notification URLs are derived from BaseURL rather than configured, with one
// override — NotifyURL — because that is the one PayFast's own servers have to
// reach, which on a development machine means a tunnel's hostname and not
// localhost.
type PayFast struct {
	MerchantID  string
	MerchantKey string
	Passphrase  string
	Sandbox     bool

	NotifyURL string

	// AllowedCIDRs overrides PayFast's published source ranges. It is
	// configuration rather than a constant because PayFast has changed its ranges
	// before, and adding one should not need a release of this project.
	AllowedCIDRs []string
	// AllowAnySourceIP disables the source-IP check entirely. See
	// PAYFAST_ALLOWED_CIDRS=any in .env.example: it is for testing against the
	// sandbox and never right in production.
	AllowAnySourceIP bool
}

// AllowsEmbedding reports whether any origin may fetch the catalog fragments.
func (c Config) AllowsEmbedding() bool { return len(c.EmbedOrigins) > 0 }

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
		TrustProxyIP:      boolEnv("TRUST_PROXY_IP", false),
		PayFast: PayFast{
			MerchantID:  os.Getenv("PAYFAST_MERCHANT_ID"),
			MerchantKey: os.Getenv("PAYFAST_MERCHANT_KEY"),
			Passphrase:  os.Getenv("PAYFAST_PASSPHRASE"),
			// Sandbox defaults to true: the wrong default here takes real money
			// from a real card during somebody's first afternoon with the project.
			Sandbox:   boolEnv("PAYFAST_SANDBOX", true),
			NotifyURL: strings.TrimSpace(os.Getenv("PAYFAST_NOTIFY_URL")),
		},
	}
	c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.PayFast.MerchantID == "" {
		missing = append(missing, "PAYFAST_MERCHANT_ID")
	}
	if c.PayFast.MerchantKey == "" {
		missing = append(missing, "PAYFAST_MERCHANT_KEY")
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

	decoded, err := decodeSecret("SESSION_SECRET", secret)
	if err != nil {
		return Config{}, err
	}
	c.SessionSecret = decoded
	if prev := os.Getenv("SESSION_SECRET_PREVIOUS"); prev != "" {
		c.SessionSecretPrevious, err = decodeSecret("SESSION_SECRET_PREVIOUS", prev)
		if err != nil {
			return Config{}, err
		}
	}

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

	// Origins are compared literally against the browser's Origin header, so a
	// trailing slash or a path in one of these would never match anything and is
	// better reported now than debugged later.
	for _, origin := range strings.Split(os.Getenv("EMBED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin != "*" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
				return Config{}, fmt.Errorf("config: EMBED_ORIGINS entry %q must be scheme://host[:port] with no path", origin)
			}
		}
		c.EmbedOrigins = append(c.EmbedOrigins, origin)
	}

	// "any" is spelled out rather than being an empty list, so disabling a
	// security check is something an operator typed on purpose.
	switch cidrs := strings.TrimSpace(os.Getenv("PAYFAST_ALLOWED_CIDRS")); cidrs {
	case "":
	case "any":
		c.PayFast.AllowAnySourceIP = true
	default:
		for _, cidr := range strings.Split(cidrs, ",") {
			if cidr = strings.TrimSpace(cidr); cidr != "" {
				c.PayFast.AllowedCIDRs = append(c.PayFast.AllowedCIDRs, cidr)
			}
		}
	}

	if c.PayFast.NotifyURL != "" {
		u, err := url.Parse(c.PayFast.NotifyURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return Config{}, fmt.Errorf("config: PAYFAST_NOTIFY_URL %q must be an absolute URL", c.PayFast.NotifyURL)
		}
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

func decodeSecret(name, value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("config: %s must be base64: %w", name, err)
	}
	if len(decoded) < auth.MinSecretLen {
		return nil, fmt.Errorf("config: %s decodes to %d bytes, want at least %d "+
			"(generate one with `openssl rand -base64 32`)", name, len(decoded), auth.MinSecretLen)
	}
	return decoded, nil
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

// boolEnv reads a flag. Only the obvious spellings count as true, and anything
// else is false — a typo turning a safety default off silently is worse than a
// typo being ignored.
func boolEnv(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
