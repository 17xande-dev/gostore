package catalog_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
)

func TestStore_CreateGetAndUpdate(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	created, err := s.Create(ctx, catalog.Product{
		Slug: "a-book", Title: "A Book", Description: "d", Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned an empty id")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create returned a zero created_at")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "A Book" || got.Slug != "a-book" || !got.Active {
		t.Errorf("Get returned %+v", got)
	}
	if len(got.Variants) != 0 {
		t.Errorf("new product has %d variants, want 0", len(got.Variants))
	}

	got.Title = "A Better Book"
	got.Active = false
	updated, err := s.Update(ctx, got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "A Better Book" || updated.Active {
		t.Errorf("Update returned %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Error("Update did not move updated_at forward")
	}

	bySlug, err := s.GetBySlug(ctx, "a-book")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.ID != created.ID {
		t.Errorf("GetBySlug returned %s, want %s", bySlug.ID, created.ID)
	}
}

func TestStore_NotFound(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	// A well-formed id that does not exist, and a path parameter that is not a
	// UUID at all, must both be 404s rather than driver errors.
	for _, id := range []string{"3f2504e0-4f89-41d3-9a0c-0305e82c3301", "not-a-uuid", ""} {
		if _, err := s.Get(ctx, id); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
		}
		if err := s.Delete(ctx, id); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("Delete(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if _, err := s.GetBySlug(ctx, "nope"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("GetBySlug = %v, want ErrNotFound", err)
	}
}

func TestStore_DuplicateSlugIsAConflict(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := catalog.Product{Slug: "taken", Title: "First", Active: true}
	if _, err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p.Title = "Second"
	_, err := s.Create(ctx, p)
	var conflict *catalog.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Create with a duplicate slug = %v, want a *ConflictError", err)
	}
	if conflict.Field != "slug" {
		t.Errorf("conflict field = %q, want slug", conflict.Field)
	}
}

func TestStore_OptionNamesRoundTrip(t *testing.T) {
	// The option names are on the product and the values are on the variants, so
	// three separate writes have to carry them: Create, Update and the seed's
	// Upsert. A struct literal that forgets one is silent — the column simply
	// stays empty — which is exactly the failure this pins.
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{
		Slug: "tee", Title: "Tee", Active: true,
		Option1Name: "Size", Option2Name: "Colour", Option3Name: "Material",
	})
	if p.Option1Name != "Size" || p.Option2Name != "Colour" || p.Option3Name != "Material" {
		t.Fatalf("Create did not return the option names: %+v", p)
	}

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Option1Name != "Size" || got.Option3Name != "Material" {
		t.Errorf("option names did not survive the round trip: %+v", got)
	}

	// Renaming a heading and clearing an unused slot are both ordinary edits.
	got.Option1Name, got.Option3Name = "Chest", ""
	if _, err := s.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if reread, err := s.Get(ctx, p.ID); err != nil {
		t.Fatalf("Get after update: %v", err)
	} else if reread.Option1Name != "Chest" || reread.Option3Name != "" {
		t.Errorf("Update did not write the option names: %+v", reread)
	}

	// Upsert is the seed's path, and it must update the names on a re-run rather
	// than leave a fixture's earlier spelling in place.
	up, err := s.Upsert(ctx, catalog.Product{
		Slug: "tee", Title: "Tee", Active: true, Option1Name: "Format",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if up.Option1Name != "Format" || up.Option2Name != "" {
		t.Errorf("Upsert did not write the option names: %+v", up)
	}
}

func TestStore_VariantOptionsRoundTrip(t *testing.T) {
	// The third slot in particular: two would be easy to carry through by accident
	// while the third stays empty everywhere.
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	v := mustCreateVariant(t, s, catalog.Variant{
		ProductID: p.ID, SKU: "TEE-1",
		Option1: "L", Option2: "Navy", Option3: "Cotton",
		PriceCents: 29900, StockQty: 1, Active: true,
	})
	if v.Option3 != "Cotton" {
		t.Fatalf("CreateVariant dropped an option: %+v", v)
	}

	v.Option2, v.Option3 = "Black", "Linen"
	if _, err := s.UpdateVariant(ctx, v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}
	got, err := s.Variants(ctx, p.ID)
	if err != nil {
		t.Fatalf("Variants: %v", err)
	}
	if len(got) != 1 || got[0].Option2 != "Black" || got[0].Option3 != "Linen" {
		t.Errorf("UpdateVariant did not write the options: %+v", got)
	}
	if want := "L / Black / Linen"; got[0].Label() != want {
		t.Errorf("Label() = %q, want %q", got[0].Label(), want)
	}
}

func TestStore_Variants(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Slug: "tee", Title: "Tee", Active: true})

	v, err := s.CreateVariant(ctx, catalog.Variant{
		ProductID: p.ID, SKU: "TEE-M-BLK", Option1: "M", Option2: "Black", PriceCents: 29900, StockQty: 7, Active: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	if v.ID == "" || v.ProductID != p.ID {
		t.Fatalf("CreateVariant returned %+v", v)
	}

	// A duplicate SKU and a duplicate set of option values are different mistakes
	// and must be reported against different fields.
	_, err = s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "TEE-M-BLK", Option1: "L", PriceCents: 1, Active: true})
	var conflict *catalog.ConflictError
	if !errors.As(err, &conflict) || conflict.Field != "sku" {
		t.Errorf("duplicate SKU = %v, want a sku conflict", err)
	}
	_, err = s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "OTHER", Option1: "M", Option2: "Black", PriceCents: 1, Active: true})
	if !errors.As(err, &conflict) || conflict.Field != "options" {
		t.Errorf("duplicate options = %v, want an options conflict", err)
	}

	v.StockQty = 2
	v.PriceCents = 31900
	if _, err := s.UpdateVariant(ctx, v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Variants) != 1 {
		t.Fatalf("product has %d variants, want 1", len(got.Variants))
	}
	if got.Variants[0].StockQty != 2 || got.Variants[0].PriceCents != 31900 {
		t.Errorf("variant is %+v after update", got.Variants[0])
	}
	if got.TotalStock() != 2 {
		t.Errorf("TotalStock = %d, want 2", got.TotalStock())
	}

	// A variant id belonging to another product must not be editable through
	// this product's URL.
	other := mustCreate(t, s, catalog.Product{Slug: "other", Title: "Other", Active: true})
	stray := v
	stray.ProductID = other.ID
	if _, err := s.UpdateVariant(ctx, stray); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("UpdateVariant across products = %v, want ErrNotFound", err)
	}
	if err := s.DeleteVariant(ctx, other.ID, v.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("DeleteVariant across products = %v, want ErrNotFound", err)
	}

	if err := s.DeleteVariant(ctx, p.ID, v.ID); err != nil {
		t.Fatalf("DeleteVariant: %v", err)
	}
	if vs, err := s.Variants(ctx, p.ID); err != nil || len(vs) != 0 {
		t.Errorf("Variants after delete = %v, %v", vs, err)
	}
}

