package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/17xande-dev/gostore/internal/downloads"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/outbox"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	srv     *httptest.Server
	server  *Server
	pool    *pgxpool.Pool
	users   *auth.Store
	catalog *catalog.Store
	orders  *orders.Store
	carts   *cart.Store
	images  *blob.Fake
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith serves the endpoint behind a different verifier, nil meaning the
// real one.
func newFixtureWith(t *testing.T, verify func(*Server) mcpauth.TokenVerifier) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	cat := catalog.NewStore(pool)
	queue, err := outbox.New(pool, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		pool: pool, users: auth.NewStore(pool), catalog: cat,
		orders: orders.NewStore(pool), carts: cart.NewStore(pool),
		images: blob.NewFake(),
	}
	f.server = New(Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users:   f.users,
		Catalog: cat,
		Orders:  f.orders,
		Grants:  downloads.NewStore(pool, cat),
		Outbox:  queue,
		Images:  f.images,
		BaseURL: "https://shop.test",
		Version: "test",
	})
	mux := http.NewServeMux()
	if verify == nil {
		mux.Handle("/mcp", f.server.Handler())
	} else {
		mux.Handle("/mcp", f.server.handler(verify(f.server)))
	}
	mux.Handle("PUT "+UploadPath+"{token}", f.server.UploadHandler())
	mux.Handle("POST "+UploadPath+"{token}", f.server.UploadHandler())
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// account makes an administrator and a token for it.
func (f *fixture) account(t *testing.T, email string, role auth.Role) (auth.User, string) {
	t.Helper()
	hash, err := auth.HashPassword("correct horse battery",
		auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.users.Create(t.Context(), email, "", hash, role, false)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := f.users.IssueAPIToken(t.Context(), u.ID, "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return u, token
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (f *fixture) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   f.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call runs a tool, returning its structured output decoded into out (when out
// is not nil) and, for a tool error, its message.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(text.Text)
			}
		}
		return msg.String()
	}
	if out != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, raw, err)
		}
	}
	return ""
}

func TestEndpoint_RefusesWithoutAValidToken(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "owner@example.com", auth.RoleOwner)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for name, header := range map[string]string{
		"none":    "",
		"garbage": "Bearer gst_nope",
		"session": "Bearer " + strings.TrimPrefix(token, auth.APITokenPrefix),
		"basic":   "Basic " + token,
	} {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, res.StatusCode)
		}
	}
}

func TestEndpoint_ARevokedTokenStopsOnItsNextCall(t *testing.T) {
	f := newFixture(t)
	u, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)
	if msg := call(t, s, "list_products", map[string]any{}, nil); msg != "" {
		t.Fatalf("list_products: %s", msg)
	}

	if err := f.users.SetDisabled(t.Context(), u.ID, true); err == nil {
		t.Fatal("disabled the last owner")
	}
	// The last owner cannot be disabled; demote-free revocation is a password change.
	if err := f.users.SetPassword(t.Context(), u.ID, mustHash(t), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_products", Arguments: map[string]any{}}); err == nil {
		t.Fatal("a token revoked by a password change still works")
	}
}

