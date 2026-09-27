package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/outbox"
	"github.com/17xande-dev/mailer"
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
		jobs := []outbox.Message{h.outbox.Encrypt(order.ID, "confirmation", plain)}
		if data.OwnerEmail != "" {
			// The owner never needs a buyer's download credentials.
			data.Downloads = nil
			plain, err = json.Marshal(data)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, h.outbox.Encrypt(order.ID, "owner", plain))
		}
		return jobs, nil
	}
}

// ProcessMail drains a bounded batch. Tests call the same worker synchronously;
// the server calls it on a timer so HTTP callbacks never wait for SMTP.
func (h *Handler) ProcessMail(ctx context.Context) {
	for range 20 {
		found, err := h.outbox.DeliverOne(ctx, h.deliverOrderEmail)
		if err != nil {
			h.log.Error("email queue", "error", err)
		}
		if !found || ctx.Err() != nil {
			return
		}
	}
}

// StartMailWorker returns a wait function so shutdown joins the worker before
// closing the database pool. Jobs survive cancellation and are retried at boot.
func (h *Handler) StartMailWorker(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			h.ProcessMail(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { <-done }
}

func (h *Handler) deliverOrderEmail(ctx context.Context, kind string, plain []byte) error {
	var data orderMailData
	if err := json.Unmarshal(plain, &data); err != nil {
		return err
	}
	if kind == "owner" {
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
	if kind != "confirmation" {
		return errors.New("unknown email kind")
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
