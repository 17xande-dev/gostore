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
	products = attachVariants(products, variants(vrows))

	// Categories are attached here and not in ListActive, because the admin list
	// shows them and storefront cards do not. The hot path does not pay for a
	// query nothing renders.
	crows, err := s.q.ListAllProductCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list product categories: %w", err)
	}
	return attachCategories(products, crows), nil
}

// Search describes one request for a page of the storefront catalog. The zero
// value is not useful: PageSize must be positive, and Page counts from 1, because
// that is what appears in the URL.
type Search struct {
	// Query is the shopper's words. It is matched three ways — stemmed full text,
	// trigram similarity and a plain substring — because each finds what the others
	// miss. Empty means "everything", and needs no special case anywhere.
	Query string

	// CategorySlugs widens rather than narrows: several selected slugs return the
	// union. These are kinds of thing rather than facets like size and colour, so
	// asking for a product that is simultaneously a book and apparel is almost
	// always asking for nothing.
	//
	// Slugs rather than ids, so a public URL parameter needs no lookup before the
	// search can run and a category renamed in the admin keeps its links working.
	CategorySlugs []string

	Page     int
	PageSize int
}

// Results is one page of the catalog plus the size of the whole filtered set, so
// a pager can be drawn without a second query that might disagree.
type Results struct {
	Products []Product
	Total    int
	Pages    int
}

// SearchActive returns one page of the products a customer may see, filtered and
// ordered by relevance. It is the storefront's only listing query: an unfiltered
// request is not a special case, because an empty query ranks every row 0 and the
// ordering falls through to the title.
func (s *Store) SearchActive(ctx context.Context, q Search) (Results, error) {
	if q.PageSize <= 0 {
		return Results{}, fmt.Errorf("catalog: search: page size must be positive")
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	// A slice, never nil: cardinality(NULL) is NULL rather than 0, so a nil array
	// would make the "no categories selected" test fail and match nothing at all.
	slugs := q.CategorySlugs
	if slugs == nil {
		slugs = []string{}
	}

	rows, err := s.q.SearchActiveProducts(ctx, gen.SearchActiveProductsParams{
		Q:             q.Query,
		CategorySlugs: slugs,
		PageSize:      int32(q.PageSize),
		PageOffset:    int32((q.Page - 1) * q.PageSize),
	})
	if err != nil {
		return Results{}, fmt.Errorf("catalog: search products: %w", err)
	}

	out := Results{Products: make([]Product, 0, len(rows))}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		// Every row carries the same window count, so reading it from the first is
		// reading it from all of them.
		out.Total = int(r.TotalCount)
		out.Products = append(out.Products, Product{
			ID:          r.ID,
			Slug:        r.Slug,
			Title:       r.Title,
			Description: r.Description,
			Active:      r.Active,
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.UpdatedAt,
			ImageKey:    r.ImageKey,
			Kind:        Kind(r.Kind),
			Option1Name: r.Option1Name,
			Option2Name: r.Option2Name,
			Option3Name: r.Option3Name,
		})
		ids = append(ids, r.ID)
	}
	out.Pages = (out.Total + q.PageSize - 1) / q.PageSize

	if len(ids) == 0 {
		return out, nil
	}
	vrows, err := s.q.ListActiveVariantsByProducts(ctx, ids)
	if err != nil {
		return Results{}, fmt.Errorf("catalog: search variants: %w", err)
	}
	out.Products = attachVariants(out.Products, variants(vrows))
	return out, nil
}

// NewestActive returns the most recently added products a customer may see, with
// their active variants. The index page is the caller, where it is example
// content rather than a listing — the catalog is what lists things.
//
// It shares SearchActive's visibility rules, so the front page can never show a
// product the catalog hides. Categories are deliberately not attached: the cards
// do not render them, so the front page does not pay for a second query.
func (s *Store) NewestActive(ctx context.Context, limit int) ([]Product, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("catalog: newest: limit must be positive")
	}

	rows, err := s.q.ListNewestActiveProducts(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("catalog: newest products: %w", err)
	}
	out := products(rows)
	if len(out) == 0 {
		// Non-nil, so a template's "if no products" branch is about an empty
		// catalog rather than about a nil this function happened to return.
		return out, nil
	}

	ids := make([]string, 0, len(out))
	for _, p := range out {
		ids = append(ids, p.ID)
	}
	vrows, err := s.q.ListActiveVariantsByProducts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("catalog: newest variants: %w", err)
	}
	return attachVariants(out, variants(vrows)), nil
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

