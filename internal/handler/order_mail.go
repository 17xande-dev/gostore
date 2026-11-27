package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/outbox"
	"github.com/jackc/pgx/v5"
)

type orderMailData struct {
	StoreName  string
	Currency   string
	BaseURL    string
	Order      orders.Order
	Oversold   []string
	Downloads  []DownloadLink
	OwnerEmail string
}

type DownloadLink struct{ Title, Label, URL string }

const (
	kindConfirmation = "confirmation"
	kindOwner        = "owner"
)

// newMailQueue registers the two order emails. The handlers are this handler's
// methods, so the queue is built here rather than by main.
func (h *Handler) newMailQueue(store outbox.Store[pgx.Tx], opts outbox.Options) (*outbox.Queue[pgx.Tx], error) {
	// A transport or template error can include an address or a download URL.
	opts.Describe = func(error) string {
		return "Email delivery failed; check mail configuration and retry."
	}
	email := outbox.Kind[pgx.Tx]{
		Run:     h.deliverOrderEmail,
		Timeout: func(context.Context, outbox.Job) time.Duration { return 30 * time.Second },
	}
	// The flag commits with the job's completion, so a send is never recorded as
	// "not emailed" nor the reverse: the transport's own repeat is the only gap.
	confirmation := email
	confirmation.OnDone = func(ctx context.Context, tx pgx.Tx, job outbox.Job, _ string) error {
		return gen.New(tx).MarkOrderEmailed(ctx, strings.TrimPrefix(job.Subject, orders.MailSubjectPrefix))
	}
	return outbox.New(store, map[string]outbox.Kind[pgx.Tx]{
		kindConfirmation: confirmation,
		kindOwner:        email,
	}, opts)
}

// MailQueue is the server's queue, built with the handlers above.
func (h *Handler) MailQueue() *outbox.Queue[pgx.Tx] { return h.outbox }

// prepareOrderEmails builds the jobs for a payment. The payloads carry download
// URLs with their tokens, so they are sealed by the queue's Sealer inside the
// order's transaction (enqueueOrderMail) and cleared once the mail is sent.
func (h *Handler) prepareOrderEmails(order orders.Order) func(orders.PaidResult) ([]outbox.Message, error) {
	return func(result orders.PaidResult) ([]outbox.Message, error) {
		if h.outbox == nil {
			return nil, errors.New("email queue is not configured")
		}
		data := orderMailData{
			StoreName: h.cfg.StoreName, Currency: order.Currency, BaseURL: h.cfg.BaseURL,
			Order: order, Oversold: result.Oversold, OwnerEmail: h.cfg.OrderNotifyEmail,
		}
		for _, grant := range result.Grants {
			data.Downloads = append(data.Downloads, DownloadLink{Title: grant.Title, Label: grant.VariantLabel,
				URL: h.cfg.BaseURL + "/downloads/" + grant.Token})
		}
		plain, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		jobs := []outbox.Message{orderMessage(order.ID, kindConfirmation, plain)}
		if data.OwnerEmail != "" {
			// The owner never needs a buyer's download credentials.
			data.Downloads = nil
			plain, err = json.Marshal(data)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, orderMessage(order.ID, kindOwner, plain))
		}
		return jobs, nil
	}
}

// orderMessage is one job, unique per order and kind: a replayed payment
// notification cannot queue a second receipt.
func orderMessage(orderID, kind string, payload []byte) outbox.Message {
	subject := orders.MailSubject(orderID)
	return outbox.Message{Kind: kind, Subject: subject, UniqueKey: subject + ":" + kind, Payload: payload}
}

// enqueueOrderMail runs inside the order's transaction.
func (h *Handler) enqueueOrderMail(ctx context.Context, tx pgx.Tx, m outbox.Message) error {
	_, _, err := h.outbox.Enqueue(ctx, tx, m)
	return mailQueueError("enqueue "+m.Kind+" email", err)
}

// mailQueueError reduces an error to its class: a PostgreSQL diagnostic can
// include the row holding the sealed payload.
func mailQueueError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %T", operation, err)
}

func (h *Handler) deliverOrderEmail(ctx context.Context, job outbox.Job) error {
	var data orderMailData
	if err := json.Unmarshal(job.Payload, &data); err != nil {
		// The payload was authenticated, so this is a bug, not a transient fault.
		return outbox.Permanent(errors.New("queued email payload is not valid"))
	}
	if job.Kind == kindOwner {
		text, err := h.tmpl.Text("email_order_notify.txt", data)
		if err != nil {
			return err
		}
		subject := "New order " + data.Order.Reference() + " — " + data.Currency + " " + formatCents(data.Order.TotalCents)
		if len(data.Oversold) > 0 {
			subject = "OVERSOLD: " + subject
		}
		return h.mail.Send(ctx, mailer.Message{To: []string{data.OwnerEmail}, Subject: subject, Text: text})
	}
	if job.Kind != kindConfirmation {
		return outbox.Permanent(errors.New("unknown email kind"))
	}
	text, err := h.tmpl.Text("email_order_paid.txt", data)
	if err != nil {
		return err
	}
	html, err := h.tmpl.String("email_order_paid", data)
	if err != nil {
		return err
	}
	return h.mail.Send(ctx, mailer.Message{To: []string{data.Order.Customer.Email},
		Subject: data.StoreName + " order " + data.Order.Reference() + " — payment received", Text: text, HTML: html})
}
