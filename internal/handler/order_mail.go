package handler

import (
	"context"

	"github.com/17xande-dev/gostore/internal/email"
	"github.com/17xande-dev/gostore/internal/orders"
)

// Mail for a paid order, and the one invariant that governs all of it: **the order
// is already recorded paid before any of this runs.** A mail server that is down,
// slow, or misconfigured must never be able to lose a sale, so nothing here can
// fail the payment callback and nothing here is retried by failing it.
//
// Two messages go out, and they are deliberately separate sends rather than one
// message with two recipients: the customer's copy is a receipt and the owner's is
// a work order, they say different things, and one of them failing should not
// suppress the other.
//
//   - The customer gets a confirmation, once. orders.emailed records that, so a
//     replayed gateway notification does not send a second receipt.
//   - Whoever packs the parcel gets a notification, if ORDER_NOTIFY_EMAIL is set.
//     It also carries the oversell warning, which otherwise only exists in the
//     logs — the person who has to tell a customer their item is not in stock
//     after all should not have to find that in a log aggregator.

// orderMailData is what both order emails render from.
type orderMailData struct {
	StoreName string
	Currency  string
	BaseURL   string
	Order     orders.Order

	// Oversold names the lines whose stock could not be decremented. Only the
	// owner's copy shows it; telling a customer their order may not be
	// deliverable, in the same breath as confirming it, is not the way to find
	// out.
	Oversold []string
}

// sendOrderEmails delivers the receipt and the notification for a paid order. It
// returns nothing: every outcome here is logged and none of them changes what the
// caller does.
func (h *Handler) sendOrderEmails(ctx context.Context, order orders.Order, oversold []string) {
	data := orderMailData{
		StoreName: h.cfg.StoreName,
		Currency:  h.cfg.Currency,
		BaseURL:   h.cfg.BaseURL,
		Order:     order,
		Oversold:  oversold,
	}
	log := h.log.With("order", order.ID)

	if order.Emailed {
		// A replay, or a retry after the notification email failed. Either way the
		// customer already has their receipt.
		log.Info("confirmation already sent; not sending another")
	} else if err := h.sendConfirmation(ctx, data); err != nil {
		// Logged and dropped. The order stands, and the customer has already seen
		// a confirmation page with their reference on it.
		log.Error("failed to send the order confirmation", "to", order.Customer.Email, "error", err)
	} else if err := h.orders.MarkEmailed(ctx, order.ID); err != nil {
		// The mail went out but the flag did not stick, so a retry would send a
		// second copy. Worth logging loudly and not worth failing over.
		log.Error("sent the confirmation but failed to record it", "error", err)
	} else {
		log.Info("sent the order confirmation", "to", order.Customer.Email)
	}

	if h.cfg.OrderNotifyEmail == "" {
		return
	}
	if err := h.sendOwnerNotification(ctx, data); err != nil {
		log.Error("failed to notify the store owner", "to", h.cfg.OrderNotifyEmail, "error", err)
		return
	}
	log.Info("notified the store owner", "to", h.cfg.OrderNotifyEmail)
}

func (h *Handler) sendConfirmation(ctx context.Context, data orderMailData) error {
	text, err := h.tmpl.Text("email_order_paid.txt", data)
	if err != nil {
		return err
	}
	html, err := h.tmpl.String("email_order_paid", data)
	if err != nil {
		return err
	}
	return h.mail.Send(ctx, email.Message{
		To:      data.Order.Customer.Email,
		Subject: data.StoreName + " order " + data.Order.Reference() + " — payment received",
		Text:    text,
		HTML:    html,
	})
}

func (h *Handler) sendOwnerNotification(ctx context.Context, data orderMailData) error {
	text, err := h.tmpl.Text("email_order_notify.txt", data)
	if err != nil {
		return err
	}
	subject := "New order " + data.Order.Reference() + " — " + data.Currency + " " +
		formatCents(data.Order.TotalCents)
	if len(data.Oversold) > 0 {
		// In the subject, because it is the one thing on this page that needs
		// acting on before the parcel is packed.
		subject = "OVERSOLD: " + subject
	}
	return h.mail.Send(ctx, email.Message{
		To:      h.cfg.OrderNotifyEmail,
		Subject: subject,
		Text:    text,
	})
}
