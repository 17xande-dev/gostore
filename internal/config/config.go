// Package config loads all runtime configuration from the environment.
//
// Everything the server needs comes from env vars, so the same binary and image
// run unchanged on a VM, in Compose, or on a managed container platform.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from the environment, applying defaults and
// returning an error listing every missing or malformed required value.
func Load() (Config, error) {
	c := Config{
		Port:            env("PORT", "8080"),
		BaseURL:         strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		StoreName:       env("STORE_NAME", "gostore"),
		Currency:        env("CURRENCY", "ZAR"),
		TemplateDir:     os.Getenv("TEMPLATE_DIR"),
		LogLevel:        env("LOG_LEVEL", "info"),
		ShutdownTimeout: 15 * time.Second,
	}

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("config: required env vars not set: %s", strings.Join(missing, ", "))
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
