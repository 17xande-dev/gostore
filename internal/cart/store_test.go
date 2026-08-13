package cart_test

import (
	"errors"
	"testing"

	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	pool   *pgxpool.Pool
	carts  *cart.Store
	cat    *catalog.Store
	tee    catalog.Variant // 4 in stock
	sock   catalog.Variant // 1 in stock
	hidden catalog.Variant // active variant of an inactive product
	off    catalog.Variant // inactive variant
}

func setup(t *testing.T) fixture {
	t.Helper()

	pool := dbtest.Pool(t)
	f := fixture{pool: pool, carts: cart.NewStore(pool), cat: catalog.NewStore(pool)}
	ctx := t.Context()

	p, err := f.cat.Create(ctx, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.tee = mustVariant(t, f.cat, catalog.Variant{ProductID: p.ID, SKU: "TEE-M", Size: "M", PriceCents: 29900, StockQty: 4, Active: true})
	f.sock = mustVariant(t, f.cat, catalog.Variant{ProductID: p.ID, SKU: "TEE-S", Size: "S", PriceCents: 19900, StockQty: 1, Active: true})
	f.off = mustVariant(t, f.cat, catalog.Variant{ProductID: p.ID, SKU: "TEE-L", Size: "L", PriceCents: 39900, StockQty: 5, Active: false})

	draft, err := f.cat.Create(ctx, catalog.Product{Slug: "draft", Title: "Draft", Active: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.hidden = mustVariant(t, f.cat, catalog.Variant{ProductID: draft.ID, SKU: "DRAFT-1", PriceCents: 9900, StockQty: 3, Active: true})

	return f
}

func mustVariant(t *testing.T, s *catalog.Store, v catalog.Variant) catalog.Variant {
	t.Helper()
	out, err := s.CreateVariant(t.Context(), v)
	if err != nil {
		t.Fatalf("CreateVariant %s: %v", v.SKU, err)
	}
	return out
}

func TestNewToken(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		token, err := cart.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		// 24 random bytes, so guessing one is not a way into somebody's cart.
		if len(token) < 32 {
			t.Fatalf("token %q is only %d characters", token, len(token))
		}
		if seen[token] {
			t.Fatalf("NewToken repeated %q", token)
		}
		seen[token] = true
	}
}

func TestStore_AddAndRead(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, err := f.carts.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	empty, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !empty.Empty() || empty.Count() != 0 || empty.TotalCents() != 0 {
		t.Errorf("a new cart is not empty: %+v", empty)
	}
	if empty.Purchasable() {
		t.Error("an empty cart reports itself as purchasable")
	}

	if err := f.carts.Add(ctx, token, f.tee.ID, 2); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.carts.Add(ctx, token, f.sock.ID, 1); err != nil {
		t.Fatalf("Add: %v", err)
	}

	c, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(c.Items) != 2 {
		t.Fatalf("cart has %d lines, want 2: %+v", len(c.Items), c.Items)
	}
	if c.Count() != 3 {
		t.Errorf("Count = %d, want 3", c.Count())
	}
	if want := int64(2*29900 + 19900); c.TotalCents() != want {
		t.Errorf("TotalCents = %d, want %d", c.TotalCents(), want)
	}
	if !c.Purchasable() {
		t.Errorf("a cart of available items is not purchasable: %v", c.Problems())
	}

	// Catalog details are read live rather than copied onto the cart.
	for _, i := range c.Items {
		if i.ProductTitle != "Tee" || i.ProductSlug != "tee" {
			t.Errorf("line %+v has no product details", i)
		}
	}

	// Adding the same variant again adds to the line rather than making a second
	// one.
	if err := f.carts.Add(ctx, token, f.tee.ID, 1); err != nil {
		t.Fatalf("Add again: %v", err)
	}
	c, _ = f.carts.Get(ctx, token)
	if len(c.Items) != 2 {
		t.Errorf("adding an existing variant made %d lines", len(c.Items))
	}
	if c.Count() != 4 {
		t.Errorf("Count = %d, want 4 after adding one more", c.Count())
	}
}

func TestStore_AddRespectsStock(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, err := f.carts.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Four are in stock, so five is refused — and the error says how many there
	// are, so the shopper can be told the actual limit.
	var out *cart.OutOfStockError
	if err := f.carts.Add(ctx, token, f.tee.ID, 5); !errors.As(err, &out) {
		t.Fatalf("Add 5 of 4 = %v, want an OutOfStockError", err)
	} else if out.Available != 4 {
		t.Errorf("OutOfStockError.Available = %d, want 4", out.Available)
	}

	// Two adds of three must not smuggle six past a limit of four: the check is
	// against the resulting total, not the increment.
	if err := f.carts.Add(ctx, token, f.tee.ID, 3); err != nil {
		t.Fatalf("Add 3: %v", err)
	}
	if err := f.carts.Add(ctx, token, f.tee.ID, 3); !errors.As(err, &out) {
		t.Fatalf("second Add 3 = %v, want an OutOfStockError", err)
	}

	c, _ := f.carts.Get(ctx, token)
	if c.Count() != 3 {
		t.Errorf("cart holds %d, want the 3 that were allowed", c.Count())
	}
}

func TestStore_AddRejectsUnavailable(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, err := f.carts.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cases := map[string]string{
		"inactive variant":                 f.off.ID,
		"active variant, inactive product": f.hidden.ID,
		"unknown id":                       "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		// A malformed id must not surface as a database error.
		"not a uuid": "../../etc/passwd",
	}
	for name, variantID := range cases {
		if err := f.carts.Add(ctx, token, variantID, 1); !errors.Is(err, cart.ErrUnavailable) {
			t.Errorf("%s: Add = %v, want ErrUnavailable", name, err)
		}
	}

	for _, quantity := range []int{0, -1, cart.MaxQuantity + 1} {
		if err := f.carts.Add(ctx, token, f.tee.ID, quantity); !errors.Is(err, cart.ErrQuantity) {
			t.Errorf("Add quantity %d = %v, want ErrQuantity", quantity, err)
		}
	}

	if c, _ := f.carts.Get(ctx, token); !c.Empty() {
		t.Errorf("the cart is not empty after only refused adds: %+v", c.Items)
	}
}

func TestStore_SetQuantityAndRemove(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	if err := f.carts.Add(ctx, token, f.tee.ID, 1); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := f.carts.SetQuantity(ctx, token, f.tee.ID, 3); err != nil {
		t.Fatalf("SetQuantity: %v", err)
	}
	if c, _ := f.carts.Get(ctx, token); c.Count() != 3 {
		t.Errorf("Count = %d, want 3", c.Count())
	}

	var out *cart.OutOfStockError
	if err := f.carts.SetQuantity(ctx, token, f.tee.ID, 99); !errors.As(err, &out) {
		t.Errorf("SetQuantity beyond stock = %v, want an OutOfStockError", err)
	}
	if c, _ := f.carts.Get(ctx, token); c.Count() != 3 {
		t.Error("a refused quantity change was applied anyway")
	}

	// Zero removes the line, which is how a remove button works with no
	// JavaScript.
	if err := f.carts.SetQuantity(ctx, token, f.tee.ID, 0); err != nil {
		t.Fatalf("SetQuantity 0: %v", err)
	}
	if c, _ := f.carts.Get(ctx, token); !c.Empty() {
		t.Error("quantity 0 did not remove the line")
	}

	// Removing what is not there is not an error: the intent is satisfied, and a
	// double-click should not produce a failure page.
	if err := f.carts.Remove(ctx, token, f.tee.ID); err != nil {
		t.Errorf("Remove of an absent line = %v, want nil", err)
	}
}

func TestStore_UnavailableItemsStayVisible(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	if err := f.carts.Add(ctx, token, f.tee.ID, 2); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The shop withdraws the variant and empties its stock after it is in the
	// cart. The line must not disappear — a vanishing line reads as a bug, or
	// worse, as a silent change to the total.
	f.tee.Active = false
	f.tee.StockQty = 0
	if _, err := f.cat.UpdateVariant(ctx, f.tee); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	c, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(c.Items) != 1 {
		t.Fatalf("the withdrawn line disappeared: %+v", c.Items)
	}
	item := c.Items[0]
	if item.Purchasable || item.Available() {
		t.Error("a withdrawn item reports itself as available")
	}
	if c.Purchasable() {
		t.Error("a cart containing a withdrawn item is purchasable")
	}
	if problems := c.Problems(); len(problems) != 1 {
		t.Errorf("Problems() = %v, want one complaint", problems)
	}
	// The total still counts it, because it is still on the page.
	if c.TotalCents() != 2*29900 {
		t.Errorf("TotalCents = %d, want the line still counted", c.TotalCents())
	}
}

func TestStore_PricesAreLive(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	if err := f.carts.Add(ctx, token, f.tee.ID, 2); err != nil {
		t.Fatalf("Add: %v", err)
	}

	f.tee.PriceCents = 34900
	if _, err := f.cat.UpdateVariant(ctx, f.tee); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	// The cart holds a quantity, not a price. Snapshotting happens when the order
	// is created, not before.
	c, _ := f.carts.Get(ctx, token)
	if c.Items[0].UnitPriceCents != 34900 {
		t.Errorf("unit price = %d, want the current 34900", c.Items[0].UnitPriceCents)
	}
	if c.TotalCents() != 2*34900 {
		t.Errorf("TotalCents = %d, want %d", c.TotalCents(), 2*34900)
	}
}

func TestStore_UnknownToken(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := f.carts.Get(ctx, token); !errors.Is(err, cart.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", token, err)
		}
	}

	// Adding against a token whose cart was cleaned up reports ErrNotFound, so
	// the handler can start a fresh cart instead of failing.
	if err := f.carts.Add(ctx, "not-a-real-token", f.tee.ID, 1); !errors.Is(err, cart.ErrNotFound) {
		t.Errorf("Add with an unknown token = %v, want ErrNotFound", err)
	}
}

func TestStore_DeletingAVariantEmptiesItFromCarts(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	if err := f.carts.Add(ctx, token, f.tee.ID, 1); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// An abandoned cart must not stop the shop owner from deleting a variant —
	// which is why cart_items cascades and order_items does not.
	if err := f.cat.DeleteVariant(ctx, f.tee.ProductID, f.tee.ID); err != nil {
		t.Fatalf("DeleteVariant: %v", err)
	}
	c, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !c.Empty() {
		t.Errorf("the cart still holds a deleted variant: %+v", c.Items)
	}
}

func TestStore_ClearAndCleanup(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	if err := f.carts.Add(ctx, token, f.tee.ID, 1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.carts.Clear(ctx, token); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	c, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get after Clear: %v", err)
	}
	if !c.Empty() {
		t.Error("Clear left items behind")
	}

	// A fresh cart is not old, so the cleanup must leave it alone.
	if n, err := f.carts.DeleteOlderThan(ctx, 60); err != nil || n != 0 {
		t.Errorf("DeleteOlderThan(60) = %d, %v; want it to spare a new cart", n, err)
	}

	// Age it past the window and it goes.
	if _, err := f.pool.Exec(ctx, `UPDATE carts SET updated_at = now() - interval '61 days' WHERE id = $1`, token); err != nil {
		t.Fatalf("age the cart: %v", err)
	}
	n, err := f.carts.DeleteOlderThan(ctx, 60)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteOlderThan deleted %d carts, want 1", n)
	}
	if _, err := f.carts.Get(ctx, token); !errors.Is(err, cart.ErrNotFound) {
		t.Errorf("the cart survived cleanup: %v", err)
	}
}

func TestStore_TouchOnChange(t *testing.T) {
	f := setup(t)
	ctx := t.Context()

	token, _ := f.carts.Create(ctx)
	before, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The cleanup measures activity, not creation, so every change has to stamp
	// the cart — otherwise a cart in daily use gets deleted after 60 days.
	if err := f.carts.Add(ctx, token, f.tee.ID, 1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	after, err := f.carts.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at did not move: %s then %s", before.UpdatedAt, after.UpdatedAt)
	}
}