func mustHash(t *testing.T) string {
	t.Helper()
	h, err := auth.HashPassword("another password entirely",
		auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// minimalArgs is a schema-valid call for every write tool, so the sweep below
// reaches the permission check rather than stopping at input validation.
var minimalArgs = map[string]map[string]any{
	"create_product":      {"title": "Mug"},
	"update_product":      {"id": "00000000-0000-0000-0000-000000000000"},
	"delete_product":      {"id": "00000000-0000-0000-0000-000000000000"},
	"create_variant":      {"product_id": "00000000-0000-0000-0000-000000000000", "sku": "X", "price": "1"},
	"update_variant":      {"product_id": "00000000-0000-0000-0000-000000000000", "id": "00000000-0000-0000-0000-000000000000"},
	"delete_variant":      {"product_id": "00000000-0000-0000-0000-000000000000", "id": "00000000-0000-0000-0000-000000000000"},
	"create_image_upload": {"product_id": "00000000-0000-0000-0000-000000000000"},
	"create_category":     {"name": "Mugs"},
	"update_category":     {"id": "00000000-0000-0000-0000-000000000000"},
	"delete_category":     {"id": "00000000-0000-0000-0000-000000000000"},
	"set_fulfillment":     {"order_id": "00000000-0000-0000-0000-000000000000", "fulfilled": true},
	"retry_order_email":   {"id": "00000000-0000-0000-0000-000000000000"},
	"revoke_entitlement":  {"order_id": "00000000-0000-0000-0000-000000000000", "entitlement_id": "00000000-0000-0000-0000-000000000000"},
	"restore_entitlement": {"order_id": "00000000-0000-0000-0000-000000000000", "entitlement_id": "00000000-0000-0000-0000-000000000000"},
}

// Only administrators may hold a token. One made for a lesser role before that
// was the rule — the fixture issues it straight from the store, which is how such
// a token would exist — is refused outright, not merely limited to reads.
func TestEndpoint_RefusesATokenWhoseRoleMayNotHoldOne(t *testing.T) {
	f := newFixture(t)
	f.account(t, "owner@example.com", auth.RoleOwner)
	for _, role := range []auth.Role{auth.RoleManager, auth.RoleViewer} {
		_, token := f.account(t, string(role)+"@example.com", role)
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401", role, res.StatusCode)
		}
	}
	// And an admin's works, so the refusal above is about the role.
	_, token := f.account(t, "admin@example.com", auth.RoleAdmin)
	if msg := call(t, f.connect(t, token), "list_categories", map[string]any{}, nil); msg != "" {
		t.Errorf("admin list_categories: %s", msg)
	}
}

// Every tool names a permission, and a role without it is refused before the
// tool does anything — the MCP twin of the admin route sweep.
//
// Behind the verifier without its role check: no role that may hold a token
// lacks a write permission today, so this is the check that still has to hold
// the day one does.
func TestTools_EveryWriteRefusesAViewer(t *testing.T) {
	f := newFixtureWith(t, func(s *Server) mcpauth.TokenVerifier { return s.lookup })
	f.account(t, "owner@example.com", auth.RoleOwner)
	_, token := f.account(t, "viewer@example.com", auth.RoleViewer)
	s := f.connect(t, token)

	listed, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(f.server.Tools()) {
		t.Fatalf("listed %d tools, registered %d", len(listed.Tools), len(f.server.Tools()))
	}
	for _, tool := range f.server.Tools() {
		if !tool.Perm.Valid() {
			t.Errorf("%s names no valid permission", tool.Name)
			continue
		}
		if tool.Perm == auth.PermRead {
			continue
		}
		args, ok := minimalArgs[tool.Name]
		if !ok {
			t.Errorf("%s is a write with no entry in minimalArgs", tool.Name)
			continue
		}
		msg := call(t, s, tool.Name, args, nil)
		if !strings.Contains(msg, "cannot do this") {
			t.Errorf("%s as a viewer: %q, want a role refusal", tool.Name, msg)
		}
	}
	// And the viewer can still read.
	if msg := call(t, s, "list_categories", map[string]any{}, nil); msg != "" {
		t.Errorf("viewer list_categories: %s", msg)
	}
}

func TestTools_CatalogRoundTrip(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "admin@example.com", auth.RoleAdmin)
	s := f.connect(t, token)

	var cat Category
	if msg := call(t, s, "create_category", map[string]any{"name": "Mugs"}, &cat); msg != "" {
		t.Fatalf("create_category: %s", msg)
	}
	var p Product
	if msg := call(t, s, "create_product", map[string]any{
		"title": "Blue Mug", "option_names": []string{"Size"}, "category_ids": []string{cat.ID},
	}, &p); msg != "" {
		t.Fatalf("create_product: %s", msg)
	}
	if p.Slug != "blue-mug" || p.Kind != "physical" || !p.Active || len(p.Categories) != 1 {
		t.Fatalf("created %+v", p)
	}

	var v Variant
	if msg := call(t, s, "create_variant", map[string]any{
		"product_id": p.ID, "sku": "MUG-L", "options": []string{"Large"}, "price": "149.9", "stock_qty": 5,
	}, &v); msg != "" {
		t.Fatalf("create_variant: %s", msg)
	}
	if v.Price != "149.90" || v.StockQty != 5 || v.Options[0] != "Large" {
		t.Fatalf("variant %+v", v)
	}

	// A partial update leaves what it does not mention — the categories included.
	if msg := call(t, s, "update_product", map[string]any{"id": p.ID, "title": "Big Blue Mug"}, &p); msg != "" {
		t.Fatalf("update_product: %s", msg)
	}
	if p.Title != "Big Blue Mug" || p.Slug != "blue-mug" || len(p.Categories) != 1 || len(p.Variants) != 1 {
		t.Fatalf("after update %+v", p)
	}
	if msg := call(t, s, "update_variant", map[string]any{"product_id": p.ID, "id": v.ID, "price": "99"}, &v); msg != "" {
		t.Fatalf("update_variant: %s", msg)
	}
	if v.Price != "99.00" || v.SKU != "MUG-L" {
		t.Fatalf("variant after update %+v", v)
	}

	stored, err := f.catalog.Get(t.Context(), p.ID)
	if err != nil || stored.Title != "Big Blue Mug" || stored.Variants[0].PriceCents != 9900 {
		t.Fatalf("store has %+v, %v", stored, err)
	}

	var del deleted
	if msg := call(t, s, "delete_category", map[string]any{"id": cat.ID}, &del); msg != "" || !strings.Contains(del.Notice, "1 product") {
		t.Fatalf("delete_category: %q %+v", msg, del)
	}
}