func TestStore_DeleteCascadesToVariants(t *testing.T) {
	pool := dbtest.Pool(t)
	s := catalog.NewStore(pool)
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Slug: "doomed", Title: "Doomed", Active: true})
	if _, err := s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "D-1", PriceCents: 100, Active: true}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	if err := s.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var variants int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM product_variants").Scan(&variants); err != nil {
		t.Fatalf("count variants: %v", err)
	}
	if variants != 0 {
		t.Errorf("%d variants survived the product, want 0", variants)
	}
}

func TestStore_List(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	if products, err := s.List(ctx); err != nil || len(products) != 0 {
		t.Fatalf("List on an empty catalog = %v, %v", products, err)
	}

	b := mustCreate(t, s, catalog.Product{Slug: "b", Title: "Beta", Active: true})
	a := mustCreate(t, s, catalog.Product{Slug: "a", Title: "Alpha", Active: true})
	if _, err := s.CreateVariant(ctx, catalog.Variant{ProductID: b.ID, SKU: "B-1", PriceCents: 100, StockQty: 3, Active: true}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	products, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("List returned %d products, want 2", len(products))
	}
	if products[0].ID != a.ID || products[1].ID != b.ID {
		t.Error("List is not ordered by title")
	}
	// Variants must be attached to the right product, not to whichever came
	// back first.
	if len(products[0].Variants) != 0 {
		t.Errorf("Alpha has %d variants, want 0", len(products[0].Variants))
	}
	if len(products[1].Variants) != 1 || products[1].Variants[0].SKU != "B-1" {
		t.Errorf("Beta has variants %+v, want just B-1", products[1].Variants)
	}
}

