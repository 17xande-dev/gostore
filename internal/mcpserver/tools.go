package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/17xande-dev/gostore/internal/admin"
	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools. Deliberately absent, each for a reason:
//
//   - Bytes in a tool call. An image goes through create_image_upload's URL
//     instead (see upload.go), and the store never fetches one from a URL it is
//     given, which would make it a request-forger.
//   - Downloadable-file uploads. A digital file can be gigabytes, which is the
//     web admin's job; get_product returns the admin page to upload at.
//   - Administrator accounts. Passwords, roles and the rules against acting on
//     yourself are not something to hand an assistant: an agent that can create
//     an owner is an escalation path.
//   - Payment state. The admin cannot change it either.

var (
	no  = false
	yes = true
)

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: &no}
}

func additive(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &no, OpenWorldHint: &no}
}

func editing(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &yes, IdempotentHint: true, OpenWorldHint: &no}
}

func (s *Server) registerTools() {
	add(s, auth.PermRead, &mcp.Tool{Name: "list_products", Annotations: readOnly("List products"),
		Description: "Every product with its variants, active or not, ordered by title."}, s.listProducts)
	add(s, auth.PermRead, &mcp.Tool{Name: "get_product", Annotations: readOnly("Get a product"),
		Description: "One product by id: variants, categories, image URL, attached files, and the admin page for uploading downloadable files."}, s.getProduct)
	add(s, auth.PermRead, &mcp.Tool{Name: "list_categories", Annotations: readOnly("List categories"),
		Description: "Every category, in display order."}, s.listCategories)
	add(s, auth.PermRead, &mcp.Tool{Name: "search_orders", Annotations: readOnly("Search orders"),
		Description: "Orders newest first, 50 a page, optionally matching text in the id, customer name or email, and optionally filtered."}, s.searchOrders)
	add(s, auth.PermRead, &mcp.Tool{Name: "get_order", Annotations: readOnly("Get an order"),
		Description: "One order by id: items, customer, payment facts, fulfilment, download entitlements and email delivery."}, s.getOrder)

	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "create_product", Annotations: additive("Create a product"),
		Description: "Creates a product. It cannot be bought until it has an active variant (create_variant). A blank slug is derived from the title."}, s.createProduct)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "update_product", Annotations: editing("Update a product"),
		Description: "Changes the fields given and leaves the rest. category_ids, when given, replaces the product's categories. The kind cannot change once the product has been ordered."}, s.updateProduct)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "delete_product", Annotations: editing("Delete a product"),
		Description: "Deletes a product that has never been ordered. An ordered one is refused: deactivate it with update_product instead."}, s.deleteProduct)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "create_image_upload", Annotations: additive("Create an image upload URL"),
		Description: "Returns a single-use URL, valid for 15 minutes, that sets a product's image: send the image file's raw bytes to it as the body of a PUT (or POST), e.g. `curl --fail -T photo.jpg <upload_url>`. JPEG, PNG, GIF or WebP, up to 5 MB. A new image replaces the old one. The response to the upload is JSON with the new image_url, or an error."}, s.createImageUpload)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "create_variant", Annotations: additive("Create a variant"),
		Description: "Adds a purchasable variant to a product: a SKU, its option values in the order of the product's option names, a price and stock."}, s.createVariant)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "update_variant", Annotations: editing("Update a variant"),
		Description: "Changes the fields given and leaves the rest."}, s.updateVariant)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "delete_variant", Annotations: editing("Delete a variant"),
		Description: "Deletes a variant that has never been ordered. An ordered one is refused: deactivate it instead."}, s.deleteVariant)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "create_category", Annotations: additive("Create a category"),
		Description: "Creates a category. A blank slug is derived from the name; lower positions are shown first."}, s.createCategory)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "update_category", Annotations: editing("Update a category"),
		Description: "Changes the fields given and leaves the rest."}, s.updateCategory)
	add(s, auth.PermCatalogWrite, &mcp.Tool{Name: "delete_category", Annotations: editing("Delete a category"),
		Description: "Deletes a category and removes it from its products. The products themselves are untouched."}, s.deleteCategory)

	add(s, auth.PermOrdersWrite, &mcp.Tool{Name: "set_fulfillment", Annotations: editing("Set fulfilment"),
		Description: "Marks a paid order with physical items as shipped (or not), with an optional tracking reference and internal note. Payment state cannot be changed."}, s.setFulfillment)
	add(s, auth.PermOrdersWrite, &mcp.Tool{Name: "retry_order_email", Annotations: additive("Retry order emails"),
		Description: "Queues an order's undelivered emails to be sent again now."}, s.retryOrderEmail)
	add(s, auth.PermOrdersWrite, &mcp.Tool{Name: "revoke_entitlement", Annotations: editing("Revoke a download"),
		Description: "Stops a buyer's download link working. Reversible with restore_entitlement; the order and its payment are untouched."}, s.revokeEntitlement)
	add(s, auth.PermOrdersWrite, &mcp.Tool{Name: "restore_entitlement", Annotations: editing("Restore a download"),
		Description: "Reverses revoke_entitlement."}, s.restoreEntitlement)
}