func TestTools_RefusalsAreToolErrorsWithTheReason(t *testing.T) {
	f := newFixture(t)
	_, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)

	if msg := call(t, s, "create_product", map[string]any{"title": ""}, nil); !strings.Contains(msg, "title") {
		t.Errorf("blank title: %q", msg)
	}
	var p Product
	call(t, s, "create_product", map[string]any{"title": "Mug"}, &p)
	if msg := call(t, s, "create_product", map[string]any{"title": "Mug"}, nil); !strings.Contains(msg, "Already used") {
		t.Errorf("duplicate slug: %q", msg)
	}
	if msg := call(t, s, "create_variant", map[string]any{"product_id": p.ID, "sku": "M", "price": "1.234"}, nil); !strings.Contains(msg, "price") {
		t.Errorf("bad price: %q", msg)
	}
	if msg := call(t, s, "get_product", map[string]any{"id": "00000000-0000-0000-0000-000000000000"}, nil); msg != "not found" {
		t.Errorf("missing product: %q", msg)
	}
	if msg := call(t, s, "search_orders", map[string]any{"filter": "shipped"}, nil); !strings.Contains(msg, "filter") {
		t.Errorf("bad filter: %q", msg)
	}
}

func TestTools_FulfilmentRecordsTheTokensAccount(t *testing.T) {
	f := newFixture(t)
	u, token := f.account(t, "owner@example.com", auth.RoleOwner)
	s := f.connect(t, token)

	// A paid order for one physical item, built through the stores the checkout uses.
	ctx := t.Context()
	p, err := f.catalog.Create(ctx, catalog.Product{Title: "Mug", Slug: "mug", Kind: catalog.KindPhysical, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.catalog.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "MUG", PriceCents: 1000, StockQty: 3, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	cartID, err := f.carts.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.carts.Add(ctx, cartID, v.ID, 1); err != nil {
		t.Fatal(err)
	}
	order, err := f.orders.CreateFromCart(ctx, cartID, orders.Customer{
		Name: "Ann", Email: "ann@example.com", Address: "1 Road",
	}, "ZAR", "fake")
	if err != nil {
		t.Fatal(err)
	}

	if msg := call(t, s, "set_fulfillment", map[string]any{"order_id": order.ID, "fulfilled": true}, nil); !strings.Contains(msg, "only paid orders") {
		t.Fatalf("unpaid order: %q", msg)
	}
	if _, err := f.orders.MarkPaid(ctx, order.ID, orders.Payment{Gateway: "fake", Ref: "r1", Status: "COMPLETE", Amount: "10.00"}); err != nil {
		t.Fatal(err)
	}
	var got Order
	if msg := call(t, s, "set_fulfillment", map[string]any{
		"order_id": order.ID, "fulfilled": true, "tracking": "TRK1",
	}, &got); msg != "" {
		t.Fatalf("set_fulfillment: %s", msg)
	}
	if !got.Fulfilled || got.TrackingReference != "TRK1" || len(got.Items) != 1 {
		t.Fatalf("order %+v", got)
	}
	var actor *string
	if err := f.pool.QueryRow(ctx, `SELECT fulfillment_updated_by FROM orders WHERE id = $1`, order.ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor == nil || *actor != u.ID {
		t.Fatalf("fulfillment_updated_by = %v, want %s", actor, u.ID)
	}

	var list orderList
	if msg := call(t, s, "search_orders", map[string]any{"search": "ann"}, &list); msg != "" || len(list.Orders) != 1 {
		t.Fatalf("search_orders: %q %+v", msg, list)
	}
}
