// Package admin holds the rules for changing the store that sit above the stores
// and below any one way of asking — the HTML admin and the MCP endpoint both call
// it, so a rule written here cannot be skipped by using the other door.
//
// The stores persist and translate database errors; they do not derive slugs,
// resolve submitted category ids, run validation or phrase a conflict for a
// person. Those used to live in the HTML handlers, which was fine while the
// handlers were the only caller.
//
// Nothing here knows about HTTP. Parsing a form, or a tool's JSON arguments, into
// the domain values these functions take stays with the caller, because the two
// inputs are shaped differently (a price arrives as the text a person typed in
// one and as a JSON string in the other) and each wants to report a bad one in
// its own terms.
package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/downloads"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/outbox"
	"github.com/17xande-dev/gostore/internal/validate"
)

// PrepareProduct normalises a product and validates it, resolving categoryIDs
// against known, the taxonomy as it stands.
//
// A blank slug is derived from the title: a slug is a detail of the URL, not a
// decision to make on every product. An id that names no category is a field
// error rather than a foreign-key violation with nothing to point at.
func PrepareProduct(p catalog.Product, categoryIDs []string, known []catalog.Category) (catalog.Product, validate.FormErrors) {
	p.Slug = strings.TrimSpace(p.Slug)
	p.Title = strings.TrimSpace(p.Title)
	p.Description = strings.TrimSpace(p.Description)
	p.Kind = catalog.Kind(strings.TrimSpace(string(p.Kind)))
	p.Option1Name = strings.TrimSpace(p.Option1Name)
	p.Option2Name = strings.TrimSpace(p.Option2Name)
	p.Option3Name = strings.TrimSpace(p.Option3Name)
	if p.Slug == "" {
		p.Slug = catalog.Slugify(p.Title)
	}

	chosen, errs := validate.ProductCategories(categoryIDs, known)
	p.Categories = chosen
	for field, msg := range validate.Product(p) {
		errs.Add(field, msg)
	}
	return p, errs
}

// PrepareVariant normalises a variant and validates it. The price is already in
// cents: turning "149.99" into 14999 is the caller's parse, with catalog.ParsePrice.
func PrepareVariant(v catalog.Variant) (catalog.Variant, validate.FormErrors) {
	v.SKU = strings.TrimSpace(v.SKU)
	v.Option1 = strings.TrimSpace(v.Option1)
	v.Option2 = strings.TrimSpace(v.Option2)
	v.Option3 = strings.TrimSpace(v.Option3)
	return v, validate.Variant(v)
}

// PrepareCategory normalises a category and validates it. A blank slug is derived
// from the name, on the same grounds as a product's.
func PrepareCategory(c catalog.Category) (catalog.Category, validate.FormErrors) {
	c.Slug = strings.TrimSpace(c.Slug)
	c.Name = strings.TrimSpace(c.Name)
	if c.Slug == "" {
		c.Slug = catalog.Slugify(c.Name)
	}
	return c, validate.Category(c)
}

// ProductWriteErrors turns a refusal from creating or updating a product into
// field errors, and reports whether it was one. Anything else is a genuine
// failure and is the caller's to answer.
func ProductWriteErrors(err error) (validate.FormErrors, bool) {
	errs := validate.FormErrors{}
	if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
		errs.Add(conflict.Field, "Already used by another product.")
		return errs, true
	}
	if locked, ok := errors.AsType[*catalog.KindLockedError](err); ok {
		if locked.Ordered {
			errs.Add("kind", "This product has been ordered, so its kind is fixed. "+
				"Deactivate it and create a new one instead.")
		} else {
			errs.Add("kind", fmt.Sprintf("Remove the %d attached file(s) first. Switching to a "+
				"physical product would leave them in storage with nothing listing them.", locked.Files))
		}
		return errs, true
	}
	return nil, false
}

// ProductInUse is why an ordered product cannot be deleted.
const ProductInUse = "This product has been ordered and cannot be deleted. Deactivate it instead."

