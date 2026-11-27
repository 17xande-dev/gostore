package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/outboxtest"
)

// failUntilDead drains the queue while the mail relay refuses everything, past
// every backoff, until each job has used all of its attempts.
func failUntilDead(t *testing.T, s *shop) {
	t.Helper()
	s.mail.Err = errors.New("relay unavailable")
	for range 6 {
		outboxtest.Drain(t, s.handler.MailQueue())
		s.clock.Advance(7 * time.Hour)
	}
}

// deadConfirmation pays one order, signs in as the owner, and returns the order
// with its confirmation email dead.
func deadConfirmation(t *testing.T) (*shop, orders.Order, outbox.Info) {
	t.Helper()
	s := newCheckoutShop(t)
	order := payOrder(t, s)
	signIn(t, s.srv)
	failUntilDead(t, s)
	jobs := outboxtest.Enqueued(t, s.handler.MailQueue(), outbox.Filter{Subject: orders.MailSubject(order.ID), State: outbox.StateDead})
	if len(jobs) != 1 {
		t.Fatalf("want one dead confirmation, got %+v", jobs)
	}
	return s, order, jobs[0]
}

func mailRetryPath(job outbox.Info) string {
	return mailPath + "/" + strconv.FormatInt(job.ID, 10) + "/retry"
}

func TestAdminMail_ListsDeadJobsWithFixedTextCountsAndALinkToTheOrder(t *testing.T) {
	s, order, job := deadConfirmation(t)

	res, page := get(t, s.srv, mailPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", mailPath, res.StatusCode)
	}
	for _, want := range []string{
		"Dead: 1", "Pending: 0",
		">" + kindConfirmation + "</td>", ">6/6</td>",
		`href="/admin/orders/` + order.ID + `"`,
		"Email delivery failed; check mail configuration and retry.",
		mailRetryPath(job),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the mail page lacks %q", want)
		}
	}
	// What the transport said can carry credentials; only the fixed class text
	// may reach an admin's screen.
	if strings.Contains(page, "relay unavailable") {
		t.Error("the mail page shows the transport's own error text")
	}

	_, orderList := get(t, s.srv, "/admin/orders")
	if !strings.Contains(orderList, `href="/admin/mail"`) || !strings.Contains(orderList, "1 email has failed") {
		t.Errorf("the order list does not tell an admin about the dead email: %s", excerpt(orderList))
	}
	_, orderPage := get(t, s.srv, "/admin/orders/"+order.ID)
	if !strings.Contains(orderPage, "failed for good (6/6 attempts)") {
		t.Errorf("the order page does not show the dead email: %s", excerpt(orderPage))
	}
}

func TestAdminMail_NavigationLinksToThePage(t *testing.T) {
	s := newCheckoutShop(t)
	signIn(t, s.srv)
	// On a page that is not the queue's own, so the link is the nav's.
	_, page := get(t, s.srv, "/admin/categories")
	if !strings.Contains(page, `<a href="/admin/mail">Email</a>`) {
		t.Errorf("the admin navigation has no link to the email queue: %s", excerpt(page))
	}
}

func TestAdminMail_NothingDeadSaysSoAndTheOrderListStaysQuiet(t *testing.T) {
	s := newCheckoutShop(t)
	signIn(t, s.srv)
	_, page := get(t, s.srv, mailPath)
	if !strings.Contains(page, "No dead emails.") || !strings.Contains(page, "Dead: 0") {
		t.Errorf("an empty queue page: %s", excerpt(page))
	}
	if _, list := get(t, s.srv, "/admin/orders"); strings.Contains(list, "failed for good") {
		t.Error("the order list warns about dead emails when there are none")
	}
}

func TestAdminMail_RetryNeedsACSRFToken(t *testing.T) {
	s, _, job := deadConfirmation(t)

	form := url.Values{"csrf_token": {""}} // Explicitly empty, so the helper adds none.
	res, _ := post(t, s.srv, mailRetryPath(job), form)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("retry without a CSRF token = %d, want 403", res.StatusCode)
	}
	if got, err := s.handler.outbox.Get(t.Context(), job.ID); err != nil || got.State != outbox.StateDead {
		t.Errorf("a forged retry changed the job: %+v %v", got, err)
	}
}

func TestAdminMail_ViewerSeesTheListButCannotRetry(t *testing.T) {
	s, _, job := deadConfirmation(t)
	mustAccount(t, s, "viewer@example.com", testPassword, auth.RoleViewer)
	signInAs(t, s.srv, "viewer@example.com", testPassword)

	res, page := get(t, s.srv, mailPath)
	if res.StatusCode != http.StatusOK || strings.Contains(page, mailRetryPath(job)) {
		t.Errorf("viewer GET = %d; offered the retry form = %v", res.StatusCode, strings.Contains(page, mailRetryPath(job)))
	}
	res, _ = post(t, s.srv, mailRetryPath(job), url.Values{})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's retry = %d, want 403", res.StatusCode)
	}
	if got, err := s.handler.outbox.Get(t.Context(), job.ID); err != nil || got.State != outbox.StateDead {
		t.Errorf("a viewer's retry changed the job: %+v %v", got, err)
	}
}

