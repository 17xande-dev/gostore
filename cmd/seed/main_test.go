package main

import (
	"path/filepath"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// The shipped fixture is the first thing a new adopter runs, so a typo in it
// must fail here rather than at their first `make seed`.
func TestReadProducts_ShippedFixtureIsValid(t *testing.T) {
	products, err := readProducts("../../testdata/products.json")
	if err != nil {
		t.Fatalf("readProducts: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("the fixture contains no products")
	}

	slugs := map[string]bool{}
	skus := map[string]bool{}
	for _, p := range products {
		if slugs[p.Slug] {
			t.Errorf("duplicate slug %q", p.Slug)
		}
		slugs[p.Slug] = true

		// A product with no variants cannot be bought, which would make the
		// demo catalog look broken.
		if len(p.Variants) == 0 {
			t.Errorf("%s has no variants", p.Slug)
		}
		for _, v := range p.Variants {
			if skus[v.SKU] {
				t.Errorf("duplicate SKU %q", v.SKU)
			}
			skus[v.SKU] = true
			if v.PriceCents <= 0 {
				t.Errorf("%s: variant %s has price %d", p.Slug, v.SKU, v.PriceCents)
			}
		}
	}
}

func TestReadProducts_RejectsAMissingFile(t *testing.T) {
	if _, err := readProducts("testdata/does-not-exist.json"); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

// The shipped fixture's digital product, checked as carefully as the rest of it:
// it is the demonstration of the whole feature, and a broken one is worse than
// none because it looks like the feature is broken.
func TestReadProducts_ShippedFixtureHasAWorkingDigitalProduct(t *testing.T) {
	products, err := readProducts("../../testdata/products.json")
	if err != nil {
		t.Fatalf("readProducts: %v", err)
	}

	var digital []seedProduct
	for _, p := range products {
		if p.Digital() {
			digital = append(digital, p)
		}
	}
	if len(digital) == 0 {
		t.Fatal("the fixture demonstrates no digital product")
	}

	for _, p := range digital {
		if len(p.Files) == 0 {
			t.Errorf("%s is digital but has no files, so there is nothing to download", p.Slug)
		}
		// The point of the fixture is the join: one file granted by more than one
		// variant, and one granted by fewer. A fixture where every file belonged to
		// every variant would demonstrate nothing that a file column on the variant
		// would not.
		counts := map[int]int{}
		reach := map[string]bool{}
		for _, f := range p.Files {
			counts[len(f.Variants)]++
			for _, sku := range f.Variants {
				reach[sku] = true
			}
		}
		if len(counts) < 2 {
			t.Errorf("%s: every file is granted by the same number of variants, so the fixture "+
				"does not show what variant_files is for", p.Slug)
		}
		// And no variant that grants nothing: buying it would take money for an
		// empty download page.
		for _, v := range p.Variants {
			if !reach[v.SKU] {
				t.Errorf("%s: variant %s grants no files, so buying it downloads nothing", p.Slug, v.SKU)
			}
		}
	}
}

func TestCheckFiles(t *testing.T) {
	dir := "../../testdata"

	// The shipped fixture passes with storage configured, and is refused without
	// it — a fixture that asks for something the configuration cannot deliver
	// should say so rather than seeding a digital product with nothing behind it.
	products, err := readProducts(dir + "/products.json")
	if err != nil {
		t.Fatalf("readProducts: %v", err)
	}
	if err := checkFiles(products, dir, true); err != nil {
		t.Errorf("the shipped fixture was refused: %v", err)
	}
	if err := checkFiles(products, dir, false); err == nil {
		t.Error("a fixture with files was accepted with no download storage configured")
	}

	digital := func(files ...seedFile) []seedProduct {
		return []seedProduct{{
			Product: catalog.Product{
				Slug: "rec", Title: "Rec", Kind: catalog.KindDigital,
				Variants: []catalog.Variant{{SKU: "REC-1"}},
			},
			Files: files,
		}}
	}
	ok := seedFile{Path: "downloads/sample-transcript.pdf", Variants: []string{"REC-1"}}

	if err := checkFiles(digital(ok), dir, true); err != nil {
		t.Errorf("a valid fixture was refused: %v", err)
	}

	bad := map[string][]seedProduct{
		"a file that is not there": digital(seedFile{Path: "downloads/nope.mp3", Variants: []string{"REC-1"}}),
		"a SKU that is not one of this product's variants": digital(
			seedFile{Path: "downloads/sample-transcript.pdf", Variants: []string{"OTHER"}}),
		"two files with the same name": digital(ok, seedFile{
			Path: "../testdata/downloads/sample-transcript.pdf", Variants: []string{"REC-1"}}),
		"no path at all": digital(seedFile{Variants: []string{"REC-1"}}),
	}
	for name, ps := range bad {
		if err := checkFiles(ps, dir, true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Files on a physical product. Nothing would ever read the rows, and nothing
	// would ever delete the objects.
	physical := digital(ok)
	physical[0].Kind = catalog.KindPhysical
	if err := checkFiles(physical, dir, true); err == nil {
		t.Error("files on a physical product were accepted")
	}
}

func TestResolve_RefusesPathsOutsideTheSeedFilesDirectory(t *testing.T) {
	// A seed file is data. Data that can name any path on the machine and have it
	// uploaded to a bucket is a way to exfiltrate a private key by editing JSON.
	for _, path := range []string{
		"/etc/passwd",
		"../../../../etc/passwd",
		"downloads/../../secrets.env",
		"",
	} {
		if got, err := resolve("testdata", path); err == nil {
			t.Errorf("resolve(%q) = %q, want an error", path, got)
		}
	}

	got, err := resolve("testdata", "downloads/a.mp3")
	if err != nil {
		t.Fatalf("resolve of an ordinary path: %v", err)
	}
	if want := filepath.Join("testdata", "downloads", "a.mp3"); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

func TestUnreachableFiles(t *testing.T) {
	// A file no variant grants is stored and downloadable by nobody. Legal, and
	// almost always a fixture mistake, so the seed says so.
	got := unreachableFiles([]seedFile{
		{Path: "downloads/a.mp3", Variants: []string{"SKU-1"}},
		{Path: "downloads/b.pdf"},
	})
	if len(got) != 1 || got[0] != "b.pdf" {
		t.Errorf("unreachableFiles = %v, want [b.pdf]", got)
	}
}
