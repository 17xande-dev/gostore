package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound means no such order. A malformed id is the same answer: from
	// outside, an order that never existed and one that cannot be named are
	// indistinguishable.
	ErrNotFound = errors.New("orders: not found")

	// ErrEmptyCart means there is nothing to order.
	ErrEmptyCart = errors.New("orders: cart is empty")
)

// UnavailableError reports that the cart cannot be turned into an order, and
// carries the reasons in the shopper's terms so the checkout page can say what to
// fix.
type UnavailableError struct{ Problems []string }

func (e *UnavailableError) Error() string {
	return "orders: cart is not purchasable: " + strings.Join(e.Problems, " ")
}

// Payment is what a gateway's notification said, in this package's terms. The
// mapping from payment.Callback happens in the handler, so this package stays
// unaware of which gateways exist.
type Payment struct {
	Gateway string
	Ref     string
	Status  string
	Amount  string
	Raw     string
}

// PaidResult reports what marking an order paid actually did, because both of
// these are things the caller has to act on and neither is an error.
type PaidResult struct {
	// AlreadyPaid means this notification was a replay: the order was paid
	// before, and no stock moved this time. Gateways retry, and a retry must not
	// decrement stock twice.
	AlreadyPaid bool

	// Oversold names the lines whose stock could not be decremented because
	// there was not enough left. The order is still recorded paid — the money has
	// been taken, and pretending otherwise loses it — so these need a human.
	Oversold []string

	// Grants are the download entitlements this payment created, one per digital
	// line, each carrying its plaintext token.
	//
	// This is the only time the token exists in readable form. Only its SHA-256
	// hash is stored, so if the confirmation email does not go out the link cannot
	// be recovered — a new entitlement has to be issued. That is the deliberate
	// trade for a database leak not being a licence to download the catalogue, and
	// it is why the email is sent on the same request that mints these.
	Grants []Grant
}

// Grant is one buyer's new download link.
type Grant struct {
	EntitlementID string
	OrderItemID   int64
	VariantID     string
	Title         string
	VariantLabel  string
	// Token is the plaintext credential, to be put in the email and then
	// forgotten. It is never stored and never logged.
	Token string
}

