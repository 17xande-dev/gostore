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
//   - **It always answers 200.** A gateway retries anything else, and a
//     notification that fails validation is not "try again later" — it is either
//     forged or broken, and neither improves on the third attempt. Rejections are
//     logged, in full, and dropped.
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
	// The limiter answers 429, which looks like it contradicts this handler's
	// always-200 rule. It does not: 200 means "read and decided", so a gateway does
	// not retry a forgery. A throttled request has not been read, and a retry is
	// exactly what should happen — hence 429 with Retry-After, from in front of the
	// handler rather than inside it.
	mux.Handle("POST /payments/{gateway}/callback", h.limits.callback(http.HandlerFunc(h.paymentCallback)))
}

func (h *Handler) paymentCallback(w http.ResponseWriter, r *http.Request) {
	// Whatever happens below, the answer is 200 and an empty body. Deferring it
	// means no early return can accidentally leave a gateway retrying a
	// notification this store has already decided to ignore.
	defer func() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	}()

	// The {gateway} segment keeps the route stable if a second gateway is ever
	// added. Today exactly one name matches.
	if name := r.PathValue("gateway"); name != h.gateway.Name() {
		h.log.Warn("payment callback for an unknown gateway", "gateway", name, "remote", r.RemoteAddr)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxCallbackBytes+1))
	if err != nil {
		h.log.Error("payment callback: read body", "error", err)
		return
	}

	sourceIP := middleware.ClientIP(r, h.cfg.TrustProxyIP)
	cb, err := h.gateway.ParseCallback(r.Context(), body, sourceIP)
	if err != nil {
		// Which check failed is the whole diagnostic value of this log line: a
		// signature mismatch is usually a passphrase that disagrees with the
		// dashboard, an IP rejection is usually a proxy or a changed range, and a
		// failed confirmation is usually neither.
		h.log.Warn("rejected payment callback",
			"gateway", h.gateway.Name(), "source_ip", sourceIP, "error", err, "body_bytes", len(body))
		return
	}

	h.applyCallback(r, cb)
}

// applyCallback is everything that happens once a notification is proven genuine.
func (h *Handler) applyCallback(r *http.Request, cb payment.Callback) {
	log := h.log.With("gateway", h.gateway.Name(), "order", cb.OrderID,
		"gateway_ref", cb.Ref, "gateway_status", cb.Status)

	order, err := h.orders.Get(r.Context(), cb.OrderID)
	if err != nil {
		if errors.Is(err, orders.ErrNotFound) {
			// A genuine notification for an order this store has never heard of.
			// Worth looking at: most likely two deployments sharing one merchant
			// account, which is how one store's payments get confirmed against
			// another's database.
			log.Warn("payment callback names an unknown order")
			return
		}
		log.Error("payment callback: read order", "error", err)
		return
	}

	p := orders.Payment{
		Gateway: h.gateway.Name(),
		Ref:     cb.Ref,
		Status:  cb.Status,
		Amount:  cb.Amount,
		Raw:     string(cb.Raw),
	}

	if !cb.Paid {
		// Cancelled, failed, or still pending at the gateway. Recorded, never acted
		// on, and never allowed to contradict a payment that already succeeded.
		status := unpaidStatus(cb.Status)
		if err := h.orders.RecordUnpaid(r.Context(), order.ID, status, p); err != nil {
			log.Error("record unpaid order", "error", err)
			return
		}
		log.Info("payment did not complete", "status", status)
		return
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
		}
		return
	}

	result, err := h.orders.MarkPaid(r.Context(), order.ID, p)
	if err != nil {
		// The money is taken and this store failed to record it. Nothing here can
		// fix that, so it is logged at the level someone is paged for; the gateway's
		// retry is the actual recovery mechanism, and the operation is idempotent.
		log.Error("failed to mark a paid order paid", "error", err)
		return
	}
	if result.AlreadyPaid {
		// Routine: gateways retry, and this is what stops a retry selling the same
		// stock twice.
		log.Info("ignored a replayed payment notification")
		return
	}

	if len(result.Oversold) > 0 {
		// The money is in, so the order stands. Somebody has to reconcile the
		// stock by hand; the owner's notification email carries this, and the
		// admin order view grows a flag for it in the hardening phase.
		log.Error("order paid but stock could not be decremented — oversold",
			"items", result.Oversold)
	}
	log.Info("order paid", "total_cents", order.TotalCents, "items", order.Count())

	// Mail last, and only now that the order is recorded paid: a mail server
	// having a bad afternoon must not be able to lose a sale. Nothing below can
	// fail this request — see internal/handler/order_mail.go.
	h.sendOrderEmails(r.Context(), order, result.Oversold)
}

// unpaidStatus maps a gateway's own vocabulary onto this store's. Anything
// unrecognised stays pending rather than being called a failure: a status this
// code has not seen before is not evidence that a payment will not arrive.
func unpaidStatus(gatewayStatus string) orders.Status {
	switch gatewayStatus {
	case "CANCELLED", "cancelled":
		return orders.StatusCancelled
	case "FAILED", "failed":
		return orders.StatusFailed
	default:
		return orders.StatusPending
	}
}