// Get returns one product with its variants and its categories. The admin edit
// form is the caller, and it needs both.
func (s *Store) Get(ctx context.Context, id string) (Product, error) {
	row, err := s.q.GetProduct(ctx, id)
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: product: %w", err))
	}
	p := product(row)
	if p.Variants, err = s.Variants(ctx, p.ID); err != nil {
		return Product{}, err
	}
	if p.Categories, err = s.CategoriesByProduct(ctx, p.ID); err != nil {
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

// Create inserts a product and its category links. The database generates the
// id, so no UUID dependency reaches the binary.
//
// The links are written in the same transaction as the row, because a product
// that exists but is in none of the categories the operator ticked is a save
// that half worked, and nothing in the admin would say so.
func (s *Store) Create(ctx context.Context, p Product) (Product, error) {
	return s.inTx(ctx, func(q *gen.Queries) (Product, error) {
		row, err := q.CreateProduct(ctx, gen.CreateProductParams{
			Slug:        p.Slug,
			Title:       p.Title,
			Description: p.Description,
			Active:      p.Active,
			Kind:        string(p.kindOrDefault()),
			Option1Name: p.Option1Name,
			Option2Name: p.Option2Name,
			Option3Name: p.Option3Name,
		})
		if err != nil {
			return Product{}, translate(fmt.Errorf("catalog: create product: %w", err))
		}
		out := product(row)
		if out.Categories, err = setCategories(ctx, q, out.ID, p.Categories); err != nil {
			return Product{}, err
		}
		return out, nil
	})
}

// Update writes every editable product column, and replaces the product's
// category links with the submitted set — the form submits all of them, so a
// category that is not in p is one that was unticked.
func (s *Store) Update(ctx context.Context, p Product) (Product, error) {
	return s.inTx(ctx, func(q *gen.Queries) (Product, error) {
		if err := checkKindChange(ctx, q, p); err != nil {
			return Product{}, err
		}
		row, err := q.UpdateProduct(ctx, gen.UpdateProductParams{
			ID:          p.ID,
			Slug:        p.Slug,
			Title:       p.Title,
			Description: p.Description,
			Active:      p.Active,
			Kind:        string(p.kindOrDefault()),
			Option1Name: p.Option1Name,
			Option2Name: p.Option2Name,
			Option3Name: p.Option3Name,
		})
		if err != nil {
			return Product{}, translate(fmt.Errorf("catalog: update product: %w", err))
		}
		out := product(row)
		if out.Categories, err = setCategories(ctx, q, out.ID, p.Categories); err != nil {
			return Product{}, err
		}
		return out, nil
	})
}

// kindOrDefault is what actually reaches the database, so a zero-valued Product
// — from a seed file that says nothing, or a test literal — is a parcel rather
// than a CHECK constraint violation.
func (p Product) kindOrDefault() Kind {
	if p.Kind == "" {
		return KindPhysical
	}
	return p.Kind
}

// KindLockedError says a product's kind cannot change, and why.
//
// It is its own type rather than ErrInUse because the two refusals need different
// sentences on the form: one is "somebody bought this", the other is "there are
// still files attached", and the second tells the operator what to do next.
type KindLockedError struct {
	// Ordered is true when an order references this product, which freezes the
	// kind permanently.
	Ordered bool
	// Files is how many files are still attached, when that is the obstacle.
	Files int64
}

func (e *KindLockedError) Error() string {
	if e.Ordered {
		return "catalog: the kind of an ordered product cannot be changed"
	}
	return fmt.Sprintf("catalog: %d file(s) are still attached", e.Files)
}

// checkKindChange enforces the two rules that freeze a product's kind.
//
// Neither protects purchase history — order_items snapshots the kind, so a
// completed sale is already safe. What they protect is *live* state, where the
// failure is silent: flipping physical to digital leaves a stock count nothing
// decrements any more, and flipping the other way leaves files and live
// entitlements on a product that now ships.
//
// It reads the current row rather than trusting the submitted one, so a
// hand-crafted request cannot claim the product was already digital.
func checkKindChange(ctx context.Context, q *gen.Queries, p Product) error {
	current, err := q.GetProduct(ctx, p.ID)
	if err != nil {
		return translate(fmt.Errorf("catalog: read product: %w", err))
	}
	if Kind(current.Kind) == p.kindOrDefault() {
		return nil
	}

	ordered, err := q.CountProductOrderItems(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("catalog: count order items: %w", err)
	}
	if ordered > 0 {
		return &KindLockedError{Ordered: true}
	}

	// Only in this direction. Going physical→digital with no files is the ordinary
	// way a digital product is created, and refusing it would make the kind
	// unsettable. Going digital→physical while files exist would strand objects in
	// the bucket with nothing in the admin still listing them — and deleting them
	// as a side effect of a dropdown is worse, so the operator is asked to do it.
	if p.kindOrDefault() == KindPhysical {
		files, err := q.CountProductFiles(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("catalog: count files: %w", err)
		}
		if files > 0 {
			return &KindLockedError{Files: files}
		}
	}
	return nil
}

// inTx runs fn against a transaction and commits it, so the several writes a
// product save has become still land or fail together.
func (s *Store) inTx(ctx context.Context, fn func(*gen.Queries) (Product, error)) (Product, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Product{}, fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	out, err := fn(s.q.WithTx(tx))
	if err != nil {
		return Product{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Product{}, fmt.Errorf("catalog: commit: %w", err)
	}
	return out, nil
}

// setCategories replaces a product's category links and returns them as stored.
// Reading them back rather than echoing the input is what makes an id that no
// longer names a category visible: it simply is not in the result.
func setCategories(ctx context.Context, q *gen.Queries, productID string, categories []Category) ([]Category, error) {
	if err := q.ClearProductCategories(ctx, productID); err != nil {
		return nil, translate(fmt.Errorf("catalog: clear product categories: %w", err))
	}
	for _, c := range categories {
		err := q.AddProductCategory(ctx, gen.AddProductCategoryParams{
			ProductID: productID, CategoryID: c.ID,
		})
		if err != nil {
			return nil, translate(fmt.Errorf("catalog: link category: %w", err))
		}
	}
	rows, err := q.ListCategoriesByProduct(ctx, productID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: product categories: %w", err))
	}
	return categoriesOf(rows), nil
}

// SetImage points a product at an uploaded object, returning the product as
// stored. The caller deletes the object that was there before, if any, *after*
// this returns: an orphaned object costs a few kilobytes, while an object deleted
// out from under a live row is a broken image on the storefront.
//
// Only the key is given, because the URL is not a stored fact — it is the key
// resolved against whichever backend is running.
func (s *Store) SetImage(ctx context.Context, id, imageKey string) (Product, error) {
	row, err := s.q.SetProductImage(ctx, gen.SetProductImageParams{
		ID: id, ImageKey: imageKey,
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
		Option1:    v.Option1,
		Option2:    v.Option2,
		Option3:    v.Option3,
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
		Option1:    v.Option1,
		Option2:    v.Option2,
		Option3:    v.Option3,
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

// Categories returns the whole taxonomy in display order. It is small and read
// by every admin product form, so it is one unfiltered query.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list categories: %w", err)
	}
	return categoriesOf(rows), nil
}

// CategoriesByProduct returns one product's categories in display order.
func (s *Store) CategoriesByProduct(ctx context.Context, productID string) ([]Category, error) {
	rows, err := s.q.ListCategoriesByProduct(ctx, productID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: product categories: %w", err))
	}
	return categoriesOf(rows), nil
}

// Category returns one category.
func (s *Store) Category(ctx context.Context, id string) (Category, error) {
	row, err := s.q.GetCategory(ctx, id)
	if err != nil {
		return Category{}, translate(fmt.Errorf("catalog: category: %w", err))
	}
	return category(row), nil
}

// CreateCategory inserts a category.
func (s *Store) CreateCategory(ctx context.Context, c Category) (Category, error) {
	row, err := s.q.CreateCategory(ctx, gen.CreateCategoryParams{
		Slug: c.Slug, Name: c.Name, Position: c.Position,
	})
	if err != nil {
		return Category{}, translate(fmt.Errorf("catalog: create category: %w", err))
	}
	return category(row), nil
}

// UpdateCategory writes every editable category column.
func (s *Store) UpdateCategory(ctx context.Context, c Category) (Category, error) {
	row, err := s.q.UpdateCategory(ctx, gen.UpdateCategoryParams{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Position: c.Position,
	})
	if err != nil {
		return Category{}, translate(fmt.Errorf("catalog: update category: %w", err))
	}
	return category(row), nil
}

// DeleteCategory removes a category and reports how many products it was
// unlinked from. Unlike deleting a product, this is never refused: the join rows
// go with it by cascade and the products themselves are untouched, because a
// taxonomy edit must not delete catalog entries.
//
// The count is returned because without it the operator cannot tell a category
// nothing used from one fifty products used — both would look like the same
// silent success.
func (s *Store) DeleteCategory(ctx context.Context, id string) (int64, error) {
	linked, err := s.q.CountCategoryProducts(ctx, id)
	if err != nil {
		return 0, translate(fmt.Errorf("catalog: count category products: %w", err))
	}
	affected, err := s.q.DeleteCategory(ctx, id)
	if err != nil {
		return 0, translate(fmt.Errorf("catalog: delete category: %w", err))
	}
	if affected == 0 {
		return 0, ErrNotFound
	}
	return linked, nil
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
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Active:      p.Active,
		Kind:        string(p.kindOrDefault()),
		Option1Name: p.Option1Name,
		Option2Name: p.Option2Name,
		Option3Name: p.Option3Name,
	})
	if err != nil {
		return Product{}, translate(fmt.Errorf("catalog: upsert product: %w", err))
	}
	out := product(row)

	// A seed file names categories by slug, because it has no ids to refer to.
	// Each is created if it is new and left exactly as it is if it already exists,
	// so re-seeding never renames or reorders a taxonomy the operator has edited.
	//
	// Links are added, never cleared first: a fixture is not authoritative over a
	// catalog somebody has since categorised, for the same reason its stock counts
	// are not.
	for _, slug := range p.CategorySlugs {
		if _, err := q.UpsertCategory(ctx, gen.UpsertCategoryParams{
			Slug: slug, Name: TitleFromSlug(slug),
		}); err != nil {
			return Product{}, translate(fmt.Errorf("catalog: upsert category %q: %w", slug, err))
		}
		if err := q.AddProductCategoryBySlug(ctx, gen.AddProductCategoryBySlugParams{
			ProductID: out.ID, Slug: slug,
		}); err != nil {
			return Product{}, translate(fmt.Errorf("catalog: link category %q: %w", slug, err))
		}
	}
	if out.Categories, err = categoriesFrom(ctx, q, out.ID); err != nil {
		return Product{}, err
	}

	out.Variants = make([]Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		vrow, err := q.UpsertVariant(ctx, gen.UpsertVariantParams{
			ProductID:  out.ID,
			SKU:        v.SKU,
			Option1:    v.Option1,
			Option2:    v.Option2,
			Option3:    v.Option3,
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

// categoriesFrom reads one product's categories through whichever handle it is
// given, so the same read works inside a transaction and outside one.
func categoriesFrom(ctx context.Context, q *gen.Queries, productID string) ([]Category, error) {
	rows, err := q.ListCategoriesByProduct(ctx, productID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: product categories: %w", err))
	}
	return categoriesOf(rows), nil
}

// attachCategories files each category under its product, the same shape as
// attachVariants and for the same reason.
func attachCategories(products []Product, rows []gen.ListAllProductCategoriesRow) []Product {
	byID := make(map[string]int, len(products))
	for i, p := range products {
		products[i].Categories = []Category{}
		byID[p.ID] = i
	}
	for _, r := range rows {
		if i, ok := byID[r.ProductID]; ok {
			products[i].Categories = append(products[i].Categories, category(r.Category))
		}
	}
	return products
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
		Slug:        r.Slug,
		Title:       r.Title,
		Description: r.Description,
		Active:      r.Active,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		ImageKey:    r.ImageKey,
		Kind:        Kind(r.Kind),
		Option1Name: r.Option1Name,
		Option2Name: r.Option2Name,
		Option3Name: r.Option3Name,
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
		Option1:    r.Option1,
		Option2:    r.Option2,
		Option3:    r.Option3,
		PriceCents: r.PriceCents,
		StockQty:   r.StockQty,
		Active:     r.Active,
	}
}

func category(r gen.Category) Category {
	return Category{
		ID:       r.ID,
		Slug:     r.Slug,
		Name:     r.Name,
		Position: r.Position,
	}
}

func categoriesOf(rows []gen.Category) []Category {
	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		out = append(out, category(r))
	}
	return out
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
			case strings.Contains(pgErr.ConstraintName, "options"):
				return &ConflictError{Field: "options"}
			}
		}
	}
	return err
}

