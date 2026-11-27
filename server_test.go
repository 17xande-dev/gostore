package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type shutdownJobLog struct {
	slog.Handler
	entered chan struct{}
	release <-chan struct{}
	waiting chan struct{}
}

func (l shutdownJobLog) Enabled(context.Context, slog.Level) bool { return true }

func (l shutdownJobLog) Handle(_ context.Context, r slog.Record) error {
	var outcome string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "outcome" {
			outcome = a.Value.String()
		}
		return true
	})
	if r.Message == "job transition" && outcome == "running" {
		close(l.entered)
		<-l.release
	}
	if r.Message == "waiting for email worker to stop" {
		close(l.waiting)
	}
	return nil
}

func TestGracefulShutdownJoinsEmailWorkerBeforeClosingPool(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg := serverEnv(t, strings.Repeat("01", 32))
	sealer, err := newQueueSealer(cfg.EmailQueueKey.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	// An independent pool can inspect the settled claim after the server
	// closes the pool it owns.
	observer, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	q, err := outbox.New(pgxstore.New(observer), map[string]outbox.Kind[pgx.Tx]{
		"confirmation": {Run: func(context.Context, outbox.Job) error { return nil }},
	}, outbox.Options{Sealer: sealer})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := q.EnqueueNow(ctx, outbox.Message{Kind: "confirmation", Subject: "order:00000000-0000-0000-0000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}

	entered, blocked, waiting := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(blocked) })
	defer release()
	log := slog.New(shutdownJobLog{Handler: slog.DiscardHandler, entered: entered, release: blocked, waiting: waiting})
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, pool, log) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("server exited before claiming the queued job: %v", err)
	case <-ctx.Done():
		t.Fatal("worker did not claim the queued job")
	}
	cancel()
	// Make one Stop call time out, then check the server still waits.
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("server exited before waiting for the worker: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("Stop did not report a timeout")
	}
	select {
	case err := <-done:
		t.Fatalf("server exited before the worker joined: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("server closed the pool before the worker joined: %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("server did not exit after the worker joined")
	}
	if err := pool.Ping(t.Context()); err == nil {
		t.Fatal("server left its pool open")
	}
	info, err := q.Get(t.Context(), id)
	if err != nil || info.State != outbox.StatePending || info.Attempts != 0 {
		t.Fatalf("server did not refund the interrupted claim: %+v %v", info, err)
	}
}