// --- shapes -------------------------------------------------------------

type Category struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type Variant struct {
	ID       string   `json:"id"`
	SKU      string   `json:"sku"`
	Options  []string `json:"options" jsonschema:"option values, in the order of the product's option_names"`
	Price    string   `json:"price" jsonschema:"decimal amount in the store's currency, e.g. 149.99"`
	StockQty int      `json:"stock_qty" jsonschema:"units in stock; ignored for digital products"`
	Active   bool     `json:"active"`
}

type File struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
}

type Product struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Kind        string     `json:"kind" jsonschema:"physical or digital"`
	Active      bool       `json:"active"`
	OptionNames []string   `json:"option_names"`
	Variants    []Variant  `json:"variants"`
	Categories  []Category `json:"categories,omitempty"`
	ImageURL    string     `json:"image_url,omitempty"`
	Files       []File     `json:"files,omitempty"`
	AdminURL    string     `json:"admin_url" jsonschema:"the product's page in the web admin, where downloadable files are uploaded"`
}

func categoryOut(c catalog.Category) Category {
	return Category{ID: c.ID, Slug: c.Slug, Name: c.Name, Position: c.Position}
}

func variantOut(v catalog.Variant) Variant {
	return Variant{
		ID: v.ID, SKU: v.SKU, Options: trimOptions([]string{v.Option1, v.Option2, v.Option3}),
		Price: catalog.FormatPrice(v.PriceCents), StockQty: v.StockQty, Active: v.Active,
	}
}

func (s *Server) productOut(p catalog.Product) Product {
	out := Product{
		ID: p.ID, Slug: p.Slug, Title: p.Title, Description: p.Description,
		Kind: string(p.Kind), Active: p.Active,
		OptionNames: trimOptions([]string{p.Option1Name, p.Option2Name, p.Option3Name}),
		Variants:    make([]Variant, 0, len(p.Variants)),
		AdminURL:    s.d.BaseURL + "/admin/products/" + p.ID + "/edit",
	}
	for _, v := range p.Variants {
		out.Variants = append(out.Variants, variantOut(v))
	}
	for _, c := range p.Categories {
		out.Categories = append(out.Categories, categoryOut(c))
	}
	if p.ImageKey != "" {
		out.ImageURL = s.imageURL(p.ImageKey)
	}
	for _, f := range p.Files {
		out.Files = append(out.Files, File{ID: f.ID, Title: f.Title, Filename: f.OriginalFilename, SizeBytes: f.SizeBytes})
	}
	return out
}

// trimOptions drops trailing empty option slots, so a product with one option
// reads as one name rather than three with two blank.
func trimOptions(opts []string) []string {
	for len(opts) > 0 && opts[len(opts)-1] == "" {
		opts = opts[:len(opts)-1]
	}
	return opts
}