// Files returns a product's downloadable files, each carrying the variants that
// include it.
//
// Two queries rather than a join, matching how variants and categories are
// attached: a join would fan each file's columns out once per variant, and a
// digital product has a handful of files rather than thousands.
func (s *Store) Files(ctx context.Context, productID string) ([]File, error) {
	rows, err := s.q.ListProductFiles(ctx, productID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: list files: %w", err))
	}
	out := make([]File, 0, len(rows))
	for _, r := range rows {
		out = append(out, file(r))
	}
	if len(out) == 0 {
		return out, nil
	}

	links, err := s.q.ListProductFileVariants(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("catalog: list file variants: %w", err)
	}
	byFile := make(map[int64][]string, len(out))
	for _, l := range links {
		byFile[l.FileID] = append(byFile[l.FileID], l.VariantID)
	}
	for i := range out {
		out[i].VariantIDs = byFile[out[i].ID]
	}
	return out, nil
}

// VariantFiles returns the files one variant grants, which is exactly what a
// buyer of that variant may download.
func (s *Store) VariantFiles(ctx context.Context, variantID string) ([]File, error) {
	rows, err := s.q.ListVariantFiles(ctx, variantID)
	if err != nil {
		return nil, translate(fmt.Errorf("catalog: list variant files: %w", err))
	}
	out := make([]File, 0, len(rows))
	for _, r := range rows {
		out = append(out, file(r))
	}
	return out, nil
}

