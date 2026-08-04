package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/dbtest"
)

func TestStartCartCleanup_SweepsOnBootAndStopsWithTheContext(t *testing.T) {
	pool := dbtest.Pool(t)
	carts := cart.NewStore(pool)
	ctx := t.Context()

	// One cart aged past the TTL and one fresh. The old one is what an abandoned
	// basket looks like after two months.
	old, err := carts.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, err := carts.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE carts SET updated_at = now() - make_interval(days => 90) WHERE id = $1`, old); err != nil {
		t.Fatalf("age the cart: %v", err)
	}

	startCartCleanup(ctx, carts, 60, slog.New(slog.DiscardHandler))

	// The sweep runs on boot, not only on the first tick — so a restart after a busy
	// month does the work immediately rather than a day later.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := carts.Get(ctx, old); err != nil {
			break // gone, as intended
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned cart was still there five seconds after startup")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And it took only what it should have.
	if _, err := carts.Get(ctx, fresh); err != nil {
		t.Errorf("the cleanup removed a cart that was still in use: %v", err)
	}
}

func TestStartCartCleanup_SurvivesAFailedSweep(t *testing.T) {
	// A cleanup that fails is a table that grows a little longer, which is not worth
	// crashing the server over: the goroutine logs and waits for the next tick. Here
	// the pool is closed under it, which is the bluntest version of a database
	// problem.
	pool := dbtest.Pool(t)
	carts := cart.NewStore(pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Close()

	startCartCleanup(ctx, carts, 60, slog.New(slog.DiscardHandler))
	// Nothing to assert but the absence of a panic taking the process with it.
	time.Sleep(100 * time.Millisecond)
}
