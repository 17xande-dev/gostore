// Package config loads all runtime configuration from the environment.
//
// Everything the server needs comes from env vars, so the same binary and image
// run unchanged on a VM, in Compose, or on a managed container platform.
package config

import (
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/env"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/ratelimit/clientip"
	"github.com/17xande-dev/websec/csrf"
)

// Config is the fully resolved configuration for one server process.
type Config struct {
	// Server
	Port            string
	BaseURL         string
	ShutdownTimeout time.Duration

	// Storage
	DatabaseURL env.Secret

	// Store presentation. No organisation-specific defaults live in the code;
	// adopters set these.
	StoreName string
	Currency  string

	// TemplateDir, when set, overlays same-named templates from disk over the
	// embedded defaults, so adopters can restyle without forking.
	TemplateDir string

	// StaticDir is TemplateDir's counterpart for assets: a file there shadows a
	// bundled one of the same name, and a new name is served too. Replacing the logo
	// is dropping a logo.svg into it — restyling without a way to supply an image
	// would be half a feature.
	StaticDir string

	// ThemeReload re-reads TemplateDir and StaticDir on every request instead of
	// once at startup, so editing a theme file and refreshing the page is enough to
	// see it. It is for writing a theme and nothing else: it costs a parse of every
	// template and a read of every asset per request, and it turns a typo in a
	// template into a 500 at request time rather than a boot failure. Off unless
	// THEME_RELOAD says otherwise; the compose stack sets it.
	ThemeReload bool

	// SessionTTL is how long a sign-in lasts. A session is a row in
	// admin_sessions and a cookie carrying a random token; there is no secret to
	// configure, because there is nothing signed to verify — see internal/auth.
	SessionTTL time.Duration

	// SetupToken is the one-time token that may claim the first administrator
	// account, supplied rather than generated.
	//
	// It exists for an automated deploy, which cannot read a token out of a log
	// line and paste it into a form. Empty is the ordinary case: the server
	// generates one on first boot against an empty admin_users and prints it.
	// Either way it is stored only as a hash and is spent by the first claim.
	SetupToken env.Secret

	// PayFast and SnapScan are the payment gateways' configuration. They are flat
	// structs here rather than the gateway packages' own Configs so that config
	// depends on no gateway: main assembles them, which is also where a gateway is
	// chosen.
	//
	// Either may be left unset, and at least one must be set — a store that cannot
	// take a payment is not a store. Both set means the shopper picks at checkout.
	PayFast  PayFast
	SnapScan SnapScan

	// SMTP is how transactional mail leaves. It is required, unless Graph is
	// configured instead — see the refusal in Load, and the reason recorded
	// there.
	SMTP SMTP

	// Graph, when configured, sends mail through the Microsoft Graph API
	// instead of SMTP. It takes precedence over SMTP when both are set — see
	// newMailer in main.go.
	Graph Graph

	// OrderNotifyEmail is where a copy of each paid order goes — whoever packs the
	// parcel. Empty means the customer's confirmation is the only mail sent, and
	// the operator finds orders in /admin/orders instead.
	OrderNotifyEmail string
	// EmailQueueKey encrypts pending receipts, including their download tokens.
	// Keep it across restarts and with backups until the queue is empty.
	EmailQueueKey env.Secret

	// Blob is object storage for product images.
	Blob Blob

	// ImageDir stores product images in a local directory served by this server,
	// for a shop that wants no object storage at all. Mutually exclusive with Blob:
	// two configured backends would leave "which one wins" to be guessed.
	//
	// A product image is only ever a bucket object or a file here. Pasting a URL
	// from the general internet used to be allowed and no longer is: those bytes
	// belong to somebody else, who can change or delete them, and a product page
	// with a broken image is worse than one with none.
	ImageDir string

	// Downloads is PRIVATE object storage for the files a digital product is made
	// of. Separate from Blob, and it has to be: the image bucket is publicly
	// readable by design, so a paid file in it would be one URL guess away from
	// everybody. A private prefix inside a public bucket is not an option either,
	// because public access on GCS and R2 is a whole-bucket toggle.
	Downloads Downloads

	// DownloadDir is the local-directory alternative, on the same terms as
	// ImageDir. Mutually exclusive with Downloads, and it must not be the
	// directory images are served from — that one is published at /images/, which
	// would hand every purchased file to anybody who guessed a key.
	DownloadDir string

	// DownloadMaxBytes caps an uploaded file. Audio and video, so the default is
	// gigabytes rather than the megabytes an image gets. The upload streams
	// straight to storage, so this is about what a shop should store and how long
	// a request may hold open, not about memory.
	DownloadMaxBytes int64

	// RateLimits are the per-IP limits on the three surfaces worth protecting.
	// Defaults are deliberately loose enough that no real shopper or operator
	// meets one — a limit that fires on ordinary use gets turned off.
	RateLimits RateLimits

	// CartTTLDays is how long an untouched cart survives before the cleanup job
	// removes it, keeping the carts table bounded.
	CartTTLDays int

	// ClientIPSource says where the address a request came from is read from:
	// the connection itself, X-Forwarded-For, or Cloudflare's CF-Connecting-IP.
	// It describes what is actually in front of the server, and naming a header
	// nothing is setting lets a client claim any IP it likes — which the payment
	// callback's source-IP check and the per-IP rate limits both rely on. See
	// clientip.ParseSource for what each value means.
	ClientIPSource clientip.Source

	// CookieSecure is derived from BaseURL rather than configured separately:
	// an HTTPS deployment always wants Secure cookies, and localhost
	// development cannot use them.
	CookieSecure bool

	// ShowErrorDetail puts the underlying error on the error page, instead of only
	// a reference to find in the logs. Derived from the same signal as
	// CookieSecure, deliberately: "is this production" should have one answer
	// rather than three, and this is already the question HSTS and the CSRF
	// cookie's TLS mode are decided by.
	//
	// The detail is the Go error string, never a stack trace. That string names
	// tables, columns and constraints, which is reconnaissance for anybody probing
	// the store, so it is off the moment BaseURL is https — and an http deployment
	// is one this project already treats as not-production, since it gets neither
	// Secure cookies nor HSTS.
	ShowErrorDetail bool

	// EmbedOrigins are the origins allowed to fetch the read-only catalog
	// fragments cross-origin, for dropping the catalog into a page hosted
	// elsewhere. Empty means no CORS headers at all, which is the right default
	// for a store that is only ever browsed on its own domain.
	EmbedOrigins []string

	// FontOrigins are the origins a web font may be loaded from besides this one.
	// Empty — the default — means the CSP stays closed and the theme uses the
	// system font stack.
	//
	// This opens *two* CSP directives, which is worth knowing before setting it: a
	// hosted font service serves a stylesheet that declares the fonts and then the
	// font files that stylesheet points at, so the origins land in both style-src
	// and font-src. Allowing the fonts without the stylesheet declaring them
	// half-works, which is a slow thing to diagnose. It does not open script-src:
	// a font service still cannot run JavaScript on the checkout page, which is
	// the property that makes this a narrow widening rather than a general one.
	FontOrigins []string

	// FontCSSURL is a stylesheet the default layout links from its <head>, for the
	// hosted-font case where the service gives you a CSS URL — an Adobe Fonts kit,
	// typically. Empty means no such link is rendered at all.
	//
	// Its origin must appear in FontOrigins, checked at boot: a link the CSP then
	// blocks fails silently apart from a console warning, and the page renders in
	// the fallback font with nothing to suggest why.
	//
	// Deliberately a CSS URL and not a script: a font service's JavaScript loader
	// needs script-src widened, connect-src opened for its config fetch, and a
	// nonce for the inline snippet and the inline <style> it injects. That is a far
	// larger concession than this one, and this project does not offer it.
	FontCSSURL string

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// LogFormat is "json" or "gcp". Both are JSON on stdout; "gcp" renames two
	// keys — level to severity, msg to message — because that is what Google Cloud
	// Logging reads. Without it every line files as DEFAULT severity, so
	// `severity>=ERROR` matches nothing and alerting on the error rate silently
	// never fires.
	//
	// Opt-in rather than automatic: the rename is a Google convention, and this
	// project does not assume anybody's platform.
	LogFormat string
}

