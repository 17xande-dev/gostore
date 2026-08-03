package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
}

// Store is the orders' persistence.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const orderColumns = `id::text, coalesce(cart_id, ''), customer_name, customer_email,
	customer_phone, shipping_address, total_cents, currency, status, gateway,
	coalesce(gateway_ref, ''), gateway_status, gateway_amount, gateway_payload,
	emailed, created_at, paid_at`

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

	items, problems, err := readCart(ctx, tx, cartID)
	if err != nil {
		return Order{}, err
	}
	if len(items) == 0 {
		return Order{}, ErrEmptyCart
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

	o := Order{
		CartID:     cartID,
		Customer:   c,
		TotalCents: total,
		Currency:   currency,
		Status:     StatusPending,
		Gateway:    gateway,
		Items:      items,
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO orders (id, cart_id, customer_name, customer_email, customer_phone,
		                     shipping_address, total_cents, currency, status, gateway)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id::text, created_at`,
		cartID, c.Name, c.Email, c.Phone, c.Address, total, currency, StatusPending, gateway)
	if err := row.Scan(&o.ID, &o.CreatedAt); err != nil {
		return Order{}, translate(fmt.Errorf("orders: create: %w", err))
	}

	for _, i := range items {
		_, err := tx.Exec(ctx,
			`INSERT INTO order_items (order_id, variant_id, title, size, color, unit_price_cents, quantity)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			o.ID, i.VariantID, i.Title, i.Size, i.Color, i.UnitPriceCents, i.Quantity)
		if err != nil {
			return Order{}, fmt.Errorf("orders: create item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Order{}, fmt.Errorf("orders: commit: %w", err)
	}
	return o, nil
}

// readCart returns the cart's lines priced as order items, plus any reasons they
// cannot be bought.
func readCart(ctx context.Context, tx pgx.Tx, cartID string) ([]Item, []string, error) {
	rows, err := tx.Query(ctx,
		`SELECT i.variant_id::text, i.quantity, p.title, v.size, v.color,
		        v.price_cents, v.stock_qty, (v.active AND p.active) AS purchasable
		 FROM cart_items i
		 JOIN product_variants v ON v.id = i.variant_id
		 JOIN products p ON p.id = v.product_id
		 WHERE i.cart_id = $1
		 ORDER BY p.title, v.size, v.color, v.sku`, cartID)
	if err != nil {
		return nil, nil, fmt.Errorf("orders: read cart: %w", err)
	}
	defer rows.Close()

	var (
		items    []Item
		problems []string
	)
	for rows.Next() {
		var (
			i           Item
			stock       int
			purchasable bool
		)
		if err := rows.Scan(&i.VariantID, &i.Quantity, &i.Title, &i.Size, &i.Color,
			&i.UnitPriceCents, &stock, &purchasable); err != nil {
			return nil, nil, fmt.Errorf("orders: scan cart line: %w", err)
		}
		switch {
		case !purchasable:
			problems = append(problems, i.Title+" is no longer for sale.")
		case stock < i.Quantity:
			problems = append(problems, fmt.Sprintf("%s only has %d left.", i.Title, stock))
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("orders: read cart: %w", err)
	}
	return items, problems, nil
}

// Get returns one order with its items.
func (s *Store) Get(ctx context.Context, id string) (Order, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1`, id)
	o, err := scanOrder(row)
	if err != nil {
		return Order{}, err
	}
	if o.Items, err = s.items(ctx, o.ID); err != nil {
		return Order{}, err
	}
	return o, nil
}