func TestStore_UpsertIsRerunnable(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := catalog.Product{
		Slug: "tote", Title: "Tote", Active: true,
		Variants: []catalog.Variant{
			{SKU: "TOTE-STD", Option1: "Natural", PriceCents: 14900, StockQty: 20, Active: true},
		},
	}

	first, err := s.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Someone sells eleven of them.
	sold := first.Variants[0]
	sold.StockQty = 9
	if _, err := s.UpdateVariant(ctx, sold); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	p.Title = "Canvas Tote"
	second, err := s.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("Upsert created a second product (%s then %s)", first.ID, second.ID)
	}
	if second.Title != "Canvas Tote" {
		t.Errorf("Upsert did not update the title, got %q", second.Title)
	}

	products, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 1 || len(products[0].Variants) != 1 {
		t.Fatalf("catalog is %+v, want one product with one variant", products)
	}
	// Reseeding must not reset stock to the fixture's number: the fixture is a
	// starting point, not a source of truth about inventory.
	if got := products[0].Variants[0].StockQty; got != 9 {
		t.Errorf("stock is %d after reseeding, want 9", got)
	}
}

func TestStore_SearchActiveVisibilityRules(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	// Visible: active product, one active variant that happens to be sold out
	// plus one in stock.
	visible := mustCreate(t, s, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	mustCreateVariant(t, s, catalog.Variant{ProductID: visible.ID, SKU: "TEE-S", Option1: "S", PriceCents: 29900, StockQty: 0, Active: true})
	mustCreateVariant(t, s, catalog.Variant{ProductID: visible.ID, SKU: "TEE-M", Option1: "M", PriceCents: 31900, StockQty: 2, Active: true})
	// Same product, an inactive variant: withdrawn options must not show up.
	mustCreateVariant(t, s, catalog.Variant{ProductID: visible.ID, SKU: "TEE-L", Option1: "L", PriceCents: 99900, StockQty: 5, Active: false})

	// Hidden: the product itself is inactive.
	inactive := mustCreate(t, s, catalog.Product{Slug: "draft", Title: "Draft", Active: false})
	mustCreateVariant(t, s, catalog.Variant{ProductID: inactive.ID, SKU: "DRAFT-1", PriceCents: 100, StockQty: 1, Active: true})

	// Hidden: active product whose only variant is inactive — nothing to buy, so
	// it is not a listing.
	empty := mustCreate(t, s, catalog.Product{Slug: "unbuyable", Title: "Unbuyable", Active: true})
	mustCreateVariant(t, s, catalog.Variant{ProductID: empty.ID, SKU: "UNB-1", PriceCents: 100, StockQty: 1, Active: false})

	// Hidden: active product with no variants at all.
	mustCreate(t, s, catalog.Product{Slug: "bare", Title: "Bare", Active: true})

	res, err := s.SearchActive(ctx, catalog.Search{Page: 1, PageSize: 24})
	if err != nil {
		t.Fatalf("SearchActive: %v", err)
	}
	if len(res.Products) != 1 || res.Total != 1 {
		t.Fatalf("SearchActive returned %d of %d products, want only the buyable one: %+v",
			len(res.Products), res.Total, res.Products)
	}

	got := res.Products[0]
	if got.Slug != "tee" {
		t.Errorf("SearchActive returned %q", got.Slug)
	}
	if len(got.Variants) != 2 {
		t.Fatalf("product has %d variants, want the 2 active ones: %+v", len(got.Variants), got.Variants)
	}
	// A sold-out variant is still listed: silently dropping a size reads as a
	// bug to whoever is looking for it.
	if !got.InStock() {
		t.Error("InStock() is false although one variant has stock")
	}
	if got.MinPriceCents() != 29900 || got.MaxPriceCents() != 31900 {
		t.Errorf("price range is %d–%d, want 29900–31900", got.MinPriceCents(), got.MaxPriceCents())
	}
	if got.OnePrice() {
		t.Error("OnePrice() is true for a product with two different prices")
	}
}

func TestStore_GetActiveBySlug(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Slug: "a-book", Title: "A Book", Active: true})
	// The size distinguishes them because (product_id, size, color) is unique —
	// a product gets at most one optionless variant, which is the invariant that
	// keeps "has options" from being a special case anywhere else.
	mustCreateVariant(t, s, catalog.Variant{ProductID: p.ID, SKU: "AB-1", Option1: "Paperback", PriceCents: 24900, StockQty: 3, Active: true})
	mustCreateVariant(t, s, catalog.Variant{ProductID: p.ID, SKU: "AB-2", Option1: "Hardcover", PriceCents: 34900, StockQty: 1, Active: false})

	got, err := s.GetActiveBySlug(ctx, "a-book")
	if err != nil {
		t.Fatalf("GetActiveBySlug: %v", err)
	}
	if len(got.Variants) != 1 || got.Variants[0].SKU != "AB-1" {
		t.Errorf("variants = %+v, want only the active one", got.Variants)
	}

	// Withdrawn and never-existed look the same from outside.
	if _, err := s.GetActiveBySlug(ctx, "no-such-slug"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown slug = %v, want ErrNotFound", err)
	}

	hidden := mustCreate(t, s, catalog.Product{Slug: "hidden", Title: "Hidden", Active: false})
	mustCreateVariant(t, s, catalog.Variant{ProductID: hidden.ID, SKU: "H-1", PriceCents: 100, StockQty: 1, Active: true})
	if _, err := s.GetActiveBySlug(ctx, "hidden"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("inactive product = %v, want ErrNotFound", err)
	}

	unbuyable := mustCreate(t, s, catalog.Product{Slug: "unbuyable", Title: "Unbuyable", Active: true})
	mustCreateVariant(t, s, catalog.Variant{ProductID: unbuyable.ID, SKU: "U-1", PriceCents: 100, StockQty: 1, Active: false})
	if _, err := s.GetActiveBySlug(ctx, "unbuyable"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("product with no active variants = %v, want ErrNotFound", err)
	}
}

