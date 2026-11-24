package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/handler"
	"github.com/17xande-dev/gostore/internal/mcpserver"
)

// gatewayConfig is the smallest config newGateways reads, with both providers
// switched on. Tests turn one off rather than building a config from scratch.
func gatewayConfig() config.Config {
	return config.Config{
		BaseURL:  "https://store.example",
		Currency: "ZAR",
		PayFast: config.PayFast{
			MerchantID:  "10000100",
			MerchantKey: "46f0cd694581a",
			Sandbox:     true,
		},
		SnapScan: config.SnapScan{
			SnapCode:       "shopalot",
			APIKey:         "api-key",
			WebhookAuthKey: "webhook-key",
		},
	}
}

func TestNewGateways_BuildsWhatIsConfigured(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	for name, tc := range map[string]struct {
		edit func(*config.Config)
		want []string
	}{
		"both":          {func(*config.Config) {}, []string{"payfast", "snapscan"}},
		"payfast only":  {func(c *config.Config) { c.SnapScan = config.SnapScan{} }, []string{"payfast"}},
		"snapscan only": {func(c *config.Config) { c.PayFast = config.PayFast{} }, []string{"snapscan"}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := gatewayConfig()
			tc.edit(&cfg)

			r, err := newGateways(cfg, log)
			if err != nil {
				t.Fatalf("newGateways: %v", err)
			}
			var got []string
			for _, g := range r.All() {
				got = append(got, g.Name())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("gateways = %q, want %q", got, tc.want)
			}
		})
	}
}

// A store that cannot take a payment should not start. The alternative is a shop
// that serves a catalog perfectly and fails at the one moment that matters.
func TestNewGateways_RefusesNoGatewayAtAll(t *testing.T) {
	cfg := gatewayConfig()
	cfg.PayFast = config.PayFast{}
	cfg.SnapScan = config.SnapScan{}

	if _, err := newGateways(cfg, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("newGateways accepted a store with no payment gateway")
	}
}

// Both providers settle in ZAR only. Discovering that at the first checkout,
// after an order row already exists, is worse than discovering it at boot.
func TestNewGateways_RefusesAMismatchedCurrency(t *testing.T) {
	for name, edit := range map[string]func(*config.Config){
		"payfast":  func(c *config.Config) { c.SnapScan = config.SnapScan{} },
		"snapscan": func(c *config.Config) { c.PayFast = config.PayFast{} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := gatewayConfig()
			edit(&cfg)
			cfg.Currency = "USD"

			_, err := newGateways(cfg, slog.New(slog.DiscardHandler))
			if err == nil {
				t.Fatal("newGateways accepted a currency no gateway settles in")
			}
			if !strings.Contains(err.Error(), "USD") {
				t.Errorf("the error does not name the configured currency: %v", err)
			}
		})
	}
}

// The CSP is derived from the gateways rather than configured, because a missing
// origin is refused by the browser and nowhere else — no test that does not run
// one can see it. This asserts the derivation; the browser step in the plan
// asserts the effect.
func TestNewGateways_DeclareTheirCSPOrigins(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	both, err := newGateways(gatewayConfig(), log)
	if err != nil {
		t.Fatalf("newGateways: %v", err)
	}
	forms, images := both.CSP()
	if !slices.Contains(forms, "https://sandbox.payfast.co.za") {
		t.Errorf("form-action = %q, want PayFast's origin: the hand-over is a cross-origin form post", forms)
	}
	if !slices.Contains(images, "https://pos.snapscan.io") {
		t.Errorf("img-src = %q, want SnapScan's origin: the QR code is loaded from it", images)
	}
	for _, src := range slices.Concat(forms, images) {
		// A CSP source carrying a path matches that path exactly and refuses
		// everything beneath it — the bug that cost this project an afternoon
		// over the image bucket.
		if strings.Count(src, "/") != 2 {
			t.Errorf("CSP source %q carries a path", src)
		}
	}

	// And a store without SnapScan adds nothing to img-src, so the policy stays as
	// tight as its deployment allows.
	cfg := gatewayConfig()
	cfg.SnapScan = config.SnapScan{}
	payfastOnly, err := newGateways(cfg, log)
	if err != nil {
		t.Fatalf("newGateways: %v", err)
	}
	if _, images := payfastOnly.CSP(); len(images) != 0 {
		t.Errorf("img-src = %q for a store with no QR gateway", images)
	}
}

func TestCheckout_FormActionCSP(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	const process = "https://www.payfast.co.za"
	const hosted = "https://payment.payfast.io"
	const sandbox = "https://sandbox.payfast.co.za"

	for _, tc := range []struct {
		name     string
		sandbox  bool
		snapscan bool
		want     []string
	}{
		{name: "live PayFast", want: []string{"'self'", process, hosted}},
		{name: "live PayFast with SnapScan", snapscan: true, want: []string{"'self'", process, hosted}},
		{name: "sandbox PayFast", sandbox: true, want: []string{"'self'", sandbox}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gatewayConfig()
			cfg.PayFast.Sandbox = tc.sandbox
			if !tc.snapscan {
				cfg.SnapScan = config.SnapScan{}
			}

			gateways, err := newGateways(cfg, log)
			if err != nil {
				t.Fatalf("newGateways: %v", err)
			}
			h, err := handler.New(handler.Deps{Config: cfg, Log: log, Gateways: gateways})
			if err != nil {
				t.Fatalf("handler.New: %v", err)
			}
			mcp := mcpserver.New(mcpserver.Deps{Log: log, BaseURL: cfg.BaseURL})
			router := routes(cfg, h, mcp, gateways, nil, nil, log)

			// Exercise the production routing and CSP wiring without a database:
			// checkout redirects an anonymous shopper with no cart cookie.
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.BaseURL+"/cart/checkout", nil))
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cart" {
				t.Fatalf("GET /cart/checkout = %d, Location %q; want 303 to /cart", rec.Code, rec.Header().Get("Location"))
			}

			// Chromium enforces form-action on the redirect, not just the POST.
			// Check exact source tokens so a look-alike or a wildcard cannot pass.
			var formActions [][]string
			for _, header := range rec.Header().Values("Content-Security-Policy") {
				for directive := range strings.SplitSeq(header, ";") {
					fields := strings.Fields(directive)
					if len(fields) > 0 && fields[0] == "form-action" {
						formActions = append(formActions, fields[1:])
					}
				}
			}
			if len(formActions) != 1 || !slices.Equal(formActions[0], tc.want) {
				t.Errorf("form-action = %q, want exactly %q", formActions, tc.want)
			}
		})
	}
}
