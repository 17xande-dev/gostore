package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned for a product or variant that does not exist, so
// handlers can answer 404 without inspecting driver errors.
var ErrNotFound = errors.New("catalog: not found")

// ErrInUse is returned when a delete would orphan a row that purchase history
// points at. Orders snapshot what was bought, but they still reference the
// variant, and history must not be rewritable.
var ErrInUse = errors.New("catalog: still referenced by other records")

// ConflictError reports a uniqueness violation against a specific field, which
// a form can render next to the offending input rather than as a 500.
type ConflictError struct {
	Field string // "slug", "sku" or "options"
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("catalog: %s is already taken", e.Field)
}

// Store is the catalog's persistence. Every method takes a context so a
// cancelled request stops work in the database too.
//
// The SQL lives in internal/db/queries/catalog.sql and the scanning is generated
// from it by sqlc; what remains here is the part that is genuinely this package's
// own — turning the driver's vocabulary into the domain's, and arranging rows the
// way the domain sees them.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// List returns every product with its variants attached, ordered by title. The
// admin list is the caller; it is deliberately unpaginated, because the scope
// this project is honest about is a small catalog.
func (s *Store) List(ctx context.Context) ([]Product, error) {
	rows, err := s.q.ListProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list products: %w", err)
	}
	products := products(rows)
	if len(products) == 0 {
		return products, nil
	}

	// One query for every variant, then attach in memory: a join would fan the
	// product columns out per variant, and the catalog is small enough that two
	// round trips are cheaper than deduplicating rows.
	vrows, err := s.q.ListAllVariants(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list variants: %w", err)
	}
	return attachVariants(products, variants(vrows)), nil
}

// ListActive returns the products a customer may see: active products with
// their active variants, and only those that have at least one — a product with
// nothing purchasable under it is not a listing, it is a dead end.
//
// Out-of-stock variants are included. Hiding them would make a size silently
// disappear from a size selector, which reads as a bug; the storefront shows
// them as unavailable instead.
func (s *Store) ListActive(ctx context.Context) ([]Product, error) {
	rows, err := s.q.ListActiveProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list active products: %w", err)
	}
	products := products(rows)
	if len(products) == 0 {
		return products, nil
	}

	vrows, err := s.q.ListActiveVariants(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list active variants: %w", err)
	}
	return attachVariants(products, variants(vrows)), nil
}

// GetActiveBySlug returns one product for the storefront, with its active
// variants. An inactive product, or one whose every variant is inactive, is
// ErrNotFound: from outside, "withdrawn" and "never existed" are the same page.
func (s *Store) GetActiveBySlug(ctx context.Context, slug string) (Product, error) {
	row, err := s.q.GetActiveProductBySlug(ctx, slug)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: product: %w", err))
	}

	vrows, err := s.q.ListActiveVariantsByProduct(ctx, row.ID)
	if err != nil {
		return Product{}, fmt.Errorf("catalog: active variants: %w", err)
	}
	if len(vrows) == 0 {
		return Product{}, ErrNotFound
	}
	p := product(row)
	p.Variants = variants(vrows)
	return p, nil
}

// Get returns one product with its variants.
func (s *Store) Get(ctx context.Context, id string) (Product, error) {
	row, err := s.q.GetProduct(ctx, id)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: product: %w", err))
	}
	p := product(row)
	if p.Variants, err = s.Variants(ctx, p.ID); err != nil {
		return Product{}, err
	}
	return p, nil
}

// GetBySlug returns one product by its public slug, with its variants.
func (s *Store) GetBySlug(ctx context.Context, slug string) (Product, error) {
	row, err := s.q.GetProductBySlug(ctx, slug)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: product: %w", err))
	}
	p := product(row)
	if p.Variants, err = s.Variants(ctx, p.ID); err != nil {
		return Product{}, err
	}
	return p, nil
}

// Variants returns one product's variants in display order.
func (s *Store) Variants(ctx context.Context, productID string) ([]Variant, error) {
	rows, err := s.q.ListVariantsByProduct(ctx, productID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: variants: %w", err))
	}
	return variants(rows), nil
}

