package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
)

// maxCallbackBytes caps what this endpoint will read. A gateway's notification is
// well under a kilobyte, and this route is unauthenticated until the gateway has
// vouched for the body — so the limit is applied while reading, not after. Each
// gateway enforces its own limit as well; this one belongs to the handler, so the
// handler stays free of any particular gateway's package.
const maxCallbackBytes = 64 << 10

// The gateway callback is the highest-stakes surface in the system: it is
// unauthenticated by definition — a payment provider cannot be given a session or
// a CSRF token — and it is the only thing that can decide money has changed hands.
// Everything about it follows from those two facts.
//
//   - It is registered outside the CSRF group. Not by an exempt-path string that
//     has to keep matching the route, but by being mounted on the server's own mux
//     rather than the first-party one. A route cannot drift out of an exemption it
//     was never inside.
//   - The gateway authenticates the notification; this handler does not try to.
//     ParseCallback returning without an error is the proof, and there is no code
//     path here that acts on an unproven one.
//   - Permanent rejections and completed transactions answer 200. Temporary
//     verification or persistence failures answer 503 so the provider retries.
//   - What only this handler can do, it does: find the order, check the amount
//     against the order's own total, and keep a replay from decrementing stock
//     twice.

// RegisterPayments wires the gateway callback. It takes its own mux registration
// on purpose — see the comment above.
func (h *Handler) RegisterPayments(mux *http.ServeMux) {
	// Rate limited, and this is the surface the limiter exists for: unauthenticated,
	// and every accepted request makes the store POST to the gateway to validate it,
	// which is an amplifier.
	//
	// A throttled request has not been processed: 429 with Retry-After lets the
	// provider try again, just like the handler's 503 on a temporary failure.
	mux.Handle("POST /payments/{gateway}/callback", h.limits.callback(http.HandlerFunc(h.paymentCallback)))
}

// RegisterMCP mounts the MCP endpoint, built by internal/mcpserver, at /mcp.
//
// On the outer mux with the payment callback, not in FirstPartyHandler: it
// authenticates with a bearer token and carries no cookie, so there is nothing
// for CSRF to protect and nosurf would only refuse every call. The limiter runs
// before the token is checked, so it bounds guessing as well as a runaway agent.
//
// uploads receives the bytes for the image upload URLs the endpoint issues, at
// uploadPath followed by the URL's token — the path is the MCP server's to say,
// since it builds the URLs. Behind the same limiter, which runs before the
// upload is looked up, for the same reason.
func (h *Handler) RegisterMCP(mux *http.ServeMux, endpoint http.Handler, uploadPath string, uploads http.Handler) {
	mux.Handle("/mcp", h.limits.mcp(endpoint))
	mux.Handle("PUT "+uploadPath+"{token}", h.limits.mcp(uploads))
	mux.Handle("POST "+uploadPath+"{token}", h.limits.mcp(uploads))
}

func (h *Handler) paymentCallback(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	defer func() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(status)
	}()

	// The {gateway} segment names which provider is claiming to have taken money,
	// and only a configured one is listened to.
	name := r.PathValue("gateway")
	gateway, err := h.gateways.Lookup(name)
	if err != nil {
		h.log.Warn("payment callback for an unknown gateway", "gateway", name, "remote", r.RemoteAddr)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxCallbackBytes+1))
	if err != nil {
		h.log.Error("payment callback: read body", "error", err)
		status = http.StatusServiceUnavailable
		return
	}

	sourceIP := middleware.ClientIP(r, h.cfg.ClientIPSource)
	cb, err := gateway.ParseCallback(r.Context(), payment.Notification{
		Body: body, Header: r.Header, SourceIP: sourceIP,
	})
	if err != nil {
		if errors.Is(err, payment.ErrRetryable) {
			status = http.StatusServiceUnavailable
		}
		// Which check failed is the whole diagnostic value of this log line: a
		// signature mismatch is usually a passphrase that disagrees with the
		// dashboard, an IP rejection is usually a proxy or a changed range, and a
		// failed confirmation is usually neither.
		h.log.Warn("rejected payment callback",
			"gateway", gateway.Name(), "source_ip", sourceIP, "error", err, "body_bytes", len(body))
		return
	}

	if err := h.applyCallback(r, gateway, cb); err != nil {
		status = http.StatusServiceUnavailable
	}
}

