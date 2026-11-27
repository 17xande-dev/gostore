package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EMAIL_QUEUE_KEY is part of what an operator was promised (its name, 64 hex
// characters, used directly as the AES-256-GCM key, key ID 1), so a change to
// any of it must be a decision, not an accident.

const testQueueKeyHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// independentSealer is the documented key handling, written out separately from
// newQueueSealer.
func independentSealer(t *testing.T, keyHex string) outbox.Sealer {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	s, err := outbox.AESGCM(1, map[byte][]byte{1: key})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewQueueSealer_IsTheEnvKeyUsedDirectlyUnderKeyID1(t *testing.T) {
	got, err := newQueueSealer(testQueueKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	want := independentSealer(t, testQueueKeyHex)
	ad := []byte("outbox/v1\x00confirmation\x00order:1\x00")

	// Sealed by one, opened by the other, in both directions.
	for name, pair := range map[string][2]outbox.Sealer{"newQueueSealer seals": {got, want}, "documented seals": {want, got}} {
		sealed, err := pair[0].Seal([]byte("payload"), ad)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sealed) < 2 || sealed[0] != 0x01 || sealed[1] != 0x01 {
			t.Errorf("%s: ciphertext starts % x, want version 0x01 and key ID 0x01", name, sealed[:min(2, len(sealed))])
		}
		plain, err := pair[1].Open(sealed, ad)
		if err != nil || !bytes.Equal(plain, []byte("payload")) {
			t.Errorf("%s: the other side opened it as %q, %v", name, plain, err)
		}
	}

	// A different key must not open it, so the test cannot pass by both sides
	// sharing a constant.
	other, err := newQueueSealer(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := got.Seal([]byte("payload"), ad)
	if _, err := other.Open(sealed, ad); err == nil {
		t.Error("a sealer from another key opened the payload")
	}
}

func TestNewQueueSealer_RefusesAKeyThatIsNot64HexCharacters(t *testing.T) {
	for _, bad := range []string{"", "abcd", strings.Repeat("ab", 31), strings.Repeat("ab", 33), strings.Repeat("zz", 32)} {
		if s, err := newQueueSealer(bad); err == nil || s != nil || !strings.Contains(err.Error(), "EMAIL_QUEUE_KEY") {
			t.Errorf("key %q: sealer %v, error %v; want a refusal naming EMAIL_QUEUE_KEY", bad, s, err)
		}
	}
}

// serverEnv sets what config.Load needs for a server on a throwaway schema.
func serverEnv(t *testing.T, queueKey string) config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", os.Getenv("TEST_DATABASE_URL"))
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("EMAIL_QUEUE_KEY", queueKey)
	t.Setenv("PAYFAST_MERCHANT_ID", "10000100")
	t.Setenv("PAYFAST_MERCHANT_KEY", "46f0cd694581a")
	t.Setenv("SETUP_TOKEN", "unused-test-setup-token-fixture-000")
	t.Setenv("IMAGE_DIR", t.TempDir())
	t.Setenv("SMTP_HOST", "localhost")
	t.Setenv("EMAIL_FROM", "orders@example.com")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = "0"
	return cfg
}

// The server's queue must be built with that sealer. A queue without one stores
// payloads in the clear and cannot read the sealed ones already in the table.
func TestRunServer_QueueOpensPayloadsSealedWithTheDocumentedKeyAndRefusesOthers(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cfg := serverEnv(t, testQueueKeyHex)

	// Enqueue before the server starts, through an independent queue, so the
	// only thing under test is what the server's own queue does with the rows.
	observer, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	subject := "order:00000000-0000-0000-0000-000000000001"
	enqueue := func(sealer outbox.Sealer) int64 {
		t.Helper()
		q, err := outbox.New(pgxstore.New(observer), map[string]outbox.Kind[pgx.Tx]{
			"confirmation": {Run: func(context.Context, outbox.Job) error { return nil }},
		}, outbox.Options{Sealer: sealer})
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := q.EnqueueNow(ctx, outbox.Message{Kind: "confirmation", Subject: subject, Payload: []byte("secret")})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	good := enqueue(independentSealer(t, testQueueKeyHex))
	bad := enqueue(independentSealer(t, strings.Repeat("ab", 32)))
	reader, err := outbox.New(pgxstore.New(observer), map[string]outbox.Kind[pgx.Tx]{
		"confirmation": {Run: func(context.Context, outbox.Job) error { return errors.New("not run here") }},
	}, outbox.Options{})
	if err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runServer(runCtx, cfg, pool, slog.New(slog.DiscardHandler)) }()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not stop")
		}
	}()

	// The wrongly keyed job must die for its key. The right one is opened and
	// handed to the app's handler, which cannot render "secret" and so fails for
	// a different reason, still pending.
	deadline := time.After(15 * time.Second)
	for {
		g, err := reader.Get(ctx, good)
		if err != nil {
			t.Fatal(err)
		}
		b, err := reader.Get(ctx, bad)
		if err != nil {
			t.Fatal(err)
		}
		if b.State == outbox.StateDead && g.Attempts >= 1 {
			if !strings.Contains(b.LastError, "cannot open payload") {
				t.Errorf("wrongly keyed job died of %q, want a payload that cannot be opened", b.LastError)
			}
			if strings.Contains(g.LastError, "cannot open payload") {
				t.Errorf("a payload sealed with the documented key was refused: %+v", g)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the server's queue did not settle both jobs: good %+v, bad %+v", g, b)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
