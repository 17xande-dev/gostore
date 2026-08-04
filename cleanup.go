package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/17xande-dev/gostore/internal/cart"
)

// cleanupInterval is how often abandoned carts are swept. Daily: the table grows
// by one row per shopper who adds something and never comes back, which for a shop
// this size is not a rate that needs watching more closely than that.
const cleanupInterval = 24 * time.Hour

// startCartCleanup deletes carts nobody has touched for ttlDays, on boot and then
// daily, until ctx is cancelled.
//
// It runs in every instance rather than being elected to one, and that is fine
// because the work is a single idempotent DELETE: two instances running it at once
// produce the same end state as one, and the second finds nothing to do. Electing a
// leader would need coordination this store has no other use for.
//
// The sweep is deliberately not a database trigger or a cron container. It is a
// goroutine in the process that already has a pool and a logger, which keeps the
// deployment story — one binary, one container — intact.
func startCartCleanup(ctx context.Context, carts *cart.Store, ttlDays int, log *slog.Logger) {
	sweep := func() {
		// Its own timeout, and detached from the shutdown context: a sweep that is
		// running when the server is asked to stop should finish or give up on its
		// own terms rather than leave a half-executed statement.
		sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()

		removed, err := carts.DeleteOlderThan(sweepCtx, ttlDays)
		if err != nil {
			// Logged and not retried before the next tick. A cleanup that fails is a
			// table that grows a little longer, which is not worth waking anyone.
			log.Error("cart cleanup failed", "error", err)
			return
		}
		if removed > 0 {
			log.Info("cart cleanup removed abandoned carts", "carts", removed, "older_than_days", ttlDays)
		}
	}

	go func() {
		// Once at startup, so a long-lived deployment is not the only one that ever
		// cleans up, and a restart after a busy month does the work immediately.
		sweep()

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Debug("cart cleanup stopping")
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
