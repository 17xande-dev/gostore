package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/outbox"
	"github.com/jackc/pgx/v5"
)

// paidOrderWithMail makes a paid order through the same store call the webhook
// uses, so its confirmation job exists as it does in production.
func paidOrderWithMail(t *testing.T, f *fixture, withJob bool) orders.Order {
	t.Helper()
	ctx := t.Context()
	p, err := f.catalog.Create(ctx, catalog.Product{Title: "Mug", Slug: "mug", Kind: catalog.KindPhysical, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.catalog.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "MUG", PriceCents: 1000, StockQty: 3, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	cartID, err := f.carts.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.carts.Add(ctx, cartID, v.ID, 1); err != nil {
		t.Fatal(err)
	}
	order, err := f.orders.CreateFromCart(ctx, cartID, orders.Customer{Name: "Ann", Email: "ann@example.com", Address: "1 Road"}, "ZAR", "fake")
	if err != nil {
		t.Fatal(err)
	}
	pay := orders.Payment{Gateway: "fake", Ref: "r1", Status: "COMPLETE", Amount: "10.00"}
	if !withJob {
		if _, err := f.orders.MarkPaid(ctx, order.ID, pay); err != nil {
			t.Fatal(err)
		}
		return order
	}
	subject := orders.MailSubject(order.ID)
	prepare := func(orders.PaidResult) ([]outbox.Message, error) {
		return []outbox.Message{{Kind: "confirmation", Subject: subject, UniqueKey: subject + ":confirmation", Payload: []byte("{}")}}, nil
	}
	enqueue := func(ctx context.Context, tx pgx.Tx, m outbox.Message) error {
		_, _, err := f.queue.Enqueue(ctx, tx, m)
		return err
	}
	if _, err := f.orders.MarkPaidWithMail(ctx, order.ID, pay, prepare, enqueue); err != nil {
		t.Fatal(err)
	}
	return order
}

func (f *fixture) orderJobs(t *testing.T, order orders.Order) []outbox.Info {
	t.Helper()
	jobs, err := f.queue.List(t.Context(), outbox.Filter{Subject: orders.MailSubject(order.ID), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func (f *fixture) killJobs(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE outbox_jobs SET state = 'dead', attempts = max_attempts, last_error = 'gave up'`); err != nil {
		t.Fatal(err)
	}
}

func TestTools_RetryOrderEmailRevivesADeadJobAndRecordsTheTokensOwner(t *testing.T) {
	f := newFixture(t)
	u, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)
	order := paidOrderWithMail(t, f, true)
	f.killJobs(t)

	var got queued
	if msg := call(t, s, "retry_order_email", map[string]any{"id": order.ID}, &got); msg != "" || !got.Queued {
		t.Fatalf("retry_order_email: %q %+v", msg, got)
	}
	jobs := f.orderJobs(t, order)
	if len(jobs) != 1 || jobs[0].State != outbox.StatePending || jobs[0].RetriedBy != u.ID {
		t.Fatalf("after retry: %+v, want one pending job retried by %s", jobs, u.ID)
	}
}

func TestTools_RetryOrderEmailAcceptsANonCanonicalOrderID(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)
	order := paidOrderWithMail(t, f, true)
	f.killJobs(t)

	// Postgres reads any UUID spelling, so the lookup finds the order; the job's
	// subject must come from the id it returns, not from the spelling typed.
	var got queued
	if msg := call(t, s, "retry_order_email", map[string]any{"id": strings.ToUpper(order.ID)}, &got); msg != "" || !got.Queued {
		t.Fatalf("retry_order_email: %q %+v", msg, got)
	}
	if jobs := f.orderJobs(t, order); len(jobs) != 1 || jobs[0].State != outbox.StatePending {
		t.Fatalf("an upper-case id did not revive the job: %+v", jobs)
	}
}

func TestTools_RetryOrderEmailWithNoJobSaysSoInsteadOfQueued(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)
	order := paidOrderWithMail(t, f, false)

	var got queued
	msg := call(t, s, "retry_order_email", map[string]any{"id": order.ID}, &got)
	if !strings.Contains(msg, "no undelivered emails") || got.Queued {
		t.Fatalf("retry with no job: msg %q, queued %v", msg, got.Queued)
	}
}

func TestTools_GetOrderReportsEachEmailsStateAndSentTime(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)
	order := paidOrderWithMail(t, f, true)

	var got Order
	if msg := call(t, s, "get_order", map[string]any{"id": order.ID}, &got); msg != "" {
		t.Fatal(msg)
	}
	if len(got.Emails) != 1 || got.Emails[0].State != outbox.StatePending || got.Emails[0].SentAt != nil {
		t.Fatalf("pending: %+v", got.Emails)
	}
	f.killJobs(t)
	call(t, s, "get_order", map[string]any{"id": order.ID}, &got)
	if len(got.Emails) != 1 || got.Emails[0].State != outbox.StateDead || got.Emails[0].LastError != "gave up" {
		t.Fatalf("dead: %+v", got.Emails)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE outbox_jobs SET state = 'done', finished_at = now(), last_error = ''`); err != nil {
		t.Fatal(err)
	}
	call(t, s, "get_order", map[string]any{"id": order.ID}, &got)
	if len(got.Emails) != 1 || got.Emails[0].State != outbox.StateDone || got.Emails[0].SentAt == nil || got.Emails[0].SentAt.IsZero() {
		t.Fatalf("done: %+v", got.Emails)
	}
}