// slots spreads up to three option values over the three columns.
func slots(opts []string) ([catalog.OptionSlots]string, error) {
	var out [catalog.OptionSlots]string
	if len(opts) > catalog.OptionSlots {
		return out, refuse("at most %d options", catalog.OptionSlots)
	}
	copy(out[:], opts)
	return out, nil
}

// --- reads ----------------------------------------------------------------

type none struct{}

type productList struct {
	Products []Product `json:"products"`
}

func (s *Server) listProducts(ctx context.Context, _ auth.User, _ none) (productList, error) {
	ps, err := s.d.Catalog.List(ctx)
	if err != nil {
		return productList{}, err
	}
	out := productList{Products: make([]Product, 0, len(ps))}
	for _, p := range ps {
		out.Products = append(out.Products, s.productOut(p))
	}
	return out, nil
}

type byID struct {
	ID string `json:"id"`
}

func (s *Server) loadProduct(ctx context.Context, id string) (catalog.Product, error) {
	p, err := s.d.Catalog.Get(ctx, id)
	if err != nil {
		return catalog.Product{}, err
	}
	if p.Digital() {
		if p.Files, err = s.d.Catalog.Files(ctx, p.ID); err != nil {
			return catalog.Product{}, err
		}
	}
	return p, nil
}

func (s *Server) getProduct(ctx context.Context, _ auth.User, in byID) (Product, error) {
	p, err := s.loadProduct(ctx, in.ID)
	if err != nil {
		return Product{}, err
	}
	return s.productOut(p), nil
}

type categoryList struct {
	Categories []Category `json:"categories"`
}

func (s *Server) listCategories(ctx context.Context, _ auth.User, _ none) (categoryList, error) {
	cs, err := s.d.Catalog.Categories(ctx)
	if err != nil {
		return categoryList{}, err
	}
	out := categoryList{Categories: make([]Category, 0, len(cs))}
	for _, c := range cs {
		out.Categories = append(out.Categories, categoryOut(c))
	}
	return out, nil
}

type orderSearch struct {
	Search string `json:"search,omitempty" jsonschema:"text to match in the order id, customer name or email"`
	Filter string `json:"filter,omitempty" jsonschema:"oversold, email (an email not yet delivered) or unfulfilled (paid, physical, not shipped)"`
	Page   int    `json:"page,omitempty" jsonschema:"1-based; defaults to 1"`
}

