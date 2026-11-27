package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/outboxtest"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/17xande-dev/payment"
)

// payOrder runs a checkout and pays it without draining the queue.
func payOrder(t *testing.T, s *shop) orders.Order {
	t.Helper()
	order := placeOrder(t, s, "S", 1)
	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "ref-"+order.ID[:8], "paid", order.TotalCents))
	return order
}

func TestMail_PaymentRollsBackEnqueuedJobs(t *testing.T) {
	s := newNotifyingShop(t)
	order := placeOrder(t, s, "S", 1)
	// Fail at commit, after the confirmation and the owner's copy were inserted,
	// so a job written outside the order's transaction would survive.
	if _, err := s.pool.Exec(t.Context(), `
		CREATE FUNCTION reject_paid() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'reject payment'; END $$;
		CREATE CONSTRAINT TRIGGER reject_paid AFTER UPDATE ON orders
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.status = 'paid') EXECUTE FUNCTION reject_paid()
	`); err != nil {
		t.Fatal(err)
	}
	res := callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "atomic", "paid", order.TotalCents))
	if res.StatusCode != http.StatusServiceUnavailable || s.reload(t, order.ID).Paid() {
		t.Fatalf("callback = %d, paid = %v; want the payment rolled back", res.StatusCode, s.reload(t, order.ID).Paid())
	}
	if jobs := outboxtest.Enqueued(t, s.handler.MailQueue(), outbox.Filter{}); len(jobs) != 0 {
		t.Fatalf("%d jobs escaped the payment's rollback", len(jobs))
	}
}

func TestMail_ConfirmationCompletionAndEmailedFlagCommitTogether(t *testing.T) {
	s := newCheckoutShop(t)
	order := payOrder(t, s)
	// Fail the commit after OnDone would have run, so a flag written on another
	// connection would leak.
	if _, err := s.pool.Exec(t.Context(), `
		CREATE FUNCTION reject_done() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'reject completion'; END $$;
		CREATE CONSTRAINT TRIGGER reject_done AFTER UPDATE ON outbox_jobs
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state = 'done') EXECUTE FUNCTION reject_done()
	`); err != nil {
		t.Fatal(err)
	}
	if n := outboxtest.Drain(t, s.handler.MailQueue()); n != 1 || len(s.mail.To("jane@example.com")) != 1 {
		t.Fatalf("worker ran %d jobs; want one delivered confirmation", n)
	}
	if s.reload(t, order.ID).Emailed {
		t.Fatal("emailed was set although the completion rolled back")
	}
	jobs := outboxtest.Enqueued(t, s.handler.MailQueue(), outbox.Filter{Subject: orders.MailSubject(order.ID)})
	if len(jobs) != 1 || jobs[0].State != outbox.StateRunning {
		t.Fatalf("completion should leave the claim recoverable: %+v", jobs)
	}
}