// File returns one file of one product. The product id is part of the lookup, so
// a file id from another product's page finds nothing rather than leaking a title.
func (s *Store) File(ctx context.Context, productID string, id int64) (File, error) {
	row, err := s.q.GetProductFile(ctx, gen.GetProductFileParams{ID: id, ProductID: productID})
	if err != nil {
		return File{}, translate(fmt.Errorf("catalog: get file: %w", err))
	}
	return file(row), nil
}

// AddFile records an uploaded file and links it to the given variants, in one
// transaction so a file can never exist with no variants because the second write
// failed — which would be a file nobody can download and nothing in the admin
// explains.
//
// The object is already in storage by the time this is called. That order is
// deliberate and matches the image path: a row pointing at bytes that are not
// there is a broken download, while bytes with no row are a logged orphan.
func (s *Store) AddFile(ctx context.Context, f File, variantIDs []string) (File, error) {
	var out File
	err := s.tx(ctx, func(q *gen.Queries) error {
		pos, err := q.NextProductFilePosition(ctx, f.ProductID)
		if err != nil {
			return fmt.Errorf("catalog: next file position: %w", err)
		}
		row, err := q.CreateProductFile(ctx, gen.CreateProductFileParams{
			ProductID:        f.ProductID,
			Position:         int(pos),
			Title:            f.Title,
			ObjectKey:        f.ObjectKey,
			OriginalFilename: f.OriginalFilename,
			ContentType:      f.ContentType,
			SizeBytes:        f.SizeBytes,
		})
		if err != nil {
			return translate(fmt.Errorf("catalog: create file: %w", err))
		}
		out = file(row)
		out.VariantIDs, err = setFileVariants(ctx, q, f.ProductID, out.ID, variantIDs)
		return err
	})
	if err != nil {
		return File{}, err
	}
	return out, nil
}

