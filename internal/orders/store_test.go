package orders

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shop is a migrated database with one product in it, a cart, and the three
// stores that touch an order between them.
type shop struct {
	pool     *pgxpool.Pool
	catalog  *catalog.Store
	carts    *cart.Store
	orders   *Store
	cartID   string
	variants map[string]catalog.Variant
}

func newShop(t *testing.T) *shop {
	t.Helper()

	pool := dbtest.Pool(t)
	s := &shop{
		pool:     pool,
		catalog:  catalog.NewStore(pool),
		carts:    cart.NewStore(pool),
		orders:   NewStore(pool),
		variants: map[string]catalog.Variant{},
	}

	ctx := t.Context()
	p, err := s.catalog.Create(ctx, catalog.Product{Slug: "tee", Title: "Sample Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, v := range []catalog.Variant{
		{ProductID: p.ID, SKU: "TEE-S", Option1: "S", Option2: "Navy", PriceCents: 29900, StockQty: 5, Active: true},
		{ProductID: p.ID, SKU: "TEE-M", Option1: "M", PriceCents: 31900, StockQty: 2, Active: true},
	} {
		out, err := s.catalog.CreateVariant(ctx, v)
		if err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
		s.variants[v.SKU] = out
	}

	if s.cartID, err = s.carts.Create(ctx); err != nil {
		t.Fatalf("cart Create: %v", err)
	}
	return s
}

func (s *shop) add(t *testing.T, sku string, quantity int) {
	t.Helper()
	if err := s.carts.Add(t.Context(), s.cartID, s.variants[sku].ID, quantity); err != nil {
		t.Fatalf("add %s: %v", sku, err)
	}
}

func (s *shop) stock(t *testing.T, sku string) int {
	t.Helper()
	var qty int
	err := s.pool.QueryRow(t.Context(),
		`SELECT stock_qty FROM product_variants WHERE sku = $1`, sku).Scan(&qty)
	if err != nil {
		t.Fatalf("read stock for %s: %v", sku, err)
	}
	return qty
}

func testCustomer() Customer {
	return Customer{
		Name:    "Jane Doe",
		Email:   "jane@example.com",
		Phone:   "+27 11 555 0100",
		Address: "1 Example Road\nExampletown",
	}
}

func paidNotification(amount string) Payment {
	return Payment{
		Gateway: "fake",
		Ref:     "1089250",
		Status:  "COMPLETE",
		Amount:  amount,
		Raw:     "m_payment_id=...&signature=...",
	}
}

func TestCreateFromCart_SnapshotsWhatWasBought(t *testing.T) {
	s := newShop(t)
	s.add(t, "TEE-S", 2)
	s.add(t, "TEE-M", 1)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	if o.Status != StatusPending {
		t.Errorf("Status = %q, want pending — only a gateway notification pays for anything", o.Status)
	}
	if o.TotalCents != 2*29900+31900 {
		t.Errorf("TotalCents = %d, want %d", o.TotalCents, 2*29900+31900)
	}
	if o.Count() != 3 {
		t.Errorf("Count() = %d, want 3", o.Count())
	}
	if o.Reference() == "" || o.Reference() == o.ID {
		t.Errorf("Reference() = %q, want a short quotable form of %q", o.Reference(), o.ID)
	}

	// Read it back: what is in the database is what matters, not what the call
	// returned.
	stored, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Customer != testCustomer() {
		t.Errorf("Customer = %+v", stored.Customer)
	}
	if stored.TotalCents != o.TotalCents || stored.Currency != "ZAR" || stored.Gateway != "fake" {
		t.Errorf("stored order = %+v", stored)
	}
	if !stored.PaidAt.IsZero() {
		t.Error("paid_at is set on a pending order")
	}
	if len(stored.Items) != 2 {
		t.Fatalf("%d items, want 2", len(stored.Items))
	}

	// The snapshot carries everything needed to describe the purchase without
	// reading the catalog again.
	var navy Item
	for _, i := range stored.Items {
		if i.Label() == "S / Navy" {
			navy = i
		}
	}
	if navy.Title != "Sample Tee" || navy.UnitPriceCents != 29900 || navy.Quantity != 2 {
		t.Errorf("the S / Navy line is %+v, want the title, options and price copied onto it", navy)
	}

	// Stock does not move at checkout. It moves when the money arrives, so an
	// abandoned checkout cannot quietly hold inventory forever.
	if got := s.stock(t, "TEE-S"); got != 5 {
		t.Errorf("stock after checkout = %d, want 5 — nothing is paid for yet", got)
	}

	// The cart survives, so a shopper who backs out of the payment page still has
	// their basket.
	c, err := s.carts.Get(t.Context(), s.cartID)
	if err != nil || len(c.Items) != 2 {
		t.Errorf("cart after checkout = %+v, %v", c.Items, err)
	}
}

func TestCreateFromCart_PricesFromTheCatalogNotThePage(t *testing.T) {
	// The total is the figure the gateway will be asked for and the figure a
	// notification is checked against, so it has to come from the catalog at the
	// moment of ordering — not from whatever the shopper's page was showing.
	s := newShop(t)
	s.add(t, "TEE-S", 2)

	v := s.variants["TEE-S"]
	v.PriceCents = 39900
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}
	if o.TotalCents != 2*39900 {
		t.Errorf("TotalCents = %d, want the new price %d", o.TotalCents, 2*39900)
	}
	if o.Items[0].UnitPriceCents != 39900 {
		t.Errorf("unit price = %d, want the price at the moment of ordering", o.Items[0].UnitPriceCents)
	}
}

