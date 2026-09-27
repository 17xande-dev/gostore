package outbox_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/17xande-dev/gostore/internal/outbox"
)

func TestWorkersDoNotSendTheSameJobConcurrently(t *testing.T) {
	pool := dbtest.Pool(t)
	s, err := outbox.New(pool, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = pool.QueryRow(t.Context(), `INSERT INTO orders (id, customer_name, customer_email, total_cents, currency, status)
VALUES (gen_random_uuid(), 'Buyer', 'buyer@example.com', 100, 'ZAR', 'paid') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Encrypt(id, "confirmation", []byte("receipt"))
	if err := gen.New(pool).EnqueueEmail(t.Context(), gen.EnqueueEmailParams{OrderID: id, Kind: m.Kind, Payload: m.Payload}); err != nil {
		t.Fatal(err)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := s.DeliverOne(t.Context(), func(ctx context.Context, kind string, body []byte) error {
			calls.Add(1)
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
	<-started
	found, err := s.DeliverOne(t.Context(), func(context.Context, string, []byte) error { calls.Add(1); return nil })
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if found || err != nil || calls.Load() != 1 {
		t.Fatalf("second worker: found=%v err=%v sends=%d", found, err, calls.Load())
	}
}

func TestWrongKeyAndModifiedCiphertextNeverReachTransport(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		pool := dbtest.Pool(t)
		s, err := outbox.New(pool, strings.Repeat("ab", 32))
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := pool.QueryRow(t.Context(), `INSERT INTO orders (id, customer_name, customer_email, total_cents, currency)
VALUES (gen_random_uuid(), 'Buyer', 'buyer@example.com', 100, 'ZAR') RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		m := s.Encrypt(id, "confirmation", []byte("private link"))
		if corrupt {
			m.Payload[len(m.Payload)-1] ^= 1
		} else {
			s, err = outbox.New(pool, strings.Repeat("cd", 32))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := gen.New(pool).EnqueueEmail(t.Context(), gen.EnqueueEmailParams{OrderID: id, Kind: m.Kind, Payload: m.Payload}); err != nil {
			t.Fatal(err)
		}
		called := false
		found, err := s.DeliverOne(t.Context(), func(context.Context, string, []byte) error {
			called = true
			return errors.New("must not reach transport")
		})
		if !found || err == nil || called {
			t.Fatalf("invalid ciphertext: found=%v err=%v sent=%v", found, err, called)
		}
		jobs, err := s.ForOrder(t.Context(), id)
		if err != nil || len(jobs) != 1 || jobs[0].SentAt != nil || jobs[0].Attempts != 1 {
			t.Fatalf("job lost: %+v %v", jobs, err)
		}
	}
}