// UpdateFile renames a file and rewrites which variants include it. The object
// key is deliberately not touched: bytes are replaced by uploading a new file,
// never by repointing a row, because repointing would orphan the old object with
// nothing recording that it existed.
func (s *Store) UpdateFile(ctx context.Context, f File, variantIDs []string) (File, error) {
	var out File
	err := s.tx(ctx, func(q *gen.Queries) error {
		row, err := q.UpdateProductFile(ctx, gen.UpdateProductFileParams{
			ID:        f.ID,
			ProductID: f.ProductID,
			Title:     f.Title,
			Position:  f.Position,
		})
		if err != nil {
			return translate(fmt.Errorf("catalog: update file: %w", err))
		}
		out = file(row)
		out.VariantIDs, err = setFileVariants(ctx, q, f.ProductID, out.ID, variantIDs)
		return err
	})
	if err != nil {
		return File{}, err
	}
	return out, nil
}

// DeleteFile removes the row and returns the object key, so the caller can delete
// the bytes afterwards.
//
// The row goes first and the object second, which is the reverse of AddFile and
// right for the same reason: the end state has no file either way, and a deleted
// object still referenced by a live row would be a download that 500s, while a
// row deleted before its object is a logged orphan.
//
// download_events referencing this file cascade away with it. That is a real loss
// of history and it is the right trade: the alternative is keeping rows that point
// at a file whose title nobody can look up, and the statistics a shop owner wants
// are about files they still sell.
func (s *Store) DeleteFile(ctx context.Context, productID string, id int64) (string, error) {
	key, err := s.q.DeleteProductFile(ctx, gen.DeleteProductFileParams{ID: id, ProductID: productID})
	if err != nil {
		return "", translate(fmt.Errorf("catalog: delete file: %w", err))
	}
	return key, nil
}

