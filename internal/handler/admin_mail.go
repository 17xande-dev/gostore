package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/outbox"
)

// The email queue's dead letters.
//
// A job that failed six times stays dead until somebody retries it, so after a
// mail outage this page is the one place that says which buyers never got their
// receipt or download links, without a database client. It shows only what the
// queue stores: the kind, the attempt count and the fixed failure reason (never
// a payload, a download URL or transport text), and links each job to its order
// for who and what.

// deadMailLimit caps the list, because the queue's List is paged and a page
// that tried to hold every dead job would grow with an outage. The cap is shown
// on the page, and retrying a row makes the next one appear.
const deadMailLimit = 100

const mailPath = "/admin/mail"

var mailNotices = map[string]string{
	"retried":  "The email was queued to be sent again.",
	"not_dead": "That email is not dead any more: it was already retried or handled, so nothing changed.",
}

type deadMail struct {
	outbox.Info
	// OrderID is empty when the subject is not an order's, which leaves the row
	// without a link rather than with a broken one.
	OrderID string
}

type mailPage struct {
	page
	Stats  outbox.Stats
	Dead   []deadMail
	Cap    int
	Notice string
}

// Capped reports whether more dead jobs exist than the page shows.
func (p mailPage) Capped() bool { return p.Stats.Dead > len(p.Dead) }

func (h *Handler) adminMail(w http.ResponseWriter, r *http.Request) {
	stats, err := h.outbox.Stats(r.Context())
	if err != nil {
		h.serverError(w, r, mailQueueError("count emails", err))
		return
	}
	dead, err := h.outbox.List(r.Context(), outbox.Filter{State: outbox.StateDead, Limit: deadMailLimit})
	if err != nil {
		h.serverError(w, r, mailQueueError("list dead emails", err))
		return
	}
	data := mailPage{
		page: h.newPage(r, "Email queue"), Stats: stats, Cap: deadMailLimit, Notice: noticeFor(r, mailNotices),
	}
	for _, job := range dead {
		row := deadMail{Info: job}
		if id, ok := strings.CutPrefix(job.Subject, orders.MailSubjectPrefix); ok {
			row.OrderID = id
		}
		data.Dead = append(data.Dead, row)
	}
	h.render(w, r, http.StatusOK, "admin_mail", data)
}

// adminMailRetry retries one dead job. A job that is no longer dead (another
// administrator retried it first, or the page is old) is said so, not reported
// as a success. The library wakes the worker when a retry succeeds.
func (h *Handler) adminMailRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.notFound(w, r)
		return
	}
	retried, err := h.outbox.Retry(r.Context(), id, h.actorID(r))
	if err != nil {
		h.serverError(w, r, mailQueueError("retry email", err))
		return
	}
	notice := "not_dead"
	if retried {
		notice = "retried"
	}
	http.Redirect(w, r, mailPath+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}