// MinSetupTokenLen is the shortest SETUP_TOKEN accepted. The token is, for as
// long as it is unclaimed, the credential for the whole admin area, so it is held
// to the length 32 random bytes reach in base64 rather than to anything a person
// would type.
const MinSetupTokenLen = 32

// payFastSandboxMerchantID is the merchant id PayFast publishes in its own
// documentation for testing, and therefore the one every copy of this project's
// .env.example and compose.yaml carries. It is a constant here so that "still on
// the demo credentials" is something the config can recognise.
const payFastSandboxMerchantID = "10000100"

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
	MerchantKey env.Secret
	Passphrase  env.Secret
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

// Configured reports whether PayFast is switched on. The merchant id is the
// signal, and the key is then required — half a credential is a configuration
// mistake, not a decision to run without the gateway.
func (p PayFast) Configured() bool { return p.MerchantID != "" }

// SnapScan is what the SnapScan gateway needs from the environment.
//
// SnapScan is South Africa's QR payment app: the shopper opens a URL this store
// builds, on a phone through the app or on a desktop by scanning the QR code it
// renders to.
//
// Two absences are deliberate. There is no sandbox setting, because SnapScan has
// no sandbox — configuring this at all means real money, which is the reverse of
// PAYFAST_SANDBOX's safe default. And there is no notify URL, because SnapScan's
// support configures the webhook address on the merchant account rather than
// reading it from each payment; it must point at BASE_URL + the callback route,
// which on a development machine means a tunnel.
type SnapScan struct {
	// SnapCode identifies the merchant. Its presence is what enables the gateway.
	SnapCode string
	// APIKey reads payments back from SnapScan's merchant API, which is how a
	// notification is confirmed.
	APIKey env.Secret
	// WebhookAuthKey is the shared secret a notification's HMAC is computed with.
	// Required whenever the gateway is enabled: without it, nothing about a
	// notification can be checked, and the callback route is the one thing that
	// can mark an order paid.
	WebhookAuthKey env.Secret
	// ValidationKey enables the Secure QR Payload signature, which SnapScan
	// switches on per account on request. Optional — see snapscan.Config.
	ValidationKey env.Secret
}

// Configured reports whether SnapScan is switched on.
func (s SnapScan) Configured() bool { return s.SnapCode != "" }

// Blob is object storage for product images, against anything speaking the S3
// API — Cloudflare R2, Google Cloud Storage in interoperability mode, or a self-hosted server such as RustFS.
//
// PublicBaseURL is separate from Endpoint and cannot be derived from it: the
// address a bucket is written through and the address it is read from are
// routinely different — R2 writes to <account>.r2.cloudflarestorage.com and reads
// from a custom domain — and only the operator knows the second one.
type Blob struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey env.Secret

	// Region is "auto" for R2. GCS and RustFS ignore it.
	Region string
	UseTLS bool

	PublicBaseURL string
}

