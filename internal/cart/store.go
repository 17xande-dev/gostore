package cart

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxQuantity caps a single line. There is no business reason for a shopper to
// order 10 000 of anything from a shop this size, and an unbounded quantity is a
// free way to make a page render arithmetic on absurd numbers.
const MaxQuantity = 99

var (
	// ErrNotFound means the token does not name a cart — expired, cleaned up, or
	// invented. Callers treat it as an empty cart rather than an error page.
	ErrNotFound = errors.New("cart: not found")

	// ErrUnavailable means the variant cannot be added: it does not exist, or it
	// or its product is inactive.
	ErrUnavailable = errors.New("cart: variant is not available")

	// ErrQuantity means the requested quantity is not a sane number of things.
	ErrQuantity = errors.New("cart: quantity out of range")
)

// OutOfStockError reports that the requested quantity exceeds what is left, and
// carries the number available so the shopper is told the actual limit instead of
// "no".
type OutOfStockError struct {
	Available int
}

func (e *OutOfStockError) Error() string {
	return fmt.Sprintf("cart: only %d in stock", e.Available)
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// NewToken returns an opaque cart token: 24 bytes from crypto/rand, URL-safe.
// It is unguessable rather than signed, because holding one grants nothing but
// access to a single anonymous basket.
func NewToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cart: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create starts an empty cart and returns its token.
func (s *Store) Create(ctx context.Context) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO carts (id) VALUES ($1)`, token); err != nil {
		return "", fmt.Errorf("cart: create: %w", err)
	}
	return token, nil
}

// Get returns the cart and its items, priced from the catalog as it stands now.
//
// The join is deliberately not filtered by active: an item whose product has
// been withdrawn stays visible and is marked unavailable, because a line
// silently disappearing between page loads looks like a bug or a hidden price
// change.
func (s *Store) Get(ctx context.Context, token string) (Cart, error) {
	if token == "" {
		return Cart{}, ErrNotFound
	}

	var c Cart
	err := s.pool.QueryRow(ctx, `SELECT id, created_at, updated_at FROM carts WHERE id = $1`, token).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Cart{}, ErrNotFound
		}
		return Cart{}, fmt.Errorf("cart: get: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT i.variant_id::text, i.quantity,
		        p.slug, p.title, v.sku, v.size, v.color,
		        v.price_cents, v.stock_qty, (v.active AND p.active) AS purchasable
		 FROM cart_items i
		 JOIN product_variants v ON v.id = i.variant_id
		 JOIN products p ON p.id = v.product_id
		 WHERE i.cart_id = $1
		 ORDER BY p.title, v.size, v.color, v.sku`, token)
	if err != nil {
		return Cart{}, fmt.Errorf("cart: get items: %w", err)
	}
	defer rows.Close()

	c.Items = []Item{}
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.VariantID, &i.Quantity, &i.ProductSlug, &i.ProductTitle,
			&i.SKU, &i.Size, &i.Color, &i.UnitPriceCents, &i.StockQty, &i.Purchasable); err != nil {
			return Cart{}, fmt.Errorf("cart: scan item: %w", err)
		}
		c.Items = append(c.Items, i)
	}
	if err := rows.Err(); err != nil {
		return Cart{}, fmt.Errorf("cart: get items: %w", err)
	}
	return c, nil
}

// Add puts quantity of a variant into the cart, adding to whatever is already
// there for that variant. The total is checked against stock, so adding twice
// cannot smuggle past the limit that adding once would hit.
func (s *Store) Add(ctx context.Context, token, variantID string, quantity int) error {
	if quantity < 1 || quantity > MaxQuantity {
		return ErrQuantity
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("cart: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	stock, err := availableStock(ctx, tx, variantID)
	if err != nil {
		return err
	}

	var existing int
	err = tx.QueryRow(ctx, `SELECT quantity FROM cart_items WHERE cart_id = $1 AND variant_id = $2`,
		token, variantID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("cart: read line: %w", err)
	}

	wanted := existing + quantity
	if wanted > MaxQuantity {
		return ErrQuantity
	}
	if wanted > stock {
		return &OutOfStockError{Available: stock}
	}

	if err := upsertLine(ctx, tx, token, variantID, wanted); err != nil {
		return err
	}
	return commit(ctx, tx)
}

// SetQuantity replaces the quantity of one line. Zero removes it, which is what
// makes a "remove" button work without JavaScript: the same form posts 0.
func (s *Store) SetQuantity(ctx context.Context, token, variantID string, quantity int) error {
	if quantity < 0 || quantity > MaxQuantity {
		return ErrQuantity
	}
	if quantity == 0 {
		return s.Remove(ctx, token, variantID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("cart: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	stock, err := availableStock(ctx, tx, variantID)
	if err != nil {
		return err
	}
	if quantity > stock {
		return &OutOfStockError{Available: stock}
	}
	if err := upsertLine(ctx, tx, token, variantID, quantity); err != nil {
		return err
	}
	return commit(ctx, tx)
}

// Remove deletes one line. Removing something that is not there is not an error:
// the shopper's intent — "this should not be in my cart" — is satisfied either
// way, and a double-click should not produce a failure page.
func (s *Store) Remove(ctx context.Context, token, variantID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM cart_items WHERE cart_id = $1 AND variant_id = $2`, token, variantID); err != nil {
		return fmt.Errorf("cart: remove: %w", err)
	}
	return s.touch(ctx, token)
}

// Clear empties a cart without deleting it.
func (s *Store) Clear(ctx context.Context, token string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cart_items WHERE cart_id = $1`, token); err != nil {
		return fmt.Errorf("cart: clear: %w", err)
	}
	return s.touch(ctx, token)
}

// DeleteOlderThan removes carts untouched for the given number of days, keeping
// the table bounded. Returns how many went.
func (s *Store) DeleteOlderThan(ctx context.Context, days int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM carts WHERE updated_at < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, fmt.Errorf("cart: delete old carts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// availableStock returns a variant's stock, or ErrUnavailable if it cannot be
// sold at all. A malformed id is ErrUnavailable rather than a database error:
// from outside, a variant that never existed and one that cannot be bought are
// the same answer.
func availableStock(ctx context.Context, tx pgx.Tx, variantID string) (int, error) {
	var stock int
	var purchasable bool
	err := tx.QueryRow(ctx,
		`SELECT v.stock_qty, (v.active AND p.active)
		 FROM product_variants v JOIN products p ON p.id = v.product_id
		 WHERE v.id = $1`, variantID).Scan(&stock, &purchasable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, ErrUnavailable
	case err != nil:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // invalid input syntax for uuid
			return 0, ErrUnavailable
		}
		return 0, fmt.Errorf("cart: read variant: %w", err)
	case !purchasable:
		return 0, ErrUnavailable
	}
	return stock, nil
}

// upsertLine writes a line and stamps the cart, so the cleanup job measures
// activity rather than creation.
func upsertLine(ctx context.Context, tx pgx.Tx, token, variantID string, quantity int) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
		 ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
		token, variantID, quantity)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// The cart row itself is gone: cleaned up, or the cookie is stale.
			return ErrNotFound
		}
		return fmt.Errorf("cart: write line: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE carts SET updated_at = now() WHERE id = $1`, token); err != nil {
		return fmt.Errorf("cart: touch: %w", err)
	}
	return nil
}

func (s *Store) touch(ctx context.Context, token string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE carts SET updated_at = now() WHERE id = $1`, token); err != nil {
		return fmt.Errorf("cart: touch: %w", err)
	}
	return nil
}

func commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("cart: commit: %w", err)
	}
	return nil
}