// VariantInUse is why an ordered variant cannot be deleted.
const VariantInUse = "This variant has been ordered and cannot be deleted. Deactivate it instead."

// VariantWriteErrors is ProductWriteErrors for a variant.
func VariantWriteErrors(err error) (validate.FormErrors, bool) {
	conflict, ok := errors.AsType[*catalog.ConflictError](err)
	if !ok {
		return nil, false
	}
	errs := validate.FormErrors{}
	if conflict.Field == "options" {
		errs.Add(conflict.Field, "Another variant of this product already has those options.")
	} else {
		errs.Add(conflict.Field, "Already used by another variant.")
	}
	return errs, true
}

// CategoryWriteErrors is ProductWriteErrors for a category.
func CategoryWriteErrors(err error) (validate.FormErrors, bool) {
	conflict, ok := errors.AsType[*catalog.ConflictError](err)
	if !ok {
		return nil, false
	}
	errs := validate.FormErrors{}
	errs.Add(conflict.Field, "Already used by another category.")
	return errs, true
}

// CategoryDeleted says what deleting a category did. It is never refused, and the
// products it was on are untouched, so without the count the result is
// indistinguishable from doing nothing.
func CategoryDeleted(unlinked int64) string {
	switch {
	case unlinked == 1:
		return "Category deleted, and removed from 1 product. The product itself is untouched."
	case unlinked > 1:
		return "Category deleted, and removed from " + strconv.FormatInt(unlinked, 10) +
			" products. The products themselves are untouched."
	}
	return "Category deleted. It was not used by any product."
}

// ErrBadInput is a request outside the bounds an order operation accepts.
var ErrBadInput = errors.New("admin: input out of bounds")

// OrderFilters are the order-list filters, beside "" for none.
var OrderFilters = []string{"oversold", "email", "unfulfilled"}

// CheckOrderSearch bounds an order search before it reaches the database: a known
// filter, a page in range, and search text short enough not to be a way of making
// the query expensive.
func CheckOrderSearch(search, filter string, page int) error {
	if len(search) > 200 || page < 1 || page > 1000000 {
		return ErrBadInput
	}
	if filter != "" && !slices.Contains(OrderFilters, filter) {
		return ErrBadInput
	}
	return nil
}

// CheckFulfillment bounds the free text recorded with a fulfilment.
func CheckFulfillment(tracking, note string) error {
	if len(tracking) > 200 || len(note) > 4000 {
		return ErrBadInput
	}
	return nil
}

// OrderDetail is everything the admin shows about one order.
type OrderDetail struct {
	Order orders.Order
	// Entitlements are the download grants the order created. Empty for an order
	// of physical goods only.
	Entitlements []downloads.OrderEntitlement
	Emails       []outbox.Status
	// PendingEmails is whether any of Emails is still unsent.
	PendingEmails bool
}

// LoadOrderDetail reads an order with its entitlements and email jobs. mail may be
// nil, for a deployment wired without an outbox.
func LoadOrderDetail(ctx context.Context, store *orders.Store, grants *downloads.Store, mail *outbox.Store, id string) (OrderDetail, error) {
	order, err := store.Get(ctx, id)
	if err != nil {
		return OrderDetail{}, err
	}
	// Read unconditionally rather than only for an order with digital lines: the
	// kind lives on order_items, so deciding here would mean scanning them for the
	// same answer this query already gives, and the query is a no-op for the
	// common case.
	ents, err := grants.ForOrder(ctx, order.ID)
	if err != nil {
		return OrderDetail{}, err
	}
	d := OrderDetail{Order: order, Entitlements: ents}
	if mail != nil {
		d.Emails, err = mail.ForOrder(ctx, order.ID)
		if err != nil {
			return OrderDetail{}, err
		}
		for _, email := range d.Emails {
			if email.SentAt == nil {
				d.PendingEmails = true
			}
		}
	}
	return d, nil
}