// Configured reports whether object storage is set up.
func (b Blob) Configured() bool { return b.Endpoint != "" }

// PublicOrigin is the scheme and host images are served from, with any path
// stripped — which is what a Content-Security-Policy source has to be.
//
// This is not fussiness. A CSP source whose path does not end in "/" must match a
// URL's path *exactly*, so listing "http://host:9000/bucket" permits that one URL
// and refuses "http://host:9000/bucket/products/x.jpg" — every actual image. RustFS
// and any path-style bucket URL hit this, and the failure is invisible outside a
// browser: the image returns 200 to curl, the markup is correct, and the page shows
// a broken image with a console warning nothing else surfaces.
func (b Blob) PublicOrigin() string {
	u, err := url.Parse(b.PublicBaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Load has already rejected this, so reaching here means the value changed
		// underneath us. An empty source is safe: it permits nothing.
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Downloads is private object storage for purchased files.
//
// It has no PublicBaseURL, and the absence is the design. These objects are never
// addressed publicly: a buyer's link points at this server, which checks the
// entitlement, records the click and only then produces a short-lived signed URL.
// A base URL here would be a standing invitation to build a permanent one.
type Downloads struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey env.Secret

	Region string
	UseTLS bool

	// PublicEndpoint is the address a *browser* reaches this bucket at, when that
	// differs from Endpoint. Empty means they are the same, which is the case in
	// every real deployment; it exists for a development stack where the server
	// and the browser are on different sides of a container network.
	//
	// A presigned URL's signature covers the Host header, so a URL signed for one
	// address cannot be rewritten to another — it has to be signed for the
	// browser's address from the start. Same idea as Blob.PublicBaseURL, one layer
	// down.
	PublicEndpoint string
}

// Configured reports whether download storage is set up.
func (d Downloads) Configured() bool { return d.Endpoint != "" }

// DownloadsEnabled reports whether this shop can sell digital products at all.
// With neither backend configured the admin says so rather than offering an
// upload form that could only fail.
func (c Config) DownloadsEnabled() bool { return c.Downloads.Configured() || c.DownloadDir != "" }

// ImagesEnabled reports whether a product can have an image at all — by upload to
// object storage or to a local directory. With neither, the admin says so rather
// than offering a form that could only fail.
func (c Config) ImagesEnabled() bool { return c.Blob.Configured() || c.ImageDir != "" }

// RateLimits holds the per-IP limits. Each is a number of requests per minute
// with a burst; see middleware.RateLimit for what the two mean together.
type RateLimits struct {
	// LoginPerMinute guards the admin password against brute force. Low, because
	// an operator signs in once.
	LoginPerMinute int
	// CheckoutPerMinute guards order creation. Loose, because refusing a real
	// shopper costs a sale and double-clicking is normal.
	CheckoutPerMinute int
	// CallbackPerMinute guards the payment callback, which is unauthenticated and
	// makes the store POST to the gateway for every request it accepts. Generous:
	// a throttled notification is retried, but throttling a busy shop's genuine
	// traffic delays real payments.
	CallbackPerMinute int
	// StatusPerMinute guards the checkout's payment-status poll, which a QR
	// hand-over page asks for every few seconds while the shopper pays on their
	// phone. It reads one order by the cart cookie and costs a single indexed
	// query, so the allowance is roughly "twice what an open page asks for".
	StatusPerMinute int
	// DownloadPerMinute guards the buyer's download links, which mint a signed URL
	// per click. Generous on purpose: somebody working through a conference
	// recording's twenty files is ordinary use, and a limit that fires on it would
	// be switched off rather than tuned.
	DownloadPerMinute int
	// MCPPerMinute guards the MCP endpoint, per client IP, before the bearer
	// token is checked — so it bounds guessing as well as a runaway agent. An
	// assistant working through a catalog makes a call every second or two;
	// the allowance is about twice that.
	MCPPerMinute int
}

// SMTP is the mail relay's configuration. Username and Password may be empty, for
// a relay that authenticates by network address — mailpit in development being the
// case that matters here.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password env.Secret

	// From is the sender address. A relay usually rejects a From it does not
	// consider itself responsible for, so this has to be on a domain it accepts.
	From    string
	ReplyTo string

	// TLS is "starttls" (the default, correct for port 587), "tls" (implicit, for
	// 465) or "none" (development only).
	TLS string

	// OAuth, when configured, replaces Password with an XOAUTH2 access token
	// fetched per send. It is how a Microsoft Exchange Online mailbox is reached
	// now that Basic Auth for SMTP client submission is going away.
	OAuth SMTPOAuth
}

// SMTPOAuth is an Entra ID app registration, used to obtain an access token for
// XOAUTH2. All three parts are needed or none of them are.
type SMTPOAuth struct {
	TenantID     string
	ClientID     string
	ClientSecret env.Secret
}

// Configured reports whether a token can be obtained.
func (o SMTPOAuth) Configured() bool {
	return o.TenantID != "" && o.ClientID != "" && o.ClientSecret.IsSet()
}

// Configured reports whether mail can actually be sent. Both a host and a From
// address are needed: a relay with no sender is not a working configuration, and
// half-configured is the case worth catching at startup.
func (s SMTP) Configured() bool { return s.Host != "" && s.From != "" }