func TestMail_OnceKindsSendOnceWhenEnqueuedTwice(t *testing.T) {
	s := newNotifyingShop(t)
	order := payOrder(t, s)
	ctx := t.Context()
	subject := orders.MailSubject(order.ID)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, kind := range []string{kindConfirmation, kindOwner} {
		_, inserted, err := s.handler.outbox.Enqueue(ctx, tx, orderMessage(order.ID, kind, []byte("{}")))
		if err != nil || inserted {
			t.Fatalf("%s enqueued again: inserted = %v, error = %v; want a silent no-op", kind, inserted, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := outboxtest.Drain(t, s.handler.MailQueue()); n != 2 || len(s.mail.Sent()) != 2 {
		t.Fatalf("ran %d jobs and sent %d emails, want a confirmation and the owner's copy once each", n, len(s.mail.Sent()))
	}
	for _, kind := range []string{kindConfirmation, kindOwner} {
		jobs := outboxtest.Enqueued(t, s.handler.MailQueue(), outbox.Filter{Subject: subject, Kind: kind})
		if len(jobs) != 1 || jobs[0].UniqueKey != subject+":"+kind || jobs[0].State != outbox.StateDone {
			t.Errorf("%s: want one completed job with the order's key; got %+v", kind, jobs)
		}
	}
}

type senderFunc func(context.Context, mailer.Message) error

func (f senderFunc) Send(ctx context.Context, m mailer.Message) error { return f(ctx, m) }

func TestMail_WorkersDoNotSendTheSameJobConcurrently(t *testing.T) {
	s := newCheckoutShop(t)
	payOrder(t, s)
	var sends atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s.handler.mail = senderFunc(func(ctx context.Context, m mailer.Message) error {
		if len(m.To) != 1 || m.To[0] != "jane@example.com" {
			t.Error("delivery did not render the confirmation for the buyer")
		}
		if sends.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	second, err := s.handler.newMailQueue(pgxstore.New(s.pool), outbox.Options{Sealer: outboxtest.Sealer(t, 1), Now: s.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- s.handler.outbox.RunDue(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first worker never reached the transport")
	}
	// A row lock held across delivery would prevent this update even though the
	// second worker's claim could skip it.
	tag, err := s.pool.Exec(ctx, "UPDATE outbox_jobs SET result = '' WHERE state = 'running'")
	if err != nil || tag.RowsAffected() != 1 {
		close(release)
		<-done
		t.Fatalf("could not update the live claim during delivery: %s %v", tag, err)
	}
	n := second.RunDue(ctx)
	close(release)
	if first := <-done; first != 1 || n != 0 || sends.Load() != 1 {
		t.Fatalf("jobs run = %d and %d, sends = %d; want one delivery", first, n, sends.Load())
	}
}

// A payload that cannot be opened must never reach the transport: it ends dead
// at once with the library's fixed reason, whatever was done to it.
func TestMail_UnopenablePayloadsNeverReachTransportAndEndDead(t *testing.T) {
	for _, corruption := range []string{"wrong_key", "flipped_bit", "moved_to_another_order", "moved_to_another_kind"} {
		t.Run(corruption, func(t *testing.T) {
			s := newCheckoutShop(t)
			order := payOrder(t, s)
			ctx := t.Context()
			sealer := outboxtest.Sealer(t, 1)
			switch corruption {
			case "wrong_key":
				sealer = outboxtest.Sealer(t, 2)
			case "flipped_bit":
				if _, err := s.pool.Exec(ctx, `UPDATE outbox_jobs SET payload = set_byte(payload, length(payload) - 1,
					get_byte(payload, length(payload) - 1) # 1)`); err != nil {
					t.Fatal(err)
				}
			case "moved_to_another_order":
				// The same bytes, on a row about another order: the subject is
				// part of what the seal authenticates.
				if _, err := s.pool.Exec(ctx, `UPDATE outbox_jobs SET subject = 'order:00000000-0000-0000-0000-000000000001'
					WHERE kind = 'confirmation'`); err != nil {
					t.Fatal(err)
				}
			case "moved_to_another_kind":
				// A buyer's confirmation (with download links) replayed as the
				// owner's copy.
				if _, err := s.pool.Exec(ctx, `UPDATE outbox_jobs SET kind = 'owner' WHERE kind = 'confirmation'`); err != nil {
					t.Fatal(err)
				}
			}
			q, err := s.handler.newMailQueue(pgxstore.New(s.pool), outbox.Options{Sealer: sealer, Now: s.clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			n := outboxtest.Drain(t, q)
			if len(s.mail.Sent()) != 0 {
				t.Fatalf("a payload that cannot be opened reached the transport: %+v", s.mail.Sent())
			}
			jobs := outboxtest.Enqueued(t, q, outbox.Filter{State: outbox.StateDead})
			if n == 0 || len(jobs) == 0 {
				t.Fatalf("ran %d jobs, %d dead; want at least one dead job", n, len(jobs))
			}
			for _, j := range jobs {
				if j.Attempts != 1 || j.LastError != "cannot open payload: check the key" {
					t.Errorf("dead job should say why and have spent one attempt: %+v", j)
				}
			}
			if s.reload(t, order.ID).Emailed {
				t.Error("emailed was set for a confirmation that was never sent")
			}
		})
	}
}

// The payload holds the download URL and its token. It must reach the transport
// and nothing else: not the logs, not last_error, not a request's error log.
func TestMail_DownloadURLReachesOnlyTheTransport(t *testing.T) {
	d := newDigitalShop(t)
	addToCart(t, d.srv, d.audio.ID, 1)
	if res, body := post(t, d.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatal(body)
	}
	order, err := d.orders.LatestForCart(t.Context(), cartTokenOf(t, d.srv))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	log := outboxtest.NewLog()
	d.handler.log = log.Logger()
	q, err := d.handler.newMailQueue(pgxstore.New(d.pool), outbox.Options{Sealer: outboxtest.Sealer(t, 1), Logger: log.Logger(), Now: d.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	d.handler.outbox = q

	// A transport that records the body, and fails the first time.
	const transport = "test-transport-marker-ad25b1"
	var bodies []string
	var fail atomic.Bool
	fail.Store(true)
	d.handler.mail = senderFunc(func(_ context.Context, m mailer.Message) error {
		bodies = append(bodies, m.Text+m.HTML)
		if fail.Load() {
			return errors.New(transport + " " + m.Text)
		}
		return nil
	})
	callback(t, d.srv, "fake", payment.FakeCallbackBody(order.ID, "durable", "paid", order.TotalCents))

	var sealed []byte
	if err := d.pool.QueryRow(ctx, `SELECT payload FROM outbox_jobs WHERE kind = 'confirmation'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sealed, []byte{1, 1}) || bytes.Contains(sealed, []byte("/downloads/")) {
		t.Fatal("the payload should be stored as AES-GCM ciphertext with key ID 1")
	}

	outboxtest.Drain(t, q) // fails: the error text carries the whole mail body
	if len(bodies) != 1 {
		t.Fatalf("transport saw %d bodies, want 1", len(bodies))
	}
	// The link is the secret under test: read it from what the transport got.
	var link string
	for _, f := range strings.Fields(bodies[0]) {
		if _, after, ok := strings.Cut(f, "/downloads/"); ok {
			link = "/downloads/" + strings.Trim(after, `"<>.,`)
			break
		}
	}
	token := strings.TrimPrefix(link, "/downloads/")
	if len(token) < 12 {
		t.Fatalf("the transport did not receive a download link: %q", bodies[0])
	}
	jobs := outboxtest.Enqueued(t, q, outbox.Filter{Subject: orders.MailSubject(order.ID)})
	if len(jobs) != 1 || jobs[0].State != outbox.StatePending || jobs[0].Attempts != 1 ||
		jobs[0].LastError != "Email delivery failed; check mail configuration and retry." {
		t.Fatalf("expected a failed send with a fixed description: %+v", jobs)
	}
	log.RequireAbsent(t, token, transport, orders.MailSubject(order.ID), fmt.Sprintf("%x", sealed),
		base64.StdEncoding.EncodeToString(sealed))

	// A write error can echo the sealed row; the request's error log must not.
	const diagnostic = "test-database-row-marker-cc98"
	if _, err := d.pool.Exec(ctx, `
		CREATE FUNCTION reject_job() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test-database-row-marker-cc98 %', encode(NEW.payload, 'hex'); END $$;
		CREATE TRIGGER reject_job BEFORE INSERT ON outbox_jobs FOR EACH ROW EXECUTE FUNCTION reject_job()
	`); err != nil {
		t.Fatal(err)
	}
	other := placeOrderDigital(t, d)
	res := callback(t, d.srv, "fake", payment.FakeCallbackBody(other.ID, "again", "paid", other.TotalCents))
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a failed enqueue answered %d, want 503", res.StatusCode)
	}
	log.RequireAbsent(t, diagnostic)

	// The retry delivers the same link, and the payload is cleared on success.
	fail.Store(false)
	signIn(t, d.srv)
	if res, body := post(t, d.srv, "/admin/orders/"+order.ID+"/email/retry", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry: %d %s", res.StatusCode, body)
	}
	outboxtest.Drain(t, q)
	if len(bodies) != 2 || !strings.Contains(bodies[1], token) {
		t.Fatalf("the retry did not deliver the same link: %d bodies", len(bodies))
	}
	var cleared []byte
	if err := d.pool.QueryRow(ctx, `SELECT payload FROM outbox_jobs WHERE kind = 'confirmation' AND subject = $1`,
		orders.MailSubject(order.ID)).Scan(&cleared); err != nil || len(cleared) != 0 {
		t.Fatalf("the sent job's payload was not cleared: %d bytes, %v", len(cleared), err)
	}
	log.RequireAbsent(t, token, transport)
}

func placeOrderDigital(t *testing.T, d *digitalShop) orders.Order {
	t.Helper()
	newSession(t, d.srv)
	addToCart(t, d.srv, d.audio.ID, 1)
	if res, body := post(t, d.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatal(body)
	}
	order, err := d.orders.LatestForCart(t.Context(), cartTokenOf(t, d.srv))
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func TestMail_TransportPanicIsContainedWithoutLoggingItsValue(t *testing.T) {
	s := newCheckoutShop(t)
	order := payOrder(t, s)
	const marker = "test-panic-marker-8b925d"
	s.handler.mail = senderFunc(func(context.Context, mailer.Message) error { panic(marker) })
	log := outboxtest.NewLog()
	q, err := s.handler.newMailQueue(pgxstore.New(s.pool), outbox.Options{Sealer: outboxtest.Sealer(t, 1), Logger: log.Logger(), Now: s.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if n := outboxtest.Drain(t, q); n != 1 {
		t.Fatalf("ran %d jobs; want one failed attempt", n)
	}
	jobs := outboxtest.Enqueued(t, q, outbox.Filter{})
	if len(jobs) != 1 || jobs[0].State != outbox.StatePending || jobs[0].Attempts != 1 || s.reload(t, order.ID).Emailed {
		t.Fatalf("panicked delivery should stay retryable: %+v", jobs)
	}
	if !strings.Contains(log.String(), "job callback panicked") {
		t.Fatal("expected a panic log with its location")
	}
	log.RequireAbsent(t, marker)
}
