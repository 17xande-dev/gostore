package config

import (
	"strings"
	"testing"
	"time"
)

// setRequired sets every required var to something valid, so each test can
// break exactly one of them and know which failure it is asserting on.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/gostore")
	t.Setenv("ADMIN_PASSWORD_HASH", "$2a$12$C6UzMDM.H6dfI/f/IKcEe.rLfIRhKrKcXKcXKcXKcXKcXKcXKcXKc")
	t.Setenv("SESSION_SECRET", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 bytes
}

func TestLoad_RequiresSecrets(t *testing.T) {
	// Each required var, named in the error, so a misconfigured deployment says
	// what is missing instead of failing later and less clearly.
	for _, key := range []string{"DATABASE_URL", "ADMIN_PASSWORD_HASH", "SESSION_SECRET"} {
		setRequired(t)
		t.Setenv(key, "")

		_, err := Load()
		if err == nil {
			t.Errorf("%s unset: expected an error, got nil", key)
			continue
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s unset: error %q does not name it", key, err)
		}
	}
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != "8080" {
		t.Errorf("Port = %q, want 8080", c.Port)
	}
	if c.Currency != "ZAR" {
		t.Errorf("Currency = %q, want ZAR", c.Currency)
	}
	if c.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want http://localhost:8080", c.BaseURL)
	}
	if c.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %s, want 24h", c.SessionTTL)
	}
	if len(c.SessionSecret) != 32 {
		t.Errorf("SessionSecret is %d bytes, want the 32 decoded from base64", len(c.SessionSecret))
	}
	// A plain-HTTP BaseURL cannot use Secure cookies, or local development
	// could never sign in.
	if c.CookieSecure {
		t.Error("CookieSecure is set for an http:// BaseURL")
	}
}

func TestLoad_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	setRequired(t)
	t.Setenv("BASE_URL", "https://store.example.com/")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseURL != "https://store.example.com" {
		t.Errorf("BaseURL = %q, want https://store.example.com", c.BaseURL)
	}
	// HTTPS deployments always want Secure cookies, so this is derived rather
	// than being one more thing to forget.
	if !c.CookieSecure {
		t.Error("CookieSecure is not set for an https:// BaseURL")
	}
}

func TestLoad_RejectsBadLogLevel(t *testing.T) {
	setRequired(t)
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an unknown LOG_LEVEL, got nil")
	}
}

func TestLoad_RejectsWeakSessionSecret(t *testing.T) {
	cases := map[string]string{
		"not base64":         "не-base64!!",
		"too short":          "c2hvcnQ=",                       // "short"
		"31 bytes":           strings.Repeat("A", 40) + "AA==", // one byte short of the minimum
		"empty after decode": "",
	}
	for name, secret := range cases {
		setRequired(t)
		t.Setenv("SESSION_SECRET", secret)

		if _, err := Load(); err == nil {
			t.Errorf("%s: SESSION_SECRET %q was accepted", name, secret)
		}
	}
}

func TestLoad_PreviousSessionSecret(t *testing.T) {
	setRequired(t)

	// Unset is the normal case: no rotation in progress.
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SessionSecretPrevious != nil {
		t.Errorf("SessionSecretPrevious = %v with the var unset, want nil", c.SessionSecretPrevious)
	}

	t.Setenv("SESSION_SECRET_PREVIOUS", "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load with a previous secret: %v", err)
	}
	if len(c.SessionSecretPrevious) != 32 {
		t.Errorf("SessionSecretPrevious is %d bytes, want 32", len(c.SessionSecretPrevious))
	}

	// A weak previous secret is as bad as a weak current one — it can still
	// sign a session that verifies.
	t.Setenv("SESSION_SECRET_PREVIOUS", "c2hvcnQ=")
	if _, err := Load(); err == nil {
		t.Error("a too-short SESSION_SECRET_PREVIOUS was accepted")
	}
}

func TestLoad_SessionTTL(t *testing.T) {
	setRequired(t)
	t.Setenv("SESSION_TTL_HOURS", "8")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SessionTTL != 8*time.Hour {
		t.Errorf("SessionTTL = %s, want 8h", c.SessionTTL)
	}

	for _, bad := range []string{"0", "-1", "eight", ""} {
		setRequired(t)
		t.Setenv("SESSION_TTL_HOURS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("SESSION_TTL_HOURS %q was accepted", bad)
		}
	}
}