// Graph is an Entra ID app registration used to send mail through the
// Microsoft Graph API instead of SMTP. It is a separate path from SMTP.OAuth:
// the two need different app permissions (Mail.Send versus SMTP.SendAsApp) and
// different token audiences, and are not interchangeable.
type Graph struct {
	TenantID     string
	ClientID     string
	ClientSecret env.Secret
	// From is the mailbox to send as. Kept separate from SMTP.From rather than
	// shared, since a deployment choosing Graph has no reason to have set SMTP
	// up at all.
	From string
}

// Configured reports whether Graph can actually be used. All four are needed:
// three for the token and From for the mailbox, and half-configured is the case
// worth catching at startup rather than at the first order.
func (g Graph) Configured() bool {
	return g.TenantID != "" && g.ClientID != "" && g.ClientSecret.IsSet() && g.From != ""
}

// AllowsEmbedding reports whether any origin may fetch the catalog fragments.
func (c Config) AllowsEmbedding() bool { return len(c.EmbedOrigins) > 0 }

// SetupTokenValue returns the token as the setup flow has always accepted it:
// surrounding whitespace is ignored. The config field itself stays redacted.
func (c Config) SetupTokenValue() string { return strings.TrimSpace(c.SetupToken.Reveal()) }

// Load reads configuration from the environment, applying defaults and
// returning an error listing every missing or malformed required value.
func Load() (Config, error) { return load(os.LookupEnv) }