func TestCreateFromCart_RefusesWhatCannotBeBought(t *testing.T) {
	t.Run("empty cart", func(t *testing.T) {
		s := newShop(t)
		if _, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake"); !errors.Is(err, ErrEmptyCart) {
			t.Errorf("error = %v, want ErrEmptyCart", err)
		}
	})

	t.Run("withdrawn variant", func(t *testing.T) {
		s := newShop(t)
		s.add(t, "TEE-S", 1)

		// Withdrawn between the cart page being rendered and the form being
		// submitted, which is the whole reason this check exists here as well as on
		// the cart page.
		v := s.variants["TEE-S"]
		v.Active = false
		if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
			t.Fatalf("UpdateVariant: %v", err)
		}

		_, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v, want *UnavailableError", err)
		}
		if len(unavailable.Problems) != 1 || !strings.Contains(unavailable.Problems[0], "no longer for sale") {
			t.Errorf("Problems = %v, want it to explain the problem in the shopper's terms", unavailable.Problems)
		}
	})

	t.Run("more than the stock", func(t *testing.T) {
		s := newShop(t)
		s.add(t, "TEE-M", 2)

		// Somebody else bought one first.
		v := s.variants["TEE-M"]
		v.StockQty = 1
		if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
			t.Fatalf("UpdateVariant: %v", err)
		}

		_, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("error = %v, want *UnavailableError", err)
		}
		if len(unavailable.Problems) != 1 || !strings.Contains(unavailable.Problems[0], "only has 1 left") {
			t.Errorf("Problems = %v", unavailable.Problems)
		}
	})

	t.Run("writes nothing when it refuses", func(t *testing.T) {
		s := newShop(t)
		s.add(t, "TEE-S", 1)
		v := s.variants["TEE-S"]
		v.Active = false
		if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
			t.Fatalf("UpdateVariant: %v", err)
		}
		if _, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake"); err == nil {
			t.Fatal("expected a refusal")
		}

		var n int
		if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM orders`).Scan(&n); err != nil {
			t.Fatalf("count orders: %v", err)
		}
		if n != 0 {
			t.Errorf("%d orders were written by a refused checkout", n)
		}
	})
}

func TestMarkPaid_MarksPaidAndDecrementsStock(t *testing.T) {
	s := newShop(t)
	s.add(t, "TEE-S", 2)
	s.add(t, "TEE-M", 1)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	result, err := s.orders.MarkPaid(t.Context(), o.ID, paidNotification("897.00"))
	if err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if result.AlreadyPaid {
		t.Error("a first notification was treated as a replay")
	}
	if len(result.Oversold) != 0 {
		t.Errorf("Oversold = %v with stock to spare", result.Oversold)
	}

	paid, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !paid.Paid() {
		t.Errorf("Status = %q, want paid", paid.Status)
	}
	if paid.PaidAt.IsZero() {
		t.Error("paid_at was not set")
	}
	// The gateway's own words are kept verbatim: this is the audit trail a dispute
	// is settled from.
	if paid.GatewayRef != "1089250" || paid.GatewayStatus != "COMPLETE" || paid.GatewayAmount != "897.00" {
		t.Errorf("gateway columns = %+v", paid)
	}
	if paid.GatewayPayload == "" {
		t.Error("the raw notification body was not stored")
	}
	if paid.Emailed {
		t.Error("emailed is set, but nothing has been sent")
	}

	if got := s.stock(t, "TEE-S"); got != 3 {
		t.Errorf("TEE-S stock = %d, want 3", got)
	}
	if got := s.stock(t, "TEE-M"); got != 1 {
		t.Errorf("TEE-M stock = %d, want 1", got)
	}

	// The basket has become an order, so it is emptied — but the cart row stays, so
	// the shopper's cookie still works next visit.
	c, err := s.carts.Get(t.Context(), s.cartID)
	if err != nil {
		t.Fatalf("cart Get: %v", err)
	}
	if len(c.Items) != 0 {
		t.Errorf("the cart still holds %d lines after payment", len(c.Items))
	}
}

func TestMarkPaid_IdempotentOnReplay(t *testing.T) {
	// Gateways retry. A retry must not sell the same stock twice, and this is the
	// single most important property in the package.
	s := newShop(t)
	s.add(t, "TEE-S", 2)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	if _, err := s.orders.MarkPaid(t.Context(), o.ID, paidNotification("598.00")); err != nil {
		t.Fatalf("first MarkPaid: %v", err)
	}
	before := s.stock(t, "TEE-S")

	for i := range 3 {
		result, err := s.orders.MarkPaid(t.Context(), o.ID, paidNotification("598.00"))
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if !result.AlreadyPaid {
			t.Errorf("replay %d was not reported as one", i)
		}
	}
	if got := s.stock(t, "TEE-S"); got != before {
		t.Errorf("stock moved on a replay: %d, want %d", got, before)
	}
}

func TestMarkPaid_ConcurrentNotificationsDoNotDoubleDecrement(t *testing.T) {
	// The same thing again, but racing: two notifications arriving at once must not
	// both read the old stock. The row lock inside the transaction is what stops
	// them, so it is worth testing rather than asserting.
	s := newShop(t)
	s.add(t, "TEE-S", 2)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	const racers = 6
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		applied int
		errs    []error
	)
	// Not t.Context(): several goroutines share it, and it must outlive none of
	// them being finished.
	ctx := context.Background()
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.orders.MarkPaid(ctx, o.ID, paidNotification("598.00"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if !result.AlreadyPaid {
				applied++
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent MarkPaid: %v", err)
	}
	if applied != 1 {
		t.Errorf("%d of %d notifications were applied, want exactly 1", applied, racers)
	}
	if got := s.stock(t, "TEE-S"); got != 3 {
		t.Errorf("stock = %d, want 3 — it was decremented more than once", got)
	}
}

func TestMarkPaid_RecordsTheSaleEvenWhenOversold(t *testing.T) {
	// Stock ran out between checkout and payment. The money has been taken, so the
	// order stands: refusing to record it would lose the sale *and* still be
	// oversold. The result flags it for a human instead.
	s := newShop(t)
	s.add(t, "TEE-M", 2)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	v := s.variants["TEE-M"]
	v.StockQty = 1
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	result, err := s.orders.MarkPaid(t.Context(), o.ID, paidNotification("638.00"))
	if err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if len(result.Oversold) != 1 || result.Oversold[0] != "Sample Tee" {
		t.Errorf("Oversold = %v, want the one line that could not be decremented", result.Oversold)
	}

	paid, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !paid.Paid() {
		t.Error("the order was not recorded paid, so the money is unaccounted for")
	}
	// Stock is left where it was rather than going negative: the column forbids it,
	// and a negative count would be a second wrong answer on top of the first.
	if got := s.stock(t, "TEE-M"); got != 1 {
		t.Errorf("stock = %d, want it left at 1 rather than driven negative", got)
	}
}

func TestMarkPaid_UnknownOrder(t *testing.T) {
	s := newShop(t)

	for _, id := range []string{"3f2504e0-4f89-41d3-9a0c-0305e82c3301", "not-a-uuid"} {
		if _, err := s.orders.MarkPaid(t.Context(), id, paidNotification("1.00")); !errors.Is(err, ErrNotFound) {
			t.Errorf("MarkPaid(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestRecordUnpaid(t *testing.T) {
	s := newShop(t)
	s.add(t, "TEE-S", 1)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	p := Payment{Gateway: "fake", Status: "CANCELLED", Amount: "299.00", Raw: "raw"}
	if err := s.orders.RecordUnpaid(t.Context(), o.ID, StatusCancelled, p); err != nil {
		t.Fatalf("RecordUnpaid: %v", err)
	}

	got, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCancelled || got.GatewayStatus != "CANCELLED" {
		t.Errorf("order = %+v", got)
	}
	// Nothing was paid, so nothing left inventory.
	if stock := s.stock(t, "TEE-S"); stock != 5 {
		t.Errorf("stock = %d, want 5", stock)
	}

	// A paid order is never contradicted: a late failure notification arriving
	// after a genuine completion must not un-sell something already being packed.
	if _, err := s.orders.MarkPaid(t.Context(), o.ID, paidNotification("299.00")); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := s.orders.RecordUnpaid(t.Context(), o.ID, StatusFailed, Payment{Gateway: "fake", Status: "FAILED"}); err != nil {
		t.Fatalf("late RecordUnpaid: %v", err)
	}
	after, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.Paid() {
		t.Error("a late failure notification unpaid a paid order")
	}
	if after.GatewayStatus != "COMPLETE" {
		t.Errorf("gateway_status = %q, want the completion left intact", after.GatewayStatus)
	}

	if err := s.orders.RecordUnpaid(t.Context(), o.ID, StatusPaid, p); err == nil {
		t.Error("RecordUnpaid accepted StatusPaid; that path has to go through MarkPaid")
	}
}

func TestRecordNotification_LeavesTheStatusAlone(t *testing.T) {
	// For the notification that is genuine but cannot be acted on — the amount paid
	// not matching the amount asked for. Calling that order failed would misdescribe
	// it, so only the payload is filed.
	s := newShop(t)
	s.add(t, "TEE-S", 1)

	o, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}

	err = s.orders.RecordNotification(t.Context(), o.ID, Payment{
		Gateway: "fake", Ref: "1089250", Status: "COMPLETE", Amount: "1.00", Raw: "raw body",
	})
	if err != nil {
		t.Fatalf("RecordNotification: %v", err)
	}

	got, err := s.orders.Get(t.Context(), o.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want it untouched at pending", got.Status)
	}
	if got.GatewayAmount != "1.00" || got.GatewayPayload != "raw body" {
		t.Errorf("the notification was not recorded: %+v", got)
	}
	if stock := s.stock(t, "TEE-S"); stock != 5 {
		t.Errorf("stock = %d, want 5", stock)
	}
}

func TestLatestForCart(t *testing.T) {
	s := newShop(t)
	s.add(t, "TEE-S", 1)

	if _, err := s.orders.LatestForCart(t.Context(), s.cartID); !errors.Is(err, ErrNotFound) {
		t.Errorf("with no orders: %v, want ErrNotFound", err)
	}
	if _, err := s.orders.LatestForCart(t.Context(), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("with no cart token: %v, want ErrNotFound", err)
	}

	first, err := s.orders.CreateFromCart(t.Context(), s.cartID, testCustomer(), "ZAR", "fake")
	if err != nil {
		t.Fatalf("CreateFromCart: %v", err)
	}
	got, err := s.orders.LatestForCart(t.Context(), s.cartID)
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("LatestForCart = %s, want %s", got.ID, first.ID)
	}
	if len(got.Items) != 1 {
		t.Errorf("%d items, want the order's lines attached", len(got.Items))
	}
}

func TestCustomer_NameSplitForGateways(t *testing.T) {
	// Gateways insist on two names. Splitting is crude and only happens at that
	// boundary — the store itself asks for one name and stores what it is given.
	cases := []struct{ name, first, last string }{
		{"Jane Doe", "Jane", "Doe"},
		{"Jane van der Merwe", "Jane van der", "Merwe"},
		{"Prince", "Prince", ""},
		{"  Jane Doe  ", "Jane", "Doe"},
		{"", "", ""},
	}
	for _, tc := range cases {
		c := Customer{Name: tc.name}
		if got := c.FirstName(); got != tc.first {
			t.Errorf("FirstName(%q) = %q, want %q", tc.name, got, tc.first)
		}
		if got := c.LastName(); got != tc.last {
			t.Errorf("LastName(%q) = %q, want %q", tc.name, got, tc.last)
		}
	}
}