// Store is the orders' persistence. The SQL lives in
// internal/db/queries/orders.sql; what remains here is the transaction
// orchestration, which is the part that is genuinely this package's own.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// CreateFromCart snapshots a cart into a pending order, in one transaction.
//
// Prices and availability are re-read here, inside the transaction, and the total
// is computed from what the database says right now — never from what the page the
// shopper submitted was showing. A page can be minutes old, or edited; the amount
// sent to the gateway has to be the amount the catalog agrees with, because that
// is the figure the notification will be checked against.
//
// The cart is left alone. A shopper who abandons the gateway's payment page comes
// back to an intact basket, and the cart is cleared when payment actually
// succeeds.
func (s *Store) CreateFromCart(ctx context.Context, cartID string, c Customer, currency, gateway string) (Order, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, fmt.Errorf("orders: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	lines, err := q.ListCartLinesForOrder(ctx, cartID)
	if err != nil {
		return Order{}, fmt.Errorf("orders: read cart: %w", err)
	}
	if len(lines) == 0 {
		return Order{}, ErrEmptyCart
	}

	items := make([]Item, 0, len(lines))
	var problems []string
	for _, l := range lines {
		items = append(items, Item{
			VariantID:      l.VariantID,
			Title:          l.Title,
			Kind:           l.Kind,
			VariantLabel:   catalog.OptionLabel(l.Option1, l.Option2, l.Option3),
			UnitPriceCents: l.PriceCents,
			Quantity:       l.Quantity,
		})
		switch {
		case !l.Purchasable:
			problems = append(problems, l.Title+" is no longer for sale.")
		// A download has no stock to be short of. Without this exemption every
		// digital checkout would be refused as unavailable before it reached the
		// gateway.
		case !catalog.Kind(l.Kind).Digital() && l.StockQty < l.Quantity:
			problems = append(problems, fmt.Sprintf("%s only has %d left.", l.Title, l.StockQty))
		}
	}
	// Refusing here is the last line of defence, not the first: the cart page
	// already blocks checkout on these. It exists because between rendering that
	// page and submitting it, the shop owner may have withdrawn something.
	if len(problems) > 0 {
		return Order{}, &UnavailableError{Problems: problems}
	}

	var total int64
	for _, i := range items {
		total += i.LineTotalCents()
	}

	row, err := q.CreateOrder(ctx, gen.CreateOrderParams{
		CartID:          &cartID,
		CustomerName:    c.Name,
		CustomerEmail:   c.Email,
		CustomerPhone:   c.Phone,
		ShippingAddress: c.Address,
		TotalCents:      total,
		Currency:        currency,
		Status:          string(StatusPending),
		Gateway:         gateway,
	})
	if err != nil {
		return Order{}, translate(fmt.Errorf("orders: create: %w", err))
	}

	for _, i := range items {
		err := q.CreateOrderItem(ctx, gen.CreateOrderItemParams{
			OrderID:        row.ID,
			VariantID:      i.VariantID,
			Title:          i.Title,
			VariantLabel:   i.VariantLabel,
			Kind:           i.Kind,
			UnitPriceCents: i.UnitPriceCents,
			Quantity:       i.Quantity,
		})
		if err != nil {
			return Order{}, fmt.Errorf("orders: create item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Order{}, fmt.Errorf("orders: commit: %w", err)
	}
	return Order{
		ID:         row.ID,
		CartID:     cartID,
		Customer:   c,
		TotalCents: total,
		Currency:   currency,
		Status:     StatusPending,
		Gateway:    gateway,
		CreatedAt:  row.CreatedAt,
		Items:      items,
	}, nil
}

// Get returns one order with its items.
func (s *Store) Get(ctx context.Context, id string) (Order, error) {
	row, err := s.q.GetOrder(ctx, id)
	if err != nil {
		return Order{}, translate(fmt.Errorf("orders: order: %w", err))
	}
	o := order(row)
	if o.Items, err = s.items(ctx, o.ID); err != nil {
		return Order{}, err
	}
	return o, nil
}

// LatestForCart returns the most recent order placed from a cart, which is how the
// page a shopper lands on after paying knows which order to name. The cart token
// is the only credential involved, and it is enough: it names their own basket and
// the orders placed from it, and nothing else.
func (s *Store) LatestForCart(ctx context.Context, cartID string) (Order, error) {
	if cartID == "" {
		return Order{}, ErrNotFound
	}
	row, err := s.q.GetLatestOrderForCart(ctx, &cartID)
	if err != nil {
		return Order{}, translate(fmt.Errorf("orders: order: %w", err))
	}
	o := order(row)
	if o.Items, err = s.items(ctx, o.ID); err != nil {
		return Order{}, err
	}
	return o, nil
}

// DefaultListLimit is how many orders the admin list shows. Orders accumulate
// forever, unlike the catalog, so this page is bounded from the start rather than
// after somebody's first busy month.
const DefaultListLimit = 200

// List returns recent orders, newest first, without their line items — the admin
// list shows one row per order and the lines are one click away.
func (s *Store) List(ctx context.Context, limit int) ([]Order, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	rows, err := s.q.ListRecentOrders(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("orders: list: %w", err)
	}
	out := make([]Order, 0, len(rows))
	for _, r := range rows {
		out = append(out, order(r))
	}
	return out, nil
}

func (s *Store) items(ctx context.Context, orderID string) ([]Item, error) {
	rows, err := s.q.ListOrderItems(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("orders: items: %w", err)
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, Item{
			ID:             r.ID,
			VariantID:      r.VariantID,
			Title:          r.Title,
			Kind:           r.Kind,
			VariantLabel:   r.VariantLabel,
			UnitPriceCents: r.UnitPriceCents,
			Quantity:       r.Quantity,
		})
	}
	return items, nil
}

// MarkPaid records a payment and decrements stock, in one transaction.
//
// This is the most safety-critical function in the project, and the transaction is
// why: a crash between marking an order paid and taking its stock out of inventory
// would leave the shop selling things it has already sold, and two notifications
// arriving at once would each read the old stock and each subtract from it.
//
// The order row is locked first, which serialises concurrent notifications for the
// same order, and an order that is already paid returns immediately without
// touching stock. Gateways retry, so a replay is routine traffic and not an error.
//
// A line whose stock cannot be decremented is reported in the result rather than
// failing the call: the money has been taken, so refusing to record the order
// would lose the sale as well as overselling the item.
func (s *Store) MarkPaid(ctx context.Context, id string, p Payment) (PaidResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaidResult{}, fmt.Errorf("orders: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	status, err := q.LockOrderStatus(ctx, id)
	if err != nil {
		return PaidResult{}, translate(fmt.Errorf("orders: lock order: %w", err))
	}
	if Status(status) == StatusPaid {
		// Nothing to do, and nothing to roll back either — but commit rather than
		// rollback so the lock is released cleanly.
		if err := tx.Commit(ctx); err != nil {
			return PaidResult{}, fmt.Errorf("orders: commit: %w", err)
		}
		return PaidResult{AlreadyPaid: true}, nil
	}

	err = q.MarkOrderPaid(ctx, gen.MarkOrderPaidParams{
		ID:             id,
		Status:         string(StatusPaid),
		Gateway:        p.Gateway,
		GatewayRef:     nullable(p.Ref),
		GatewayStatus:  p.Status,
		GatewayAmount:  p.Amount,
		GatewayPayload: p.Raw,
	})
	if err != nil {
		return PaidResult{}, fmt.Errorf("orders: mark paid: %w", err)
	}

	lines, err := q.ListOrderItems(ctx, id)
	if err != nil {
		return PaidResult{}, fmt.Errorf("orders: read items: %w", err)
	}

	var result PaidResult
	for _, l := range lines {
		// A download cannot run out, so there is nothing to take. Without this
		// skip every digital sale would find stock_qty at 0, count as oversold,
		// flag the order and email the owner a warning about a file.
		//
		// The kind is read from the order_items snapshot rather than from the
		// product, so a product flipped after the sale cannot change how this line
		// behaved.
		if catalog.Kind(l.Kind).Digital() {
			continue
		}
		affected, err := q.DecrementVariantStock(ctx, gen.DecrementVariantStockParams{
			StockQty: l.Quantity,
			ID:       l.VariantID,
		})
		if err != nil {
			return PaidResult{}, fmt.Errorf("orders: decrement stock: %w", err)
		}
		if affected == 0 {
			result.Oversold = append(result.Oversold, l.Title)
		}
	}

	// Entitlements are minted here, inside the same transaction that recorded the
	// payment, for the same reason the oversold flag is: an order must never be
	// paid-without-entitlements. A buyer whose money was taken and whose download
	// row failed to write has no way to reach what they bought, and nothing in the
	// admin to say so.
	if result.Grants, err = mintEntitlements(ctx, q, id); err != nil {
		return PaidResult{}, err
	}

	// Flagged in the same transaction that recorded the payment, so an order can
	// never be paid-but-unflagged: the two facts commit together or not at all.
	if len(result.Oversold) > 0 {
		if err := q.FlagOrderOversold(ctx, id); err != nil {
			return PaidResult{}, fmt.Errorf("orders: flag oversold: %w", err)
		}
	}

	// The basket has become an order, so empty it. The cart row itself stays, so
	// the shopper's cookie keeps working for their next visit.
	if err := q.ClearCartForOrder(ctx, id); err != nil {
		return PaidResult{}, fmt.Errorf("orders: clear cart: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PaidResult{}, fmt.Errorf("orders: commit: %w", err)
	}
	return result, nil
}

// RecordUnpaid records a notification that did not pay for the order — cancelled,
// failed, or still pending at the gateway.
//
// It never touches stock and never contradicts a payment: an order that is already
// paid is left alone, because a late "failed" notification arriving after a
// genuine "complete" one must not un-sell something already shipped.
func (s *Store) RecordUnpaid(ctx context.Context, id string, status Status, p Payment) error {
	if status == StatusPaid {
		return fmt.Errorf("orders: RecordUnpaid called with status %q; use MarkPaid", status)
	}

	affected, err := s.q.RecordUnpaidOrder(ctx, gen.RecordUnpaidOrderParams{
		ID:             id,
		Status:         string(status),
		Gateway:        p.Gateway,
		GatewayRef:     nullable(p.Ref),
		GatewayStatus:  p.Status,
		GatewayAmount:  p.Amount,
		GatewayPayload: p.Raw,
		Status_2:       string(StatusPaid),
	})
	if err != nil {
		return translate(fmt.Errorf("orders: record unpaid: %w", err))
	}
	if affected == 0 {
		// Either there is no such order, or it is paid and was deliberately left
		// alone. Only the first is an error, and the two are worth telling apart.
		if _, err := s.q.GetOrderStatus(ctx, id); err != nil {
			return translate(fmt.Errorf("orders: record unpaid: %w", err))
		}
	}
	return nil
}

// RecordNotification stores what a gateway said without touching the order's
// status.
//
// It exists for the case where a notification is genuine but cannot be acted on —
// the amount paid not matching the amount asked for, most importantly. Calling
// that order "failed" would misdescribe it, and calling it paid would credit a
// figure this store never quoted, so the payload is filed for whoever reconciles
// it and the status stays where it was.
func (s *Store) RecordNotification(ctx context.Context, id string, p Payment) error {
	affected, err := s.q.RecordOrderNotification(ctx, gen.RecordOrderNotificationParams{
		ID:             id,
		Gateway:        p.Gateway,
		GatewayRef:     nullable(p.Ref),
		GatewayStatus:  p.Status,
		GatewayAmount:  p.Amount,
		GatewayPayload: p.Raw,
	})
	if err != nil {
		return translate(fmt.Errorf("orders: record notification: %w", err))
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkEmailed records that the confirmation email went out, so a retry does not
// send a second one.
func (s *Store) MarkEmailed(ctx context.Context, id string) error {
	if err := s.q.MarkOrderEmailed(ctx, id); err != nil {
		return translate(fmt.Errorf("orders: mark emailed: %w", err))
	}
	return nil
}

// order maps a row onto the domain value. The two nullable columns are the only
// interesting part: an absent cart is "" because the order does not need it, and
// an absent paid_at is the zero time because "not paid" and "paid at the zero
// instant" are not going to be confused.
func order(r gen.Order) Order {
	o := Order{
		ID: r.ID,
		Customer: Customer{
			Name:    r.CustomerName,
			Email:   r.CustomerEmail,
			Phone:   r.CustomerPhone,
			Address: r.ShippingAddress,
		},
		TotalCents:     r.TotalCents,
		Currency:       r.Currency,
		Status:         Status(r.Status),
		Gateway:        r.Gateway,
		GatewayStatus:  r.GatewayStatus,
		GatewayAmount:  r.GatewayAmount,
		GatewayPayload: r.GatewayPayload,
		Emailed:        r.Emailed,
		Oversold:       r.Oversold,
		CreatedAt:      r.CreatedAt,
	}
	if r.CartID != nil {
		o.CartID = *r.CartID
	}
	if r.GatewayRef != nil {
		o.GatewayRef = *r.GatewayRef
	}
	if r.PaidAt != nil {
		o.PaidAt = *r.PaidAt
	}
	return o
}

// nullable keeps an empty gateway reference out of the partial unique index on
// (gateway, gateway_ref): NULL rows are excluded from it, empty strings are not,
// so a second unpaid order would collide with the first.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// translate turns the driver's vocabulary into this package's. A malformed UUID
// arrives as a syntax error and means the same thing as a missing row.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "22P02" {
		return ErrNotFound
	}
	return err
}
