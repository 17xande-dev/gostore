package cart

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/17xande-dev/gostore/internal/db/gen"
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

// Store is the cart's persistence. The SQL lives in internal/db/queries/cart.sql.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

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
	if err := s.q.CreateCart(ctx, token); err != nil {
		return "", fmt.Errorf("cart: create: %w", err)
	}
	return token, nil
}

// Get returns the cart and its items, priced from the catalog as it stands now.
func (s *Store) Get(ctx context.Context, token string) (Cart, error) {
	if token == "" {
		return Cart{}, ErrNotFound
	}

	row, err := s.q.GetCart(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Cart{}, ErrNotFound
		}
		return Cart{}, fmt.Errorf("cart: get: %w", err)
	}

	lines, err := s.q.ListCartItems(ctx, token)
	if err != nil {
		return Cart{}, fmt.Errorf("cart: get items: %w", err)
	}

	c := Cart{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Items: []Item{}}
	for _, l := range lines {
		c.Items = append(c.Items, Item{
			VariantID:      l.VariantID,
			Quantity:       l.Quantity,
			ProductSlug:    l.ProductSlug,
			ProductTitle:   l.ProductTitle,
			SKU:            l.SKU,
			Option1:        l.Option1,
			Option2:        l.Option2,
			Option3:        l.Option3,
			UnitPriceCents: l.PriceCents,
			StockQty:       l.StockQty,
			Purchasable:    l.Purchasable,
		})
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
	q := s.q.WithTx(tx)

	stock, err := availableStock(ctx, q, variantID)
	if err != nil {
		return err
	}

	existing, err := q.GetCartLineQuantity(ctx, gen.GetCartLineQuantityParams{
		CartID: token, VariantID: variantID,
	})
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

	if err := upsertLine(ctx, q, token, variantID, wanted); err != nil {
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
	q := s.q.WithTx(tx)

	stock, err := availableStock(ctx, q, variantID)
	if err != nil {
		return err
	}
	if quantity > stock {
		return &OutOfStockError{Available: stock}
	}
	if err := upsertLine(ctx, q, token, variantID, quantity); err != nil {
		return err
	}
	return commit(ctx, tx)
}

// Remove deletes one line. Removing something that is not there is not an error:
// the shopper's intent — "this should not be in my cart" — is satisfied either
// way, and a double-click should not produce a failure page.
func (s *Store) Remove(ctx context.Context, token, variantID string) error {
	err := s.q.DeleteCartLine(ctx, gen.DeleteCartLineParams{CartID: token, VariantID: variantID})
	if err != nil {
		if isMalformedUUID(err) {
			// A variant id that could never exist is already not in the cart.
			return nil
		}
		return fmt.Errorf("cart: remove: %w", err)
	}
	return s.touch(ctx, token)
}

// Clear empties a cart without deleting it.
func (s *Store) Clear(ctx context.Context, token string) error {
	if err := s.q.ClearCart(ctx, token); err != nil {
		return fmt.Errorf("cart: clear: %w", err)
	}
	return s.touch(ctx, token)
}

// DeleteOlderThan removes carts untouched for the given number of days, keeping
// the table bounded. Returns how many went.
func (s *Store) DeleteOlderThan(ctx context.Context, days int) (int64, error) {
	n, err := s.q.DeleteCartsOlderThan(ctx, int32(days))
	if err != nil {
		return 0, fmt.Errorf("cart: delete old carts: %w", err)
	}
	return n, nil
}

// availableStock returns a variant's stock, or ErrUnavailable if it cannot be
// sold at all. A malformed id is ErrUnavailable rather than a database error:
// from outside, a variant that never existed and one that cannot be bought are
// the same answer.
func availableStock(ctx context.Context, q *gen.Queries, variantID string) (int, error) {
	row, err := q.GetVariantAvailability(ctx, variantID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, ErrUnavailable
	case err != nil:
		if isMalformedUUID(err) {
			return 0, ErrUnavailable
		}
		return 0, fmt.Errorf("cart: read variant: %w", err)
	case !row.Purchasable:
		return 0, ErrUnavailable
	}
	return row.StockQty, nil
}

// upsertLine writes a line and stamps the cart, so the cleanup job measures
// activity rather than creation.
func upsertLine(ctx context.Context, q *gen.Queries, token, variantID string, quantity int) error {
	err := q.UpsertCartLine(ctx, gen.UpsertCartLineParams{
		CartID: token, VariantID: variantID, Quantity: quantity,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// The cart row itself is gone: cleaned up, or the cookie is stale.
			return ErrNotFound
		}
		return fmt.Errorf("cart: write line: %w", err)
	}
	if err := q.TouchCart(ctx, token); err != nil {
		return fmt.Errorf("cart: touch: %w", err)
	}
	return nil
}

func (s *Store) touch(ctx context.Context, token string) error {
	if err := s.q.TouchCart(ctx, token); err != nil {
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

// isMalformedUUID reports whether Postgres refused a value as a UUID. Ids are
// strings all the way to the database, so this is where "that is not an id at
// all" arrives.
func isMalformedUUID(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "22P02"
}