func TestAdminMail_RetryRequeuesWithAnActorAndAStaleClickSaysSo(t *testing.T) {
	s, order, job := deadConfirmation(t)

	// Through the page's own form, so a form that forgot its CSRF field fails here.
	s.srv.browser.With(t).Page(mailPath).Form(mailRetryPath(job)).Submit().
		WantRedirect(http.StatusSeeOther, mailPath+"?notice=retried")
	got, err := s.handler.outbox.Get(t.Context(), job.ID)
	if err != nil || got.State != outbox.StatePending || got.Attempts != 0 || got.RetryCount != 1 || got.RetriedBy != s.owner.ID {
		t.Fatalf("after retry: %+v %v", got, err)
	}
	_, page := get(t, s.srv, mailPath+"?notice=retried")
	if !strings.Contains(page, "queued to be sent again") || !strings.Contains(page, "No dead emails.") {
		t.Errorf("page after retry: %s", excerpt(page))
	}

	// The same button clicked again, from a page that is out of date.
	res, _ := post(t, s.srv, mailRetryPath(job), url.Values{})
	if loc := res.Header.Get("Location"); loc != mailPath+"?notice=not_dead" {
		t.Errorf("stale retry went to %q", loc)
	}
	if again, _ := s.handler.outbox.Get(t.Context(), job.ID); again.RetryCount != 1 {
		t.Errorf("a stale click retried the job again: %+v", again)
	}
	_, page = get(t, s.srv, mailPath+"?notice=not_dead")
	if !strings.Contains(page, "not dead any more") {
		t.Errorf("a stale click is not explained: %s", excerpt(page))
	}

	s.mail.Err = nil
	outboxtest.Drain(t, s.handler.MailQueue())
	if len(s.mail.To("jane@example.com")) != 1 || !s.reload(t, order.ID).Emailed {
		t.Errorf("the retried confirmation was not sent: %d emails", len(s.mail.To("jane@example.com")))
	}
}

func TestAdminMail_MoreThanOnePageOfDeadJobsIsCappedAndSaysSo(t *testing.T) {
	s := newCheckoutShop(t)
	signIn(t, s.srv)
	const total = deadMailLimit + 1
	for i := range total {
		// A subject with no order behind it: the job dies on its own (its
		// handler is told the mail is down), and the link goes to a 404 page
		// rather than being absent, which is what an order id that vanished is.
		if _, _, err := s.handler.outbox.EnqueueNow(t.Context(), outbox.Message{Kind: kindConfirmation, Subject: orders.MailSubject("gone-" + strconv.Itoa(i)),
			Payload: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	failUntilDead(t, s)

	_, page := get(t, s.srv, mailPath)
	if rows := strings.Count(page, "/retry\""); rows != deadMailLimit {
		t.Errorf("the page lists %d retry forms, want the cap of %d", rows, deadMailLimit)
	}
	if !strings.Contains(page, "Showing the newest 100 of 101") || !strings.Contains(page, "Dead: 101") {
		t.Errorf("the cap is not shown honestly: %s", excerpt(page))
	}
}

func TestAdminMail_ASubjectThatIsNotAnOrderHasNoLink(t *testing.T) {
	s := newCheckoutShop(t)
	signIn(t, s.srv)
	if _, _, err := s.handler.outbox.EnqueueNow(t.Context(), outbox.Message{Kind: kindOwner, Subject: "invoice:7", Payload: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	failUntilDead(t, s)
	_, page := get(t, s.srv, mailPath)
	if !strings.Contains(page, `<small class="muted">none</small>`) || strings.Contains(page, "Open order") {
		t.Errorf("a job about something else should have no order link: %s", excerpt(page))
	}
}

// The order page's retry button is audited: RetrySubject with the signed-in
// admin, and it revives a dead job with a fresh set of attempts.
func TestAdminOrders_RetryEmailRevivesDeadMailAndRecordsTheAdminAsTheActor(t *testing.T) {
	s, order, job := deadConfirmation(t)
	s.mail.Err = nil
	if res, _ := post(t, s.srv, "/admin/orders/"+order.ID+"/email/retry", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry = %d", res.StatusCode)
	}
	got, err := s.handler.outbox.Get(t.Context(), job.ID)
	if err != nil || got.State != outbox.StatePending || got.Attempts != 0 || got.RetriedBy != s.owner.ID {
		t.Fatalf("after the order page's retry: %+v %v", got, err)
	}
	outboxtest.Drain(t, s.handler.MailQueue())
	if !s.reload(t, order.ID).Emailed {
		t.Error("the retried confirmation was not sent")
	}
}
