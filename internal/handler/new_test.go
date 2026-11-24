package handler

import (
	"log/slog"
	"testing"
)

// mustNew builds a Handler for a test whose configuration is known good.
func mustNew(t *testing.T, d Deps) *Handler {
	t.Helper()
	h, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// A BASE_URL the kit cannot use as the CSRF origin is an error the caller can
// report at startup, not a panic inside the constructor.
func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{
		"",
		"https://Store.Example",   // upper case never matches the browser's Origin
		"https://store.example/",  // trailing slash
		"https://store.example/x", // path
		"https://store.example:443",
	} {
		t.Run(base, func(t *testing.T) {
			cfg := testConfig()
			cfg.BaseURL, cfg.CookieSecure = base, true
			defer func() {
				if v := recover(); v != nil {
					t.Fatalf("New panicked: %v", v)
				}
			}()
			h, err := New(Deps{Config: cfg, Log: slog.New(slog.DiscardHandler)})
			if err == nil || h != nil {
				t.Fatalf("New(BaseURL %q) = %v, %v; want an error", base, h, err)
			}
		})
	}
}