type OrderSummary struct {
	ID        string     `json:"id"`
	Reference string     `json:"reference"`
	Status    string     `json:"status"`
	Total     string     `json:"total"`
	Currency  string     `json:"currency"`
	Customer  string     `json:"customer"`
	Email     string     `json:"email"`
	CreatedAt time.Time  `json:"created_at"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
	Fulfilled bool       `json:"fulfilled"`
	Oversold  bool       `json:"oversold,omitempty"`
}

type orderList struct {
	Orders   []OrderSummary `json:"orders"`
	NextPage int            `json:"next_page,omitempty" jsonschema:"the page to ask for next; absent on the last page"`
}

func orderSummary(o orders.Order) OrderSummary {
	s := OrderSummary{
		ID: o.ID, Reference: o.Reference(), Status: string(o.Status),
		Total: catalog.FormatPrice(o.TotalCents), Currency: o.Currency,
		Customer: o.Customer.Name, Email: o.Customer.Email,
		CreatedAt: o.CreatedAt, Fulfilled: o.FulfilledAt != nil, Oversold: o.Oversold,
	}
	if !o.PaidAt.IsZero() {
		paid := o.PaidAt
		s.PaidAt = &paid
	}
	return s
}

func (s *Server) searchOrders(ctx context.Context, _ auth.User, in orderSearch) (orderList, error) {
	if in.Page == 0 {
		in.Page = 1
	}
	if err := admin.CheckOrderSearch(in.Search, in.Filter, in.Page); err != nil {
		return orderList{}, refuse("filter must be one of %v, page at least 1, search at most 200 characters", admin.OrderFilters)
	}
	list, next, err := s.d.Orders.Search(ctx, in.Search, in.Filter, in.Page)
	if err != nil {
		return orderList{}, err
	}
	out := orderList{Orders: make([]OrderSummary, 0, len(list))}
	for _, o := range list {
		out.Orders = append(out.Orders, orderSummary(o))
	}
	if next {
		out.NextPage = in.Page + 1
	}
	return out, nil
}

type OrderItem struct {
	Title     string `json:"title"`
	Variant   string `json:"variant,omitempty"`
	Kind      string `json:"kind"`
	UnitPrice string `json:"unit_price"`
	Quantity  int    `json:"quantity"`
}

type Entitlement struct {
	ID        string     `json:"id"`
	Product   string     `json:"product"`
	Variant   string     `json:"variant,omitempty"`
	Downloads int64      `json:"downloads"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type Email struct {
	Kind      string     `json:"kind"`
	Attempts  int        `json:"attempts"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

type Order struct {
	OrderSummary
	Phone             string        `json:"phone,omitempty"`
	Address           string        `json:"address,omitempty"`
	Items             []OrderItem   `json:"items"`
	Gateway           string        `json:"gateway"`
	GatewayRef        string        `json:"gateway_ref,omitempty"`
	GatewayStatus     string        `json:"gateway_status,omitempty"`
	FulfilledAt       *time.Time    `json:"fulfilled_at,omitempty"`
	TrackingReference string        `json:"tracking_reference,omitempty"`
	InternalNote      string        `json:"internal_note,omitempty"`
	Entitlements      []Entitlement `json:"entitlements,omitempty"`
	Emails            []Email       `json:"emails,omitempty"`
	AdminURL          string        `json:"admin_url"`
}

func (s *Server) getOrder(ctx context.Context, _ auth.User, in byID) (Order, error) {
	d, err := admin.LoadOrderDetail(ctx, s.d.Orders, s.d.Grants, s.d.Outbox, in.ID)
	if err != nil {
		return Order{}, err
	}
	o := d.Order
	out := Order{
		OrderSummary: orderSummary(o),
		Phone:        o.Customer.Phone, Address: o.Customer.Address,
		Items:   make([]OrderItem, 0, len(o.Items)),
		Gateway: o.Gateway, GatewayRef: o.GatewayRef, GatewayStatus: o.GatewayStatus,
		FulfilledAt: o.FulfilledAt, TrackingReference: o.TrackingReference, InternalNote: o.InternalNote,
		AdminURL: s.d.BaseURL + "/admin/orders/" + o.ID,
	}
	for _, it := range o.Items {
		out.Items = append(out.Items, OrderItem{
			Title: it.Title, Variant: it.VariantLabel, Kind: it.Kind,
			UnitPrice: catalog.FormatPrice(it.UnitPriceCents), Quantity: it.Quantity,
		})
	}
	for _, e := range d.Entitlements {
		out.Entitlements = append(out.Entitlements, Entitlement{
			ID: e.ID, Product: e.ProductTitle, Variant: e.VariantLabel,
			Downloads: e.DownloadCount, RevokedAt: e.RevokedAt,
		})
	}
	for _, e := range d.Emails {
		out.Emails = append(out.Emails, Email{Kind: e.Kind, Attempts: e.Attempts, SentAt: e.SentAt, LastError: e.LastError})
	}
	return out, nil
}

// --- catalog writes -------------------------------------------------------

type productInput struct {
	Title       string   `json:"title"`
	Slug        string   `json:"slug,omitempty" jsonschema:"URL slug; derived from the title when blank"`
	Description string   `json:"description,omitempty"`
	Kind        string   `json:"kind,omitempty" jsonschema:"physical (default) or digital"`
	Active      *bool    `json:"active,omitempty" jsonschema:"shown in the storefront; defaults to true"`
	OptionNames []string `json:"option_names,omitempty" jsonschema:"up to three, e.g. [\"Size\", \"Colour\"]"`
	CategoryIDs []string `json:"category_ids,omitempty"`
}

func (s *Server) createProduct(ctx context.Context, _ auth.User, in productInput) (Product, error) {
	names, err := slots(in.OptionNames)
	if err != nil {
		return Product{}, err
	}
	p := catalog.Product{
		Title: in.Title, Slug: in.Slug, Description: in.Description,
		Kind: catalog.Kind(in.Kind), Active: in.Active == nil || *in.Active,
		Option1Name: names[0], Option2Name: names[1], Option3Name: names[2],
	}
	if p.Kind == "" {
		p.Kind = catalog.KindPhysical
	}
	return s.saveProduct(ctx, p, in.CategoryIDs, true)
}

type productUpdate struct {
	ID          string    `json:"id"`
	Title       *string   `json:"title,omitempty"`
	Slug        *string   `json:"slug,omitempty"`
	Description *string   `json:"description,omitempty"`
	Kind        *string   `json:"kind,omitempty"`
	Active      *bool     `json:"active,omitempty"`
	OptionNames *[]string `json:"option_names,omitempty"`
	CategoryIDs *[]string `json:"category_ids,omitempty" jsonschema:"replaces the product's categories; [] clears them"`
}

func (s *Server) updateProduct(ctx context.Context, _ auth.User, in productUpdate) (Product, error) {
	p, err := s.d.Catalog.Get(ctx, in.ID)
	if err != nil {
		return Product{}, err
	}
	set(&p.Title, in.Title)
	set(&p.Slug, in.Slug)
	set(&p.Description, in.Description)
	if in.Kind != nil {
		p.Kind = catalog.Kind(*in.Kind)
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	if in.OptionNames != nil {
		names, err := slots(*in.OptionNames)
		if err != nil {
			return Product{}, err
		}
		p.Option1Name, p.Option2Name, p.Option3Name = names[0], names[1], names[2]
	}
	// The store replaces every category link on update, so an update that did
	// not mention categories must send back the ones the product has.
	ids := make([]string, 0, len(p.Categories))
	for _, c := range p.Categories {
		ids = append(ids, c.ID)
	}
	if in.CategoryIDs != nil {
		ids = *in.CategoryIDs
	}
	return s.saveProduct(ctx, p, ids, false)
}

func (s *Server) saveProduct(ctx context.Context, p catalog.Product, categoryIDs []string, isNew bool) (Product, error) {
	known, err := s.d.Catalog.Categories(ctx)
	if err != nil {
		return Product{}, err
	}
	p, errs := admin.PrepareProduct(p, categoryIDs, known)
	if errs.Any() {
		return Product{}, fieldErrors(errs)
	}
	var saved catalog.Product
	if isNew {
		saved, err = s.d.Catalog.Create(ctx, p)
	} else {
		saved, err = s.d.Catalog.Update(ctx, p)
	}
	if err != nil {
		if errs, ok := admin.ProductWriteErrors(err); ok {
			return Product{}, fieldErrors(errs)
		}
		return Product{}, err
	}
	full, err := s.loadProduct(ctx, saved.ID)
	if err != nil {
		return Product{}, err
	}
	return s.productOut(full), nil
}

type deleted struct {
	Deleted bool   `json:"deleted"`
	Notice  string `json:"notice,omitempty"`
}

func (s *Server) deleteProduct(ctx context.Context, _ auth.User, in byID) (deleted, error) {
	err := s.d.Catalog.Delete(ctx, in.ID)
	if errors.Is(err, catalog.ErrInUse) {
		return deleted{}, refuse(admin.ProductInUse)
	}
	if err != nil {
		return deleted{}, err
	}
	return deleted{Deleted: true}, nil
}

type variantInput struct {
	ProductID string   `json:"product_id"`
	SKU       string   `json:"sku"`
	Options   []string `json:"options,omitempty" jsonschema:"option values, in the order of the product's option_names"`
	Price     string   `json:"price" jsonschema:"decimal amount, e.g. 149.99"`
	StockQty  int      `json:"stock_qty,omitempty"`
	Active    *bool    `json:"active,omitempty" jsonschema:"defaults to true"`
}

func (s *Server) createVariant(ctx context.Context, _ auth.User, in variantInput) (Variant, error) {
	opts, err := slots(in.Options)
	if err != nil {
		return Variant{}, err
	}
	cents, err := catalog.ParsePrice(in.Price)
	if err != nil {
		return Variant{}, refuse("price: enter an amount like 149.99")
	}
	v := catalog.Variant{
		ProductID: in.ProductID, SKU: in.SKU,
		Option1: opts[0], Option2: opts[1], Option3: opts[2],
		PriceCents: cents, StockQty: in.StockQty, Active: in.Active == nil || *in.Active,
	}
	return s.saveVariant(ctx, v, true)
}

type variantUpdate struct {
	ProductID string    `json:"product_id"`
	ID        string    `json:"id"`
	SKU       *string   `json:"sku,omitempty"`
	Options   *[]string `json:"options,omitempty"`
	Price     *string   `json:"price,omitempty" jsonschema:"decimal amount, e.g. 149.99"`
	StockQty  *int      `json:"stock_qty,omitempty"`
	Active    *bool     `json:"active,omitempty"`
}

func (s *Server) updateVariant(ctx context.Context, _ auth.User, in variantUpdate) (Variant, error) {
	variants, err := s.d.Catalog.Variants(ctx, in.ProductID)
	if err != nil {
		return Variant{}, err
	}
	var v catalog.Variant
	found := false
	for _, candidate := range variants {
		if candidate.ID == in.ID {
			v, found = candidate, true
		}
	}
	if !found {
		return Variant{}, catalog.ErrNotFound
	}
	set(&v.SKU, in.SKU)
	if in.Options != nil {
		opts, err := slots(*in.Options)
		if err != nil {
			return Variant{}, err
		}
		v.Option1, v.Option2, v.Option3 = opts[0], opts[1], opts[2]
	}
	if in.Price != nil {
		if v.PriceCents, err = catalog.ParsePrice(*in.Price); err != nil {
			return Variant{}, refuse("price: enter an amount like 149.99")
		}
	}
	if in.StockQty != nil {
		v.StockQty = *in.StockQty
	}
	if in.Active != nil {
		v.Active = *in.Active
	}
	return s.saveVariant(ctx, v, false)
}

func (s *Server) saveVariant(ctx context.Context, v catalog.Variant, isNew bool) (Variant, error) {
	v, errs := admin.PrepareVariant(v)
	if errs.Any() {
		return Variant{}, fieldErrors(errs)
	}
	var saved catalog.Variant
	var err error
	if isNew {
		saved, err = s.d.Catalog.CreateVariant(ctx, v)
	} else {
		saved, err = s.d.Catalog.UpdateVariant(ctx, v)
	}
	if err != nil {
		if errs, ok := admin.VariantWriteErrors(err); ok {
			return Variant{}, fieldErrors(errs)
		}
		return Variant{}, err
	}
	return variantOut(saved), nil
}

type variantRef struct {
	ProductID string `json:"product_id"`
	ID        string `json:"id"`
}

func (s *Server) deleteVariant(ctx context.Context, _ auth.User, in variantRef) (deleted, error) {
	err := s.d.Catalog.DeleteVariant(ctx, in.ProductID, in.ID)
	if errors.Is(err, catalog.ErrInUse) {
		return deleted{}, refuse(admin.VariantInUse)
	}
	if err != nil {
		return deleted{}, err
	}
	return deleted{Deleted: true}, nil
}

type categoryInput struct {
	Name     string `json:"name"`
	Slug     string `json:"slug,omitempty" jsonschema:"derived from the name when blank"`
	Position int    `json:"position,omitempty" jsonschema:"lower is shown first"`
}

func (s *Server) createCategory(ctx context.Context, _ auth.User, in categoryInput) (Category, error) {
	return s.saveCategory(ctx, catalog.Category{Name: in.Name, Slug: in.Slug, Position: in.Position}, true)
}

type categoryUpdate struct {
	ID       string  `json:"id"`
	Name     *string `json:"name,omitempty"`
	Slug     *string `json:"slug,omitempty"`
	Position *int    `json:"position,omitempty"`
}

func (s *Server) updateCategory(ctx context.Context, _ auth.User, in categoryUpdate) (Category, error) {
	c, err := s.d.Catalog.Category(ctx, in.ID)
	if err != nil {
		return Category{}, err
	}
	set(&c.Name, in.Name)
	set(&c.Slug, in.Slug)
	if in.Position != nil {
		c.Position = *in.Position
	}
	return s.saveCategory(ctx, c, false)
}

func (s *Server) saveCategory(ctx context.Context, c catalog.Category, isNew bool) (Category, error) {
	c, errs := admin.PrepareCategory(c)
	if errs.Any() {
		return Category{}, fieldErrors(errs)
	}
	var saved catalog.Category
	var err error
	if isNew {
		saved, err = s.d.Catalog.CreateCategory(ctx, c)
	} else {
		saved, err = s.d.Catalog.UpdateCategory(ctx, c)
	}
	if err != nil {
		if errs, ok := admin.CategoryWriteErrors(err); ok {
			return Category{}, fieldErrors(errs)
		}
		return Category{}, err
	}
	return categoryOut(saved), nil
}

func (s *Server) deleteCategory(ctx context.Context, _ auth.User, in byID) (deleted, error) {
	unlinked, err := s.d.Catalog.DeleteCategory(ctx, in.ID)
	if err != nil {
		return deleted{}, err
	}
	return deleted{Deleted: true, Notice: admin.CategoryDeleted(unlinked)}, nil
}

// --- order writes ---------------------------------------------------------

type fulfillment struct {
	OrderID   string `json:"order_id"`
	Fulfilled bool   `json:"fulfilled" jsonschema:"true when shipped; false undoes it"`
	Tracking  string `json:"tracking,omitempty" jsonschema:"courier tracking reference, up to 200 characters"`
	Note      string `json:"note,omitempty" jsonschema:"internal note, never shown to the customer, up to 4000 characters"`
}

func (s *Server) setFulfillment(ctx context.Context, caller auth.User, in fulfillment) (Order, error) {
	if err := admin.CheckFulfillment(in.Tracking, in.Note); err != nil {
		return Order{}, refuse("tracking is at most 200 characters and note at most 4000")
	}
	err := s.d.Orders.SetFulfillment(ctx, in.OrderID, caller.ID, in.Fulfilled, in.Tracking, in.Note)
	if errors.Is(err, orders.ErrNotFulfillable) {
		return Order{}, refuse("only paid orders containing physical goods can be fulfilled")
	}
	if err != nil {
		return Order{}, err
	}
	return s.getOrder(ctx, caller, byID{ID: in.OrderID})
}

type queued struct {
	Queued bool `json:"queued"`
}

func (s *Server) retryOrderEmail(ctx context.Context, _ auth.User, in byID) (queued, error) {
	if _, err := s.d.Orders.Get(ctx, in.ID); err != nil {
		return queued{}, err
	}
	if s.d.Outbox == nil {
		return queued{}, refuse("this store has no email queue")
	}
	if err := s.d.Outbox.Retry(ctx, in.ID); err != nil {
		return queued{}, err
	}
	return queued{Queued: true}, nil
}

type entitlementRef struct {
	OrderID       string `json:"order_id"`
	EntitlementID string `json:"entitlement_id"`
}

type entitlementState struct {
	Revoked bool `json:"revoked"`
}

func (s *Server) revokeEntitlement(ctx context.Context, _ auth.User, in entitlementRef) (entitlementState, error) {
	if err := s.d.Grants.Revoke(ctx, in.OrderID, in.EntitlementID); err != nil {
		return entitlementState{}, err
	}
	return entitlementState{Revoked: true}, nil
}

func (s *Server) restoreEntitlement(ctx context.Context, _ auth.User, in entitlementRef) (entitlementState, error) {
	if err := s.d.Grants.Restore(ctx, in.OrderID, in.EntitlementID); err != nil {
		return entitlementState{}, err
	}
	return entitlementState{Revoked: false}, nil
}

func set(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}