// LatestForCart returns the most recent order placed from a cart, which is how the
// page a shopper lands on after paying knows which order to name. The cart token
// is the only credential involved, and it grants access to nothing but its own
// basket and the orders placed from it.
func (s *Store) LatestForCart(ctx context.Context, cartID string) (Order, error) {
	if cartID == "" {
		return Order{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE cart_id = $1 ORDER BY created_at DESC LIMIT 1`, cartID)
	o, err := scanOrder(row)
	if err != nil {
		return Order{}, err
	}
	if o.Items, err = s.items(ctx, o.ID); err != nil {
		return Order{}, err
	}
	return o, nil
}

func (s *Store) items(ctx context.Context, orderID string) ([]Item, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT variant_id::text, title, size, color, unit_price_cents, quantity
		 FROM order_items WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("orders: items: %w", err)
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.VariantID, &i.Title, &i.Size, &i.Color, &i.UnitPriceCents, &i.Quantity); err != nil {
			return nil, fmt.Errorf("orders: scan item: %w", err)
		}
		items = append(items, i)
	}
	return items, rows.Err()
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

	var status Status
	err = tx.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if err != nil {
		return PaidResult{}, translate(fmt.Errorf("orders: lock order: %w", err))
	}
	if status == StatusPaid {
		// Nothing to do, and nothing to roll back either — but commit rather than
		// rollback so the lock is released cleanly.
		if err := tx.Commit(ctx); err != nil {
			return PaidResult{}, fmt.Errorf("orders: commit: %w", err)
		}
		return PaidResult{AlreadyPaid: true}, nil
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders
		 SET status = $2, paid_at = now(), gateway = $3, gateway_ref = $4,
		     gateway_status = $5, gateway_amount = $6, gateway_payload = $7
		 WHERE id = $1`,
		id, StatusPaid, p.Gateway, nullable(p.Ref), p.Status, p.Amount, p.Raw)
	if err != nil {
		return PaidResult{}, fmt.Errorf("orders: mark paid: %w", err)
	}

	rows, err := tx.Query(ctx,
		`SELECT variant_id::text, title, quantity FROM order_items WHERE order_id = $1 ORDER BY id`, id)
	if err != nil {
		return PaidResult{}, fmt.Errorf("orders: read items: %w", err)
	}
	type line struct {
		variantID string
		title     string
		quantity  int
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.variantID, &l.title, &l.quantity); err != nil {
			rows.Close()
			return PaidResult{}, fmt.Errorf("orders: scan item: %w", err)
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PaidResult{}, fmt.Errorf("orders: read items: %w", err)
	}

	var result PaidResult
	for _, l := range lines {
		// The stock_qty >= $1 guard is what makes this safe rather than the
		// transaction alone: it turns "would go negative" into zero rows affected,
		// which is a fact the caller can act on, instead of a constraint violation
		// that would abort a transaction that has already taken money.
		tag, err := tx.Exec(ctx,
			`UPDATE product_variants SET stock_qty = stock_qty - $1 WHERE id = $2 AND stock_qty >= $1`,
			l.quantity, l.variantID)
		if err != nil {
			return PaidResult{}, fmt.Errorf("orders: decrement stock: %w", err)
		}
		if tag.RowsAffected() == 0 {
			result.Oversold = append(result.Oversold, l.title)
		}
	}

	// The basket has become an order, so empty it. The cart row itself stays, so
	// the shopper's cookie keeps working for their next visit.
	if _, err := tx.Exec(ctx,
		`DELETE FROM cart_items WHERE cart_id = (SELECT cart_id FROM orders WHERE id = $1)`, id); err != nil {
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

	tag, err := s.pool.Exec(ctx,
		`UPDATE orders
		 SET status = $2, gateway = $3, gateway_ref = $4, gateway_status = $5,
		     gateway_amount = $6, gateway_payload = $7
		 WHERE id = $1 AND status <> $8`,
		id, status, p.Gateway, nullable(p.Ref), p.Status, p.Amount, p.Raw, StatusPaid)
	if err != nil {
		return translate(fmt.Errorf("orders: record unpaid: %w", err))
	}
	if tag.RowsAffected() == 0 {
		// Either there is no such order, or it is paid and was deliberately left
		// alone. Only the first is an error, and the two are worth telling apart.
		var existing Status
		if err := s.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, id).Scan(&existing); err != nil {
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
	tag, err := s.pool.Exec(ctx,
		`UPDATE orders
		 SET gateway = $2, gateway_ref = $3, gateway_status = $4, gateway_amount = $5, gateway_payload = $6
		 WHERE id = $1`,
		id, p.Gateway, nullable(p.Ref), p.Status, p.Amount, p.Raw)
	if err != nil {
		return translate(fmt.Errorf("orders: record notification: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkEmailed records that the confirmation email went out, so a retry does not
// send a second one.
func (s *Store) MarkEmailed(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE orders SET emailed = TRUE WHERE id = $1`, id); err != nil {
		return translate(fmt.Errorf("orders: mark emailed: %w", err))
	}
	return nil
}

func scanOrder(row pgx.Row) (Order, error) {
	var (
		o      Order
		paidAt *time.Time
	)
	err := row.Scan(&o.ID, &o.CartID, &o.Customer.Name, &o.Customer.Email, &o.Customer.Phone,
		&o.Customer.Address, &o.TotalCents, &o.Currency, &o.Status, &o.Gateway, &o.GatewayRef,
		&o.GatewayStatus, &o.GatewayAmount, &o.GatewayPayload, &o.Emailed, &o.CreatedAt, &paidAt)
	if err != nil {
		return Order{}, translate(fmt.Errorf("orders: order: %w", err))
	}
	if paidAt != nil {
		o.PaidAt = *paidAt
	}
	return o, nil
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