func load(lookup env.Lookup) (Config, error) {
	r := env.New(lookup)
	from := strings.TrimSpace(r.String("EMAIL_FROM", ""))
	// Resolve credentials before validation so a file error is diagnosed even
	// when other settings are malformed.
	c := Config{
		Port:             r.String("PORT", "8080"),
		BaseURL:          strings.TrimRight(r.String("BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:      r.Secret("DATABASE_URL"),
		StoreName:        r.String("STORE_NAME", "gostore"),
		Currency:         r.String("CURRENCY", "ZAR"),
		TemplateDir:      r.String("TEMPLATE_DIR", ""),
		StaticDir:        strings.TrimSpace(r.String("STATIC_DIR", "")),
		SetupToken:       r.Secret("SETUP_TOKEN"),
		OrderNotifyEmail: strings.TrimSpace(r.String("ORDER_NOTIFY_EMAIL", "")),
		EmailQueueKey:    r.Secret("EMAIL_QUEUE_KEY"),
		ImageDir:         strings.TrimSpace(r.String("IMAGE_DIR", "")),
		Blob: Blob{
			Endpoint:      strings.TrimSpace(r.String("BLOB_ENDPOINT", "")),
			Bucket:        strings.TrimSpace(r.String("BLOB_BUCKET", "")),
			AccessKey:     r.String("BLOB_ACCESS_KEY_ID", ""),
			SecretKey:     r.Secret("BLOB_SECRET_ACCESS_KEY"),
			Region:        r.String("BLOB_REGION", "auto"),
			PublicBaseURL: strings.TrimRight(strings.TrimSpace(r.String("BLOB_PUBLIC_BASE_URL", "")), "/"),
		},
		SMTP: SMTP{
			Host:     strings.TrimSpace(r.String("SMTP_HOST", "")),
			Username: r.String("SMTP_USERNAME", ""),
			Password: r.Secret("SMTP_PASSWORD"),
			From:     from,
			ReplyTo:  strings.TrimSpace(r.String("EMAIL_REPLY_TO", "")),
			TLS:      r.String("SMTP_TLS", "starttls"),
			OAuth: SMTPOAuth{
				TenantID:     strings.TrimSpace(r.String("SMTP_OAUTH_TENANT_ID", "")),
				ClientID:     strings.TrimSpace(r.String("SMTP_OAUTH_CLIENT_ID", "")),
				ClientSecret: r.Secret("SMTP_OAUTH_CLIENT_SECRET"),
			},
		},
		Graph: Graph{
			TenantID:     strings.TrimSpace(r.String("GRAPH_TENANT_ID", "")),
			ClientID:     strings.TrimSpace(r.String("GRAPH_CLIENT_ID", "")),
			ClientSecret: r.Secret("GRAPH_CLIENT_SECRET"),
			From:         from,
		},
		PayFast: PayFast{
			MerchantID:  r.String("PAYFAST_MERCHANT_ID", ""),
			MerchantKey: r.Secret("PAYFAST_MERCHANT_KEY"),
			Passphrase:  r.Secret("PAYFAST_PASSPHRASE"),
			NotifyURL:   strings.TrimSpace(r.String("PAYFAST_NOTIFY_URL", "")),
		},
		SnapScan: SnapScan{
			SnapCode:       strings.TrimSpace(r.String("SNAPSCAN_SNAP_CODE", "")),
			APIKey:         r.Secret("SNAPSCAN_API_KEY"),
			WebhookAuthKey: r.Secret("SNAPSCAN_WEBHOOK_AUTH_KEY"),
			ValidationKey:  r.Secret("SNAPSCAN_VALIDATION_KEY"),
		},
	}
	loadDownloads(r, &c)
	c.ThemeReload = r.Bool("THEME_RELOAD", false)
	c.Blob.UseTLS = r.Bool("BLOB_USE_TLS", true)
	// Default to sandbox so a fresh install cannot take real money by accident.
	c.PayFast.Sandbox = r.Bool("PAYFAST_SANDBOX", true)
	c.LogLevel = r.OneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error")
	c.LogFormat = r.OneOf("LOG_FORMAT", "json", "json", "gcp")
	c.SessionTTL = time.Duration(r.Int("SESSION_TTL_HOURS", 24, 1, math.MaxInt)) * time.Hour
	c.ShutdownTimeout = time.Duration(r.Int("SHUTDOWN_TIMEOUT_SECONDS", 15, 0, math.MaxInt)) * time.Second
	// Only an explicit zero switches a limit off.
	c.RateLimits = RateLimits{
		LoginPerMinute:    r.Int("RATE_LIMIT_LOGIN_PER_MINUTE", 10, 0, math.MaxInt),
		CheckoutPerMinute: r.Int("RATE_LIMIT_CHECKOUT_PER_MINUTE", 20, 0, math.MaxInt),
		CallbackPerMinute: r.Int("RATE_LIMIT_CALLBACK_PER_MINUTE", 120, 0, math.MaxInt),
		StatusPerMinute:   r.Int("RATE_LIMIT_STATUS_PER_MINUTE", 30, 0, math.MaxInt),
		DownloadPerMinute: r.Int("RATE_LIMIT_DOWNLOAD_PER_MINUTE", 60, 0, math.MaxInt),
		MCPPerMinute:      r.Int("RATE_LIMIT_MCP_PER_MINUTE", 120, 0, math.MaxInt),
	}
	c.CartTTLDays = r.Int("CART_TTL_DAYS", 60, 1, math.MaxInt)
	c.SMTP.Port = r.Int("SMTP_PORT", 587, 1, 65535)
	c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")
	// One signal, three uses: Secure cookies, HSTS, and whether an error page may
	// say what actually went wrong.
	c.ShowErrorDetail = !c.CookieSecure

	if !c.DatabaseURL.IsSet() {
		r.Fail("DATABASE_URL", "is required (or set DATABASE_URL_FILE)")
	}
	// These settings historically trimmed whitespace on top of the file
	// reader's CR/LF stripping. Keep that normalization at validation and use.
	if queueKey := strings.TrimSpace(c.EmailQueueKey.Reveal()); queueKey == "" {
		r.Fail("EMAIL_QUEUE_KEY", "is required (or set EMAIL_QUEUE_KEY_FILE)")
	} else if key, err := hex.DecodeString(queueKey); err != nil || len(key) != 32 {
		r.Fail("EMAIL_QUEUE_KEY", "must be 64 hexadecimal characters; generate with openssl rand -hex 32")
	}
	// Each gateway's credentials are required only when that gateway is switched
	// on, so a SnapScan-only store needs no PayFast account and the reverse.
	if c.PayFast.Configured() && !c.PayFast.MerchantKey.IsSet() {
		r.Fail("PAYFAST_MERCHANT_KEY", "is required when PAYFAST_MERCHANT_ID is set")
	}
	if c.SnapScan.Configured() {
		if strings.TrimSpace(c.SnapScan.APIKey.Reveal()) == "" {
			r.Fail("SNAPSCAN_API_KEY", "is required when SNAPSCAN_SNAP_CODE is set")
		}
		// Not optional. A notification with nothing to check its signature
		// against is an unauthenticated request that can mark orders paid, and
		// the callback route is unauthenticated by definition — a payment
		// provider cannot be given a session or a CSRF token.
		if strings.TrimSpace(c.SnapScan.WebhookAuthKey.Reveal()) == "" {
			r.Fail("SNAPSCAN_WEBHOOK_AUTH_KEY", "is required when SNAPSCAN_SNAP_CODE is set")
		}
	}

	// A store that cannot take a payment would fail at the first checkout.
	if !c.PayFast.Configured() && !c.SnapScan.Configured() {
		r.Fail("PAYFAST_MERCHANT_ID", "no payment gateway is configured: "+
			"set PAYFAST_MERCHANT_ID (and PAYFAST_MERCHANT_KEY), or SNAPSCAN_SNAP_CODE "+
			"(and SNAPSCAN_API_KEY, SNAPSCAN_WEBHOOK_AUTH_KEY), or both")
	}

	// BASE_URL is also the site's own origin for the CSRF check, which compares it
	// byte for byte with the Origin a browser sends. A value the browser could
	// never send (a path, upper case, a default port) would make every form fail
	// as cross-origin, so it stops startup here, where -check-config reports it.
	if err := csrf.ValidateOrigin(c.BaseURL, !c.CookieSecure); err != nil {
		r.Fail("BASE_URL", "cannot be the site's origin; want lower-case scheme://host[:port] with no path")
	}

	// There is no admin credential in the environment any more: accounts live in
	// admin_users and the first one is claimed at /admin/setup. What can be
	// supplied is the token that authorises that claim, and a short one is a
	// guessable credential for the whole admin area — so it is bounded here,
	// where the message can say so, rather than accepted and regretted.
	if token := c.SetupTokenValue(); token != "" && len(token) < MinSetupTokenLen {
		r.Fail("SETUP_TOKEN", fmt.Sprintf("want at least %d characters (generate one with `openssl rand -base64 32`)", MinSetupTokenLen))
	}

	// TRUST_PROXY_IP was a bool meaning "believe X-Forwarded-For". It cannot
	// describe a Cloudflare deployment, where the trustworthy address is in
	// CF-Connecting-IP and X-Forwarded-For is appended to rather than replaced.
	//
	// Refused rather than ignored: silently falling back to the default would
	// leave a deployment that had set it reading RemoteAddr instead — which is
	// the proxy — so every shopper would share one rate-limit bucket and the
	// payment callback's source-IP check would reject every genuine
	// notification. Both fail quietly, which is what a boot refusal is for.
	if _, ok := os.LookupEnv("TRUST_PROXY_IP"); ok {
		r.Fail("TRUST_PROXY_IP", "has been replaced by CLIENT_IP_SOURCE — "+
			"use CLIENT_IP_SOURCE=forwarded for what TRUST_PROXY_IP=true did, "+
			"CLIENT_IP_SOURCE=cloudflare behind Cloudflare, or unset it for remote")
	}
	clientIPSource, sourceErr := clientip.ParseSource(r.String("CLIENT_IP_SOURCE", "remote"))
	if sourceErr != nil {
		r.Fail("CLIENT_IP_SOURCE", "must be remote, forwarded, or cloudflare")
	}
	c.ClientIPSource = clientIPSource

	// Taking real money with the demo credentials is a contradiction, and it is
	// the mistake that follows naturally from the other one: somebody discovers
	// their site has been quietly running against the sandbox, sets
	// PAYFAST_SANDBOX=false, and does not realise the merchant id came from
	// .env.example too. Every payment would then be signed with a key published
	// in PayFast's own documentation.
	//
	// Refused at boot, where it costs one message, rather than at the first
	// checkout, where it costs a customer.
	if c.PayFast.Configured() && !c.PayFast.Sandbox && c.PayFast.MerchantID == payFastSandboxMerchantID {
		r.Fail("PAYFAST_SANDBOX", "cannot be false with PAYFAST_MERCHANT_ID set to PayFast's published sandbox merchant id — "+
			"set your own credentials, or leave PAYFAST_SANDBOX=true")
	}

	// "*" is allowed here and nowhere else: the fragments these origins may fetch
	// are cookie-free and read-only, so a permissive list cannot become a way to
	// act as somebody.
	c.EmbedOrigins = parseOrigins(r, "EMBED_ORIGINS", true)

	// Whereas a wildcard font source would let any origin serve a stylesheet to
	// every page of the store, the checkout included — the opposite of what this
	// directive is for. So: list them.
	c.FontOrigins = parseOrigins(r, "FONT_ORIGINS", false)

	if c.FontCSSURL = strings.TrimSpace(r.String("FONT_CSS_URL", "")); c.FontCSSURL != "" {
		u, err := url.Parse(c.FontCSSURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			r.Fail("FONT_CSS_URL", "must be an absolute URL")
		} else if !slices.Contains(c.FontOrigins, u.Scheme+"://"+u.Host) {
			// The browser would block the stylesheet and only show a console warning.
			r.Fail("FONT_CSS_URL", "its origin must be listed in FONT_ORIGINS or the CSP will block it")
		}
	}

	// Mail is validated whenever any of it is set, so a half-configured relay is a
	// boot failure rather than a receipt that silently never arrives.
	if _, err := mailer.ParseTLSPolicy(c.SMTP.TLS); err != nil {
		r.Fail("SMTP_TLS", "must be starttls, tls, or none")
	}
	// From set without Host is not an error on its own: it is what a Graph-only
	// deployment looks like, since EMAIL_FROM serves both transports. Host
	// without From is still a mistake — a relay with no sender is not a working
	// configuration.
	if c.SMTP.Host != "" && c.SMTP.From == "" {
		r.Fail("EMAIL_FROM", "is required when SMTP_HOST is set")
	}
	// Graph is all or nothing, for the same reason XOAUTH2 is below: a
	// half-configured app registration would boot, then fail to send on the
	// first paid order. Checked before the general mail requirement below, so a
	// deployment that got partway through setting Graph up gets the specific
	// error rather than the generic "nothing is configured" one.
	graph := c.Graph
	if !graph.Configured() && (graph.TenantID != "" || graph.ClientID != "" || graph.ClientSecret.IsSet()) {
		r.Fail("GRAPH_CLIENT_SECRET", "GRAPH_TENANT_ID, GRAPH_CLIENT_ID, GRAPH_CLIENT_SECRET and EMAIL_FROM "+
			"must all be set to send through Graph")
	}

	// Mail is REQUIRED, and this reverses an earlier decision that it was optional.
	// The reason it changed is a fact that did not exist when it was made: a digital
	// download reaches its buyer as a link in the confirmation email, and only the
	// token's hash is stored — so a shop with no mail server does not merely drop a
	// receipt, it takes money for a file the buyer can then never reach, with
	// nothing recoverable afterwards.
	//
	// The old argument — "a shop's job is to take an order and record it, and that
	// does not depend on a mail server" — was right about a shop selling parcels
	// and is wrong about one that can sell downloads. Since any deployment *might*
	// sell one, the requirement is unconditional rather than tied to whether a
	// digital product happens to exist today. Graph satisfies it just as SMTP does.
	if !c.SMTP.Configured() && !c.Graph.Configured() {
		r.Fail("SMTP_HOST", "SMTP_HOST and EMAIL_FROM (or GRAPH_TENANT_ID, GRAPH_CLIENT_ID, "+
			"GRAPH_CLIENT_SECRET and EMAIL_FROM) are required — a store must be able "+
			"to send a receipt, and a digital download's link exists nowhere else")
	}
	if c.OrderNotifyEmail != "" && !c.SMTP.Configured() && !c.Graph.Configured() {
		r.Fail("ORDER_NOTIFY_EMAIL", "is set but no mail transport is configured, "+
			"so the notification could never be sent")
	}

	// XOAUTH2 is all or nothing. A half-configured app registration would boot,
	// then fail to authenticate against Exchange on the first paid order — which
	// is the worst possible moment to discover a missing environment variable.
	oauth := c.SMTP.OAuth
	if !oauth.Configured() && (oauth.TenantID != "" || oauth.ClientID != "" || oauth.ClientSecret.IsSet()) {
		r.Fail("SMTP_OAUTH_CLIENT_SECRET", "SMTP_OAUTH_TENANT_ID, SMTP_OAUTH_CLIENT_ID and SMTP_OAUTH_CLIENT_SECRET "+
			"must be set together")
	}
	if oauth.Configured() {
		// XOAUTH2 authenticates as a named mailbox, so the username is not
		// optional the way it is for a relay that authenticates by address.
		if c.SMTP.Username == "" {
			r.Fail("SMTP_USERNAME", "is required with SMTP OAuth — XOAUTH2 authenticates "+
				"as a named mailbox")
		}
		// Refused rather than resolved by precedence. Both being set means
		// somebody has a belief about which one is in use, and a silent winner
		// would leave a stale secret sitting in the environment looking live.
		if c.SMTP.Password.IsSet() {
			r.Fail("SMTP_PASSWORD", "SMTP_PASSWORD and SMTP OAuth are both set, so which one authenticates "+
				"would be a guess; unset SMTP_PASSWORD")
		}
	}

	// One image backend at most. Both configured would leave which one wins to be
	// guessed, and the guess would be wrong half the time.
	if c.Blob.Configured() && c.ImageDir != "" {
		r.Fail("BLOB_ENDPOINT", "BLOB_ENDPOINT and IMAGE_DIR are both set; product images come from one or the other")
	}
	// And at least one. Otherwise the admin's upload form has to either exist or
	// explain itself on every product page — a half-feature carried by every
	// deployment that forgot a variable.
	//
	// The bar is deliberately low: IMAGE_DIR is one path and needs nothing running.
	// Refusing to boot over a setting that cheap costs an adopter a line of config
	// and saves them a storefront that looks broken.
	if !c.ImagesEnabled() {
		r.Fail("IMAGE_DIR", "product images are required — set IMAGE_DIR for a local directory, "+
			"or the BLOB_* variables for object storage")
	}

	// Object storage is all-or-nothing: a partial configuration would fail at the
	// first upload with whichever piece is missing, which is a worse place to find
	// out than at boot.
	if c.Blob.Configured() {
		for _, f := range []struct{ name, value string }{
			{"BLOB_BUCKET", c.Blob.Bucket},
			{"BLOB_ACCESS_KEY_ID", c.Blob.AccessKey},
			{"BLOB_SECRET_ACCESS_KEY", c.Blob.SecretKey.Reveal()},
		} {
			if strings.TrimSpace(f.value) == "" {
				r.Fail(f.name, "is required when BLOB_ENDPOINT is set")
			}
		}
		if c.Blob.PublicBaseURL == "" {
			r.Fail("BLOB_PUBLIC_BASE_URL", "is required when BLOB_ENDPOINT is set")
		} else if u, err := url.Parse(c.Blob.PublicBaseURL); err != nil || u.Scheme == "" || u.Host == "" {
			r.Fail("BLOB_PUBLIC_BASE_URL", "must be an absolute URL")
		}
	} else if c.Blob.Bucket != "" || c.Blob.AccessKey != "" || c.Blob.PublicBaseURL != "" {
		r.Fail("BLOB_ENDPOINT", "BLOB_* variables are set but BLOB_ENDPOINT is not, so uploads would be off")
	}

	c.checkDownloads(r)

	// "any" is spelled out rather than being an empty list, so disabling a
	// security check is something an operator typed on purpose.
	if cidrs := strings.TrimSpace(r.String("PAYFAST_ALLOWED_CIDRS", "")); cidrs == "any" {
		c.PayFast.AllowAnySourceIP = true
	} else {
		for cidr := range strings.SplitSeq(cidrs, ",") {
			if cidr = strings.TrimSpace(cidr); cidr != "" {
				c.PayFast.AllowedCIDRs = append(c.PayFast.AllowedCIDRs, cidr)
			}
		}
	}

	if c.PayFast.NotifyURL != "" {
		u, err := url.Parse(c.PayFast.NotifyURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			r.Fail("PAYFAST_NOTIFY_URL", "must be an absolute URL")
		}
	}

	for _, d := range []struct{ key, path string }{
		{"TEMPLATE_DIR", c.TemplateDir},
		{"STATIC_DIR", c.StaticDir},
	} {
		if d.path == "" {
			continue
		}
		if fi, err := os.Stat(d.path); err != nil {
			r.Fail(d.key, "cannot access directory")
		} else if !fi.IsDir() {
			r.Fail(d.key, "must name a directory")
		}
	}

	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// parseOrigins reads a comma-separated origin list from the environment.
//
// Both lists it serves — embed origins and font origins — are compared literally
// by the browser, against the Origin header in one case and as a CSP source
// expression in the other. Neither tolerates a trailing slash or a path, so both
// are refused here where the message can say why, rather than in a browser where
// the symptom is a request that simply does not happen.
func parseOrigins(r *env.Reader, key string, allowWildcard bool) []string {
	var out []string
	for _, origin := range r.List(key, nil) {
		if origin == "*" {
			if !allowWildcard {
				r.Fail(key, "does not accept \"*\" — list the origins")
				continue
			}
			out = append(out, origin)
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			r.Fail(key, "each entry must be scheme://host[:port] with no path")
			continue
		}
		out = append(out, origin)
	}
	return out
}

// LoadTool loads the database, logging and optional seed-file storage
// settings. Migration and seed commands need no payment or mail credentials.
func LoadTool() (Config, error) {
	// Tool jobs must not require unrelated secret files to be mounted.
	r := env.New(os.LookupEnv)
	c := Config{
		DatabaseURL: r.Secret("DATABASE_URL"),
		LogLevel:    r.OneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
		LogFormat:   r.OneOf("LOG_FORMAT", "json", "json", "gcp"),
	}
	loadDownloads(r, &c)
	if !c.DatabaseURL.IsSet() {
		r.Fail("DATABASE_URL", "is required (or set DATABASE_URL_FILE)")
	}
	c.checkDownloads(r)
	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Seed fixtures can attach files to digital products, so the tool accepts the
// same download settings as the server. Fixtures without files need none.
func loadDownloads(r *env.Reader, c *Config) {
	c.DownloadDir = strings.TrimSpace(r.String("DOWNLOAD_DIR", ""))
	c.DownloadMaxBytes = parseDownloadBytes(r.String("DOWNLOAD_MAX_BYTES", ""), blob.DefaultMaxDownloadBytes)
	c.Downloads = Downloads{
		Endpoint:       strings.TrimSpace(r.String("DOWNLOAD_ENDPOINT", "")),
		Bucket:         strings.TrimSpace(r.String("DOWNLOAD_BUCKET", "")),
		AccessKey:      r.String("DOWNLOAD_ACCESS_KEY_ID", ""),
		SecretKey:      r.Secret("DOWNLOAD_SECRET_ACCESS_KEY"),
		Region:         r.String("DOWNLOAD_REGION", "auto"),
		UseTLS:         r.Bool("DOWNLOAD_USE_TLS", true),
		PublicEndpoint: strings.TrimSpace(r.String("DOWNLOAD_PUBLIC_ENDPOINT", "")),
	}
}

// checkDownloads validates the private download store, on the same all-or-nothing
// terms as the image bucket: a partial configuration would fail at the first
// upload with whichever piece is missing, which is a worse place to find out.
//
// The overlap check is the one that matters most and is unique to this store.
func (c Config) checkDownloads(r *env.Reader) {
	if c.Downloads.Configured() && c.DownloadDir != "" {
		r.Fail("DOWNLOAD_ENDPOINT", "DOWNLOAD_ENDPOINT and DOWNLOAD_DIR are both set; purchased files come from one or the other")
	}

	// The download directory must not be, or contain, or sit inside, the directory
	// images are served from. /images/ is public and unauthenticated, so an overlap
	// would publish every purchased file to anybody who guessed a key — the exact
	// failure this whole feature exists to prevent, arrived at by a configuration
	// mistake rather than a bug. Refusing at boot is the only place to catch it.
	if c.DownloadDir != "" && c.ImageDir != "" {
		dl, err1 := filepath.Abs(c.DownloadDir)
		img, err2 := filepath.Abs(c.ImageDir)
		if err1 == nil && err2 == nil && overlaps(dl, img) {
			r.Fail("DOWNLOAD_DIR", "overlaps IMAGE_DIR; everything under IMAGE_DIR is served publicly at /images/, so purchased files must not live there")
		}
	}

	if c.Downloads.Configured() {
		for _, f := range []struct{ name, value string }{
			{"DOWNLOAD_BUCKET", c.Downloads.Bucket},
			{"DOWNLOAD_ACCESS_KEY_ID", c.Downloads.AccessKey},
			{"DOWNLOAD_SECRET_ACCESS_KEY", c.Downloads.SecretKey.Reveal()},
		} {
			if strings.TrimSpace(f.value) == "" {
				r.Fail(f.name, "is required when DOWNLOAD_ENDPOINT is set")
			}
		}

		// Same endpoint and same bucket as the images means the download store is
		// the public one. Nothing downstream would notice, and every purchased file
		// would be anonymously fetchable.
		if c.Blob.Configured() &&
			strings.EqualFold(c.Downloads.Endpoint, c.Blob.Endpoint) &&
			strings.EqualFold(c.Downloads.Bucket, c.Blob.Bucket) {
			r.Fail("DOWNLOAD_BUCKET", "is the same bucket as BLOB_BUCKET, which is publicly readable; purchased files need a private bucket of their own")
		}
	} else if c.Downloads.Bucket != "" || c.Downloads.AccessKey != "" {
		r.Fail("DOWNLOAD_ENDPOINT", "DOWNLOAD_* variables are set but DOWNLOAD_ENDPOINT is not, so digital products would be off")
	}
}

// overlaps reports whether either path is the other, or is inside it.
func overlaps(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+string(filepath.Separator)) ||
		strings.HasPrefix(b, a+string(filepath.Separator))
}

// parseDownloadBytes accepts a byte count, accepting a plain number of bytes or a suffix —
// "2GiB", "500MB", "2G". Written because the alternative is an operator typing
// 2147483648 and getting a digit wrong, and because a size limit expressed in raw
// bytes in a .env file is unreadable to the person who has to change it.
//
// A malformed or non-positive value falls back to the default rather than failing
// the boot: this is a cap on an admin's own upload, and refusing to start a shop
// over it would be out of proportion. Load logs nothing here, so the check that
// catches a typo is the admin form saying a smaller number than expected.
func parseDownloadBytes(value string, def int64) int64 {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return def
	}

	mult := int64(1)
	upper := strings.ToUpper(raw)
	for _, s := range []struct {
		suffix string
		factor int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, s.suffix) {
			mult = s.factor
			upper = strings.TrimSpace(strings.TrimSuffix(upper, s.suffix))
			break
		}
	}

	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	// Overflow would turn a huge number into a negative cap, which reads as "no
	// limit" to a caller comparing against it.
	if n > math.MaxInt64/mult {
		return def
	}
	return n * mult
}