// setFileVariants replaces a file's variant links, returning the ids that were
// actually written.
//
// Clear-then-insert rather than a diff: the form submits the whole set every
// time, which is what makes unticking one mean something, and the set is a
// handful of rows.
func setFileVariants(ctx context.Context, q *gen.Queries, productID string, fileID int64, variantIDs []string) ([]string, error) {
	if err := q.ClearFileVariants(ctx, fileID); err != nil {
		return nil, fmt.Errorf("catalog: clear file variants: %w", err)
	}
	written := make([]string, 0, len(variantIDs))
	for _, id := range variantIDs {
		// The insert checks the variant belongs to this product, so a variant id
		// borrowed from another product's page links nothing instead of granting
		// its buyers somebody else's files.
		err := q.AddFileVariant(ctx, gen.AddFileVariantParams{
			VariantID: id, FileID: fileID, ProductID: productID,
		})
		if err != nil {
			return nil, translate(fmt.Errorf("catalog: link file to variant: %w", err))
		}
		written = append(written, id)
	}
	return written, nil
}

// tx is inTx for the writes that do not return a Product.
func (s *Store) tx(ctx context.Context, fn func(*gen.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("catalog: commit: %w", err)
	}
	return nil
}

func file(r gen.ProductFile) File {
	return File{
		ID:               r.ID,
		ProductID:        r.ProductID,
		Position:         r.Position,
		Title:            r.Title,
		ObjectKey:        r.ObjectKey,
		OriginalFilename: r.OriginalFilename,
		ContentType:      r.ContentType,
		SizeBytes:        r.SizeBytes,
		CreatedAt:        r.CreatedAt,
	}
}

// KindChangeBlockers reports the two facts that freeze a product's kind: how many
// order items reference it, and how many files are attached.
//
// Exported so the admin form can say *before* the operator tries, rather than
// only refusing on submit. It is not the guard — checkKindChange re-reads both
// inside Update's transaction, against the stored row rather than the submitted
// one — because a check done at render time is already stale by the time the form
// comes back.
func (s *Store) KindChangeBlockers(ctx context.Context, productID string) (ordered, files int64, err error) {
	if ordered, err = s.q.CountProductOrderItems(ctx, productID); err != nil {
		return 0, 0, translate(fmt.Errorf("catalog: count order items: %w", err))
	}
	if files, err = s.q.CountProductFiles(ctx, productID); err != nil {
		return 0, 0, translate(fmt.Errorf("catalog: count files: %w", err))
	}
	return ordered, files, nil
}