func mustCreate(t *testing.T, s *catalog.Store, p catalog.Product) catalog.Product {
	t.Helper()
	out, err := s.Create(t.Context(), p)
	if err != nil {
		t.Fatalf("Create %q: %v", p.Slug, err)
	}
	return out
}

func mustCreateVariant(t *testing.T, s *catalog.Store, v catalog.Variant) catalog.Variant {
	t.Helper()
	out, err := s.CreateVariant(t.Context(), v)
	if err != nil {
		t.Fatalf("CreateVariant %q: %v", v.SKU, err)
	}
	return out
}

func TestStore_FilesAndVariantLinks(t *testing.T) {
	// The conference-recording case the whole feature was designed around: two
	// variants with disjoint file sets, plus one file that both include. The shared
	// file is what makes a bundle possible without uploading the same two gigabytes
	// twice, and a fixture where every file belonged to one variant would not
	// exercise the join at all.
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{
		Slug: "conference", Title: "Conference", Active: true,
		Kind: catalog.KindDigital, Option1Name: "Format",
	})
	audio := mustCreateVariant(t, s, catalog.Variant{
		ProductID: p.ID, SKU: "C-A", Option1: "Audio", PriceCents: 15000, Active: true,
	})
	video := mustCreateVariant(t, s, catalog.Variant{
		ProductID: p.ID, SKU: "C-V", Option1: "Video", PriceCents: 40000, Active: true,
	})

	add := func(title string, size int64, variants ...string) catalog.File {
		f, err := s.AddFile(ctx, catalog.File{
			ProductID: p.ID, Title: title, ObjectKey: "downloads/" + p.ID + "/" + title,
			OriginalFilename: title, ContentType: "application/octet-stream", SizeBytes: size,
		}, variants)
		if err != nil {
			t.Fatalf("AddFile %s: %v", title, err)
		}
		return f
	}
	mp3 := add("a.mp3", 1000, audio.ID)
	add("a.mp4", 2000, video.ID)
	notes := add("notes.pdf", 300, audio.ID, video.ID)

	// Position is assigned in upload order, so the admin's list has a stable order
	// before anybody has reordered anything.
	files, err := s.Files(ctx, p.ID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("%d files, want 3", len(files))
	}
	for i, f := range files {
		if f.Position != i {
			t.Errorf("%s is at position %d, want %d", f.Title, f.Position, i)
		}
	}
	if !files[0].InVariant(audio.ID) || files[0].InVariant(video.ID) {
		t.Errorf("a.mp3 links to %v", files[0].VariantIDs)
	}

	// What each buyer actually gets.
	audioFiles, err := s.VariantFiles(ctx, audio.ID)
	if err != nil {
		t.Fatalf("VariantFiles: %v", err)
	}
	if got := titles(audioFiles); !slices.Equal(got, []string{"a.mp3", "notes.pdf"}) {
		t.Errorf("the audio variant grants %v", got)
	}
	videoFiles, err := s.VariantFiles(ctx, video.ID)
	if err != nil {
		t.Fatalf("VariantFiles: %v", err)
	}
	if got := titles(videoFiles); !slices.Equal(got, []string{"a.mp4", "notes.pdf"}) {
		t.Errorf("the video variant grants %v", got)
	}

	// Retitling and re-ticking. The form submits the whole set every time, which is
	// what makes unticking one mean something.
	if _, err := s.UpdateFile(ctx, catalog.File{
		ID: notes.ID, ProductID: p.ID, Title: "Programme", Position: 0,
	}, []string{video.ID}); err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}
	audioFiles, _ = s.VariantFiles(ctx, audio.ID)
	if got := titles(audioFiles); !slices.Equal(got, []string{"a.mp3"}) {
		t.Errorf("after unticking, the audio variant grants %v", got)
	}

	// Deleting returns the key so the caller can remove the bytes afterwards.
	key, err := s.DeleteFile(ctx, p.ID, mp3.ID)
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if key != mp3.ObjectKey {
		t.Errorf("DeleteFile returned %q, want the stored key", key)
	}
	if _, err := s.File(ctx, p.ID, mp3.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("the file is still readable: %v", err)
	}
}

