package config

import "testing"

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset, got nil")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/gostore")

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
}

func TestLoad_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/gostore")
	t.Setenv("BASE_URL", "https://store.example.com/")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseURL != "https://store.example.com" {
		t.Errorf("BaseURL = %q, want https://store.example.com", c.BaseURL)
	}
}

func TestLoad_RejectsBadLogLevel(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/gostore")
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an unknown LOG_LEVEL, got nil")
	}
}
