package handler

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/auth"
)

func TestAdminOrders_FulfillmentDoesNotChangePayment(t *testing.T) {
	s := newCheckoutShop(t)
	id := paidOrder(t, s)
	before := s.reload(t, id)
	signIn(t, s.srv)
	_, body := get(t, s.srv, "/admin/orders?filter=unfulfilled")
	if !strings.Contains(body, id) {
		t.Fatal("paid parcel missing from fulfillment queue")
	}
	path := "/admin/orders/" + id + "/fulfillment"
	if res, _ := post(t, s.srv, path, url.Values{}); res.StatusCode != http.StatusBadRequest {
		t.Fatal("absent state was accepted")
	}
	res, body := post(t, s.srv, path, url.Values{"fulfilled": {"1"}, "tracking": {"PARCEL-123"}, "note": {"Packed by reception"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("fulfillment: %d %s", res.StatusCode, body)
	}
	after := s.reload(t, id)
	if after.FulfilledAt == nil || after.TrackingReference != "PARCEL-123" || after.InternalNote != "Packed by reception" {
		t.Fatalf("fulfillment not saved: %+v", after)
	}
	if after.Status != before.Status || after.GatewayPayload != before.GatewayPayload || after.TotalCents != before.TotalCents {
		t.Fatal("fulfillment rewrote payment")
	}
	_, body = get(t, s.srv, "/admin/orders?filter=unfulfilled")
	if strings.Contains(body, id) {
		t.Fatal("fulfilled order still needs packing")
	}
	_, body = get(t, s.srv, "/admin/orders?q="+before.Reference())
	if !strings.Contains(body, id) {
		t.Fatal("short reference search did not find order")
	}
	mustAccount(t, s, "reader@example.com", testPassword, auth.RoleViewer)
	signInAs(t, s.srv, "reader@example.com", testPassword)
	if res, _ := post(t, s.srv, path, url.Values{"fulfilled": {"0"}}); res.StatusCode != http.StatusForbidden {
		t.Fatal("viewer changed fulfillment")
	}
}

func TestAdminOrders_SearchReachesBeyondFirstPage(t *testing.T) {
	s := newCheckoutShop(t)
	_, err := s.pool.Exec(t.Context(), `INSERT INTO orders (id, customer_name, customer_email, total_cents, currency, created_at)
SELECT gen_random_uuid(), 'Buyer ' || n, 'buyer' || n || '@example.com', 100, 'ZAR', now() - n * interval '1 minute'
FROM generate_series(1, 55) n`)
	if err != nil {
		t.Fatal(err)
	}
	signIn(t, s.srv)
	_, body := get(t, s.srv, "/admin/orders")
	if !strings.Contains(body, "Next page") || strings.Contains(body, "buyer55@example.com") {
		t.Fatal("first page is not bounded")
	}
	_, body = get(t, s.srv, "/admin/orders?page=2")
	if !strings.Contains(body, "buyer55@example.com") || strings.Contains(body, "Next page") {
		t.Fatal("older order cannot be paged to")
	}
	_, body = get(t, s.srv, "/admin/orders?q=buyer55%40example.com")
	if !strings.Contains(body, "buyer55@example.com") || strings.Contains(body, "buyer54@example.com") {
		t.Fatal("email search did not narrow results")
	}
}