func TestStore_FileCannotBeLinkedToAnotherProductsVariant(t *testing.T) {
	// A variant id borrowed from another product's page must link nothing, rather
	// than granting that product's buyers somebody else's files.
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	mine := mustCreate(t, s, catalog.Product{Slug: "mine", Title: "Mine", Active: true, Kind: catalog.KindDigital})
	theirs := mustCreate(t, s, catalog.Product{Slug: "theirs", Title: "Theirs", Active: true, Kind: catalog.KindDigital})
	stranger := mustCreateVariant(t, s, catalog.Variant{
		ProductID: theirs.ID, SKU: "T-1", PriceCents: 100, Active: true,
	})

	f, err := s.AddFile(ctx, catalog.File{
		ProductID: mine.ID, Title: "secret.mp3", ObjectKey: "k",
		OriginalFilename: "secret.mp3", ContentType: "audio/mpeg", SizeBytes: 1,
	}, []string{stranger.ID})
	if err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if got, err := s.VariantFiles(ctx, stranger.ID); err != nil || len(got) != 0 {
		t.Errorf("the other product's variant grants %v (%v)", titles(got), err)
	}
	_ = f
}

func TestStore_KindIsFrozenOnceOrderedAndWhileFilesRemain(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{
		Slug: "recording", Title: "Recording", Active: true, Kind: catalog.KindDigital,
	})
	v := mustCreateVariant(t, s, catalog.Variant{
		ProductID: p.ID, SKU: "R-1", PriceCents: 100, Active: true,
	})
	if _, err := s.AddFile(ctx, catalog.File{
		ProductID: p.ID, Title: "a.mp3", ObjectKey: "k",
		OriginalFilename: "a.mp3", ContentType: "audio/mpeg", SizeBytes: 1,
	}, []string{v.ID}); err != nil {
		t.Fatalf("AddFile: %v", err)
	}

	// digital -> physical while files remain: refused, and the error says how many.
	p.Kind = catalog.KindPhysical
	_, err := s.Update(ctx, p)
	var locked *catalog.KindLockedError
	if !errors.As(err, &locked) || locked.Ordered || locked.Files != 1 {
		t.Fatalf("Update with a file attached = %v, want a KindLockedError naming 1 file", err)
	}

	// Everything else about the product still saves — the refusal is about the kind
	// alone, not a blanket lock on the row.
	p.Kind = catalog.KindDigital
	p.Title = "Recording, renamed"
	if _, err := s.Update(ctx, p); err != nil {
		t.Fatalf("Update without changing the kind: %v", err)
	}

	// Remove the file and it becomes changeable, so the guard is a step to take
	// rather than a dead end.
	files, _ := s.Files(ctx, p.ID)
	if _, err := s.DeleteFile(ctx, p.ID, files[0].ID); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	p.Kind = catalog.KindPhysical
	if _, err := s.Update(ctx, p); err != nil {
		t.Fatalf("Update after removing the file: %v", err)
	}
}

func TestStore_KindDefaultsToPhysical(t *testing.T) {
	// A zero-valued Product — from a seed file that says nothing, or a test literal
	// — must be a parcel rather than a CHECK constraint violation.
	s := catalog.NewStore(dbtest.Pool(t))
	p := mustCreate(t, s, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if p.Kind != catalog.KindPhysical {
		t.Errorf("Kind = %q, want physical", p.Kind)
	}
	if p.Digital() {
		t.Error("a product with no kind reads as digital")
	}

	got, err := s.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != catalog.KindPhysical {
		t.Errorf("the stored kind is %q", got.Kind)
	}
}

func titles(files []catalog.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Title)
	}
	return out
}
