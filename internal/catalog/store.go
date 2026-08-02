package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const productColumns = `id::text, kind, slug, title, description, image_url, active, created_at, updated_at`
const variantColumns = `id::text, product_id::text, sku, size, color, price_cents, stock_qty, active`

// List returns every product with its variants attached, ordered by title. The
// admin list is the caller; it is deliberately unpaginated, because the scope
// this project is honest about is a small catalog.
func (s *Store) List(ctx context.Context) ([]Product, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+productColumns+` FROM products ORDER BY title`)
	if err != nil {
		return nil, fmt.Errorf("catalog: list products: %w", err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, err
	}
	if len(products) == 0 {
		return products, nil
	}

	// One query for every variant, then attach in memory: a join would fan the
	// product columns out per variant, and the catalog is small enough that two
	// round trips are cheaper than deduplicating rows.
	vrows, err := s.pool.Query(ctx, `SELECT `+variantColumns+` FROM product_variants ORDER BY size, color, sku`)
	if err != nil {
		return nil, fmt.Errorf("catalog: list variants: %w", err)
	}
	variants, err := collectVariants(vrows)
	if err != nil {
		return nil, err
	}

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
	return products, nil
}

// Get returns one product with its variants.
func (s *Store) Get(ctx context.Context, id string) (Product, error) {
	if !isUUID(id) {
		return Product{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1`, id)
	p, err := scanProduct(row)
	if err != nil {
		return Product{}, err
	}
	p.Variants, err = s.Variants(ctx, p.ID)
	if err != nil {
		return Product{}, err
	}
	return p, nil
}

// GetBySlug returns one product by its public slug, with its variants.
func (s *Store) GetBySlug(ctx context.Context, slug string) (Product, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE slug = $1`, slug)
	p, err := scanProduct(row)
	if err != nil {
		return Product{}, err
	}
	p.Variants, err = s.Variants(ctx, p.ID)
	if err != nil {
		return Product{}, err
	}
	return p, nil
}

// Variants returns one product's variants in display order.
func (s *Store) Variants(ctx context.Context, productID string) ([]Variant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+variantColumns+` FROM product_variants WHERE product_id = $1 ORDER BY size, color, sku`, productID)
	if err != nil {
		return nil, fmt.Errorf("catalog: variants: %w", err)
	}
	return collectVariants(rows)
}

// Create inserts a product. The database generates the id, so no UUID
// dependency reaches the binary.
func (s *Store) Create(ctx context.Context, p Product) (Product, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO products (id, kind, slug, title, description, image_url, active)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		 RETURNING `+productColumns,
		p.Kind, p.Slug, p.Title, p.Description, p.ImageURL, p.Active)
	return scanProduct(row)
}

// Update writes every editable product column. updated_at is maintained here
// rather than by a trigger, so the write is visible in the query itself.
func (s *Store) Update(ctx context.Context, p Product) (Product, error) {
	if !isUUID(p.ID) {
		return Product{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE products
		 SET kind = $2, slug = $3, title = $4, description = $5, image_url = $6, active = $7, updated_at = now()
		 WHERE id = $1
		 RETURNING `+productColumns,
		p.ID, p.Kind, p.Slug, p.Title, p.Description, p.ImageURL, p.Active)
	return scanProduct(row)
}

// Delete removes a product; its variants go with it by cascade. It fails once
// an order references a variant, because purchase history must not be
// rewritable — deactivate instead.
func (s *Store) Delete(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return translate(fmt.Errorf("catalog: delete product: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateVariant adds a variant to an existing product.
func (s *Store) CreateVariant(ctx context.Context, v Variant) (Variant, error) {
	if !isUUID(v.ProductID) {
		return Variant{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO product_variants (id, product_id, sku, size, color, price_cents, stock_qty, active)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+variantColumns,
		v.ProductID, v.SKU, v.Size, v.Color, v.PriceCents, v.StockQty, v.Active)
	return scanVariant(row)
}

// UpdateVariant writes every editable variant column. The product id is part of
// the WHERE clause so a mismatched pair from a URL updates nothing instead of
// editing another product's variant.
func (s *Store) UpdateVariant(ctx context.Context, v Variant) (Variant, error) {
	if !isUUID(v.ID) || !isUUID(v.ProductID) {
		return Variant{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE product_variants
		 SET sku = $3, size = $4, color = $5, price_cents = $6, stock_qty = $7, active = $8
		 WHERE id = $1 AND product_id = $2
		 RETURNING `+variantColumns,
		v.ID, v.ProductID, v.SKU, v.Size, v.Color, v.PriceCents, v.StockQty, v.Active)
	return scanVariant(row)
}

// DeleteVariant removes one variant of one product.
func (s *Store) DeleteVariant(ctx context.Context, productID, id string) error {
	if !isUUID(productID) || !isUUID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM product_variants WHERE id = $1 AND product_id = $2`, id, productID)
	if err != nil {
		return translate(fmt.Errorf("catalog: delete variant: %w", err))
	}
	if tag.RowsAffected() == 0 {
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

	row := tx.QueryRow(ctx,
		`INSERT INTO products (id, kind, slug, title, description, image_url, active)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		 ON CONFLICT (slug) DO UPDATE
		 SET kind = EXCLUDED.kind, title = EXCLUDED.title, description = EXCLUDED.description,
		     image_url = EXCLUDED.image_url, active = EXCLUDED.active, updated_at = now()
		 RETURNING `+productColumns,
		p.Kind, p.Slug, p.Title, p.Description, p.ImageURL, p.Active)
	out, err := scanProduct(row)
	if err != nil {
		return Product{}, err
	}

	out.Variants = make([]Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		vrow := tx.QueryRow(ctx,
			`INSERT INTO product_variants (id, product_id, sku, size, color, price_cents, stock_qty, active)
			 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (sku) DO UPDATE
			 SET product_id = EXCLUDED.product_id, size = EXCLUDED.size, color = EXCLUDED.color,
			     price_cents = EXCLUDED.price_cents, active = EXCLUDED.active
			 RETURNING `+variantColumns,
			out.ID, v.SKU, v.Size, v.Color, v.PriceCents, v.StockQty, v.Active)
		got, err := scanVariant(vrow)
		if err != nil {
			return Product{}, err
		}
		out.Variants = append(out.Variants, got)
	}

	if err := tx.Commit(ctx); err != nil {
		return Product{}, fmt.Errorf("catalog: commit: %w", err)
	}
	return out, nil
}

func scanProduct(row pgx.Row) (Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.Kind, &p.Slug, &p.Title, &p.Description, &p.ImageURL, &p.Active, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: product: %w", err))
	}
	return p, nil
}

func scanVariant(row pgx.Row) (Variant, error) {
	var v Variant
	err := row.Scan(&v.ID, &v.ProductID, &v.SKU, &v.Size, &v.Color, &v.PriceCents, &v.StockQty, &v.Active)
	if err != nil {
		return Variant{}, translate(fmt.Errorf("catalog: variant: %w", err))
	}
	return v, nil
}

func collectProducts(rows pgx.Rows) ([]Product, error) {
	defer rows.Close()
	products := []Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

func collectVariants(rows pgx.Rows) ([]Variant, error) {
	defer rows.Close()
	variants := []Variant{}
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		variants = append(variants, v)
	}
	return variants, rows.Err()
}

// translate turns the driver's vocabulary into this package's: a missing row
// becomes ErrNotFound, a unique violation becomes a *ConflictError naming the
// field a form can highlight.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
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

// isUUID keeps a malformed path parameter from reaching Postgres, where it
// would come back as a syntax error and read like a server fault rather than
// the 404 it is.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