// Create inserts a product. The database generates the id, so no UUID
// dependency reaches the binary.
func (s *Store) Create(ctx context.Context, p Product) (Product, error) {
	row, err := s.q.CreateProduct(ctx, gen.CreateProductParams{
		Kind:        p.Kind,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Active:      p.Active,
	})
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: create product: %w", err))
	}
	return product(row), nil
}

// Update writes every editable product column.
func (s *Store) Update(ctx context.Context, p Product) (Product, error) {
	row, err := s.q.UpdateProduct(ctx, gen.UpdateProductParams{
		ID:          p.ID,
		Kind:        p.Kind,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Active:      p.Active,
	})
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: update product: %w", err))
	}
	return product(row), nil
}

// SetImage points a product at an uploaded object, returning the product as
// stored. The caller deletes the object that was there before, if any, *after*
// this returns: an orphaned object costs a few kilobytes, while an object deleted
// out from under a live row is a broken image on the storefront.
func (s *Store) SetImage(ctx context.Context, id, imageURL, imageKey string) (Product, error) {
	row, err := s.q.SetProductImage(ctx, gen.SetProductImageParams{
		ID: id, ImageURL: imageURL, ImageKey: imageKey,
	})
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: set product image: %w", err))
	}
	return product(row), nil
}

// ClearImage removes a product's image, returning the product as stored. As with
// SetImage, deleting the object itself is the caller's job and happens afterwards.
func (s *Store) ClearImage(ctx context.Context, id string) (Product, error) {
	row, err := s.q.ClearProductImage(ctx, id)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: clear product image: %w", err))
	}
	return product(row), nil
}