// applyCallback is everything that happens once a notification is proven genuine.
func (h *Handler) applyCallback(r *http.Request, gateway payment.Gateway, cb payment.Callback) error {
	log := h.log.With("gateway", gateway.Name(), "order", cb.OrderID,
		"gateway_ref", cb.Ref, "gateway_status", cb.Status)

	order, err := h.orders.Get(r.Context(), cb.OrderID)
	if err != nil {
		if errors.Is(err, orders.ErrNotFound) {
			// A genuine notification for an order this store has never heard of.
			// Worth looking at: most likely two deployments sharing one merchant
			// account, which is how one store's payments get confirmed against
			// another's database.
			log.Warn("payment callback names an unknown order")
			return nil
		}
		log.Error("payment callback: read order", "error", err)
		return err
	}

	// A gateway only ever proves things about its own account, so a genuine
	// notification from one provider must not be allowed to settle an order
	// placed through another. Without this check, anybody able to pay a snap code
	// could close out a PayFast order by quoting its reference.
	if order.Gateway != gateway.Name() {
		log.Warn("payment callback names an order placed through a different gateway",
			"order_gateway", order.Gateway)
		return nil
	}

	p := orders.Payment{
		Gateway: gateway.Name(),
		Ref:     cb.Ref,
		Status:  cb.Status,
		Amount:  cb.Amount,
		Raw:     string(cb.Raw),
	}

	if !cb.Paid() {
		// Cancelled, failed, or still pending at the gateway. Recorded, never acted
		// on, and never allowed to contradict a payment that already succeeded.
		status := unpaidStatus(cb.Outcome)
		if err := h.orders.RecordUnpaid(r.Context(), order.ID, status, p); err != nil {
			log.Error("record unpaid order", "error", err)
			return err
		}
		log.Info("payment did not complete", "status", status)
		return nil
	}

	// The amount is checked against the order's own total, which was computed from
	// the catalog inside the transaction that created it. A mismatch means the
	// figure paid is not the figure this store asked for, so the order is not
	// credited: crediting it would be trusting a number this store never quoted.
	//
	// The status is left exactly as it was rather than being called failed — the
	// gateway said COMPLETE, so something was paid, and "failed" would read as the
	// customer's card being declined. Only the notification is recorded, which is
	// what the person reconciling this needs.
	if cb.AmountCents != order.TotalCents {
		log.Error("payment amount does not match the order; NOT marking it paid",
			"paid_cents", cb.AmountCents, "order_cents", order.TotalCents, "paid_amount", cb.Amount)
		if err := h.orders.RecordNotification(r.Context(), order.ID, p); err != nil {
			log.Error("record mismatched payment", "error", err)
			return err
		}
		return nil
	}

	result, err := h.orders.MarkPaidWithMail(r.Context(), order.ID, p, h.prepareOrderEmails(order))
	if err != nil {
		// The money is taken and this store failed to record it. Nothing here can
		// fix that, so it is logged at the level someone is paged for; the gateway's
		// retry is the actual recovery mechanism, and the operation is idempotent.
		log.Error("failed to mark a paid order paid", "error", err)
		return err
	}
	if result.AlreadyPaid {
		// Routine: gateways retry, and this is what stops a retry selling the same
		// stock twice.
		log.Info("ignored a replayed payment notification")
		return nil
	}

	if len(result.Oversold) > 0 {
		// The money is in, so the order stands. Somebody has to reconcile the
		// stock by hand; the owner's notification email carries this, and the
		// admin order view grows a flag for it in the hardening phase.
		log.Error("order paid but stock could not be decremented — oversold",
			"items", result.Oversold)
	}
	log.Info("order paid", "total_cents", order.TotalCents, "items", order.Count(),
		"downloads", len(result.Grants))

	// The durable worker owns delivery. The provider can stop retrying now that
	// the payment and its email jobs have committed together.
	return nil
}

// unpaidStatus maps a normalised outcome onto this store's order status.
//
// It reads payment.Outcome rather than the gateway's own words: PayFast says
// CANCELLED and SnapScan says error, and each provider maps its vocabulary in its
// own package so that adding a third does not mean editing this handler. An
// outcome this code cannot place stays pending — not knowing a payment failed is
// not the same as knowing it did.
func unpaidStatus(o payment.Outcome) orders.Status {
	switch o {
	case payment.OutcomeCancelled:
		return orders.StatusCancelled
	case payment.OutcomeFailed:
		return orders.StatusFailed
	default:
		return orders.StatusPending
	}
}