// Delete removes a product; its variants go with it by cascade. It fails once
// an order references a variant, because purchase history must not be
// rewritable — deactivate instead.
func (s *Store) Delete(ctx context.Context, id string) error {
	affected, err := s.q.DeleteProduct(ctx, id)
	if err != nil {
		return translate(fmt.Errorf("catalog: delete product: %w", err))
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateVariant adds a variant to an existing product.
func (s *Store) CreateVariant(ctx context.Context, v Variant) (Variant, error) {
	row, err := s.q.CreateVariant(ctx, gen.CreateVariantParams{
		ProductID:  v.ProductID,
		SKU:        v.SKU,
		Size:       v.Size,
		Color:      v.Color,
		PriceCents: v.PriceCents,
		StockQty:   v.StockQty,
		Active:     v.Active,
	})
	if err != nil {
		return Variant{}, translate(fmt.Errorf("catalog: create variant: %w", err))
	}
	return variant(row), nil
}

// UpdateVariant writes every editable variant column. The product id is part of
// the WHERE clause so a mismatched pair from a URL updates nothing instead of
// editing another product's variant.
func (s *Store) UpdateVariant(ctx context.Context, v Variant) (Variant, error) {
	row, err := s.q.UpdateVariant(ctx, gen.UpdateVariantParams{
		ID:         v.ID,
		ProductID:  v.ProductID,
		SKU:        v.SKU,
		Size:       v.Size,
		Color:      v.Color,
		PriceCents: v.PriceCents,
		StockQty:   v.StockQty,
		Active:     v.Active,
	})
	if err != nil {
		return Variant{}, translate(fmt.Errorf("catalog: update variant: %w", err))
	}
	return variant(row), nil
}

// DeleteVariant removes one variant of one product.
func (s *Store) DeleteVariant(ctx context.Context, productID, id string) error {
	affected, err := s.q.DeleteVariant(ctx, gen.DeleteVariantParams{ID: id, ProductID: productID})
	if err != nil {
		return translate(fmt.Errorf("catalog: delete variant: %w", err))
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Upsert inserts or updates a product and its variants by their natural keys —
// slug for the product, SKU for each variant — in one transaction. This is what
// makes `cmd/seed` rerunnable: seeding twice must not duplicate the catalog or
// reset stock counts to a fixture's numbers on rows that already existed.
//
// Variants absent from p are left alone rather than deleted; a seed file is not
// authoritative over a catalog someone has since edited.
func (s *Store) Upsert(ctx context.Context, p Product) (Product, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Product{}, fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// The generated queries take any DBTX, so the same code runs inside a
	// transaction by being handed the transaction instead of the pool.
	q := s.q.WithTx(tx)

	row, err := q.UpsertProduct(ctx, gen.UpsertProductParams{
		Kind:        p.Kind,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Active:      p.Active,
	})
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: upsert product: %w", err))
	}
	out := product(row)

	out.Variants = make([]Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		vrow, err := q.UpsertVariant(ctx, gen.UpsertVariantParams{
			ProductID:  out.ID,
			SKU:        v.SKU,
			Size:       v.Size,
			Color:      v.Color,
			PriceCents: v.PriceCents,
			StockQty:   v.StockQty,
			Active:     v.Active,
		})
		if err != nil {
			return Product{}, translate(fmt.Errorf("catalog: upsert variant: %w", err))
		}
		out.Variants = append(out.Variants, variant(vrow))
	}

	if err := tx.Commit(ctx); err != nil {
		return Product{}, fmt.Errorf("catalog: commit: %w", err)
	}
	return out, nil
}

// attachVariants files each variant under its product, so the caller gets two
// queries' worth of rows arranged as the domain sees them.
func attachVariants(products []Product, variants []Variant) []Product {
	byID := make(map[string]int, len(products))
	for i, p := range products {
		products[i].Variants = []Variant{}
		byID[p.ID] = i
	}
	for _, v := range variants {
		if i, ok := byID[v.ProductID]; ok {
			products[i].Variants = append(products[i].Variants, v)
		}
	}
	return products
}

// The four functions below are the whole mapping between a database row and a
// domain value. They are by name rather than by position, which is the point of
// generating the scan: a column added or moved cannot silently land in the wrong
// field.

func product(r gen.Product) Product {
	return Product{
		ID:          r.ID,
		Kind:        r.Kind,
		Slug:        r.Slug,
		Title:       r.Title,
		Description: r.Description,
		ImageURL:    r.ImageURL,
		Active:      r.Active,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		ImageKey:    r.ImageKey,
	}
}

func products(rows []gen.Product) []Product {
	out := make([]Product, 0, len(rows))
	for _, r := range rows {
		out = append(out, product(r))
	}
	return out
}

func variant(r gen.ProductVariant) Variant {
	return Variant{
		ID:         r.ID,
		ProductID:  r.ProductID,
		SKU:        r.SKU,
		Size:       r.Size,
		Color:      r.Color,
		PriceCents: r.PriceCents,
		StockQty:   r.StockQty,
		Active:     r.Active,
	}
}

func variants(rows []gen.ProductVariant) []Variant {
	out := make([]Variant, 0, len(rows))
	for _, r := range rows {
		out = append(out, variant(r))
	}
	return out
}

// translate turns the driver's vocabulary into this package's: a missing row
// becomes ErrNotFound, a unique violation becomes a *ConflictError naming the
// field a form can highlight, and a malformed UUID becomes ErrNotFound — from
// outside, an id that could never exist and one that does not are the same answer.
//
// This replaced a hand-written isUUID() pre-check. Ids are strings all the way to
// the database now, so a malformed one arrives as error 22P02 rather than being
// caught before the round trip. One less thing to keep in step with Postgres's
// idea of a UUID, at the cost of a query that was always going to find nothing.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case "22P02": // invalid input syntax, i.e. not a UUID
			return ErrNotFound
		case "23503": // foreign key violation
			return ErrInUse
		case "23505": // unique violation
			switch {
			case strings.Contains(pgErr.ConstraintName, "slug"):
				return &ConflictError{Field: "slug"}
			case strings.Contains(pgErr.ConstraintName, "sku"):
				return &ConflictError{Field: "sku"}
			case strings.Contains(pgErr.ConstraintName, "size_color"):
				return &ConflictError{Field: "options"}
			}
		}
	}
	return err
}
