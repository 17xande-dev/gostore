// Command seed loads a products JSON file into the database.
//
// It is rerunnable: products match on slug, variants on SKU and a digital
// product's files on the name they were seeded from, so seeding twice updates
// rather than duplicates. Stock counts on rows that already exist are left where
// the operator put them, and a file already in storage is retitled and re-linked
// rather than uploaded again — replacing it would mint a new file id, and a buyer
// holding an entitlement would find what they paid for had become something else.
//
//	DATABASE_URL=... go run ./cmd/seed -file testdata/products.json
//
// A fixture with files also needs somewhere private to put them: DOWNLOAD_DIR, or
// the DOWNLOAD_* variables for a bucket. Nothing else the server requires is
// needed here — an admin password hash to load a JSON file would be an obstacle
// with nothing behind it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/db"
	"github.com/17xande-dev/gostore/internal/validate"
)

func main() {
	if err := run(); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	file := flag.String("file", "testdata/products.json", "products JSON file to load")
	flag.Parse()

	cfg, err := config.LoadTool()
	if err != nil {
		return err
	}

	products, err := readProducts(*file)
	if err != nil {
		return err
	}

	files, err := openDownloads(cfg)
	if err != nil {
		return err
	}
	// Validated together, before a single row is written: a fixture naming a file
	// that is not there, or a SKU that is not one of its own variants, should fail
	// with the name of the thing rather than halfway through a partly loaded
	// catalog.
	if err := checkFiles(products, filepath.Dir(*file), cfg.DownloadsEnabled()); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := catalog.NewStore(pool)
	dir := filepath.Dir(*file)
	for _, p := range products {
		out, err := store.Upsert(ctx, p.Product)
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.Slug, err)
		}

		note := fmt.Sprintf("%d variant(s)", len(out.Variants))
		if len(p.Files) > 0 {
			added, updated, err := seedFiles(ctx, store, files, dir, out, p.Files)
			if err != nil {
				return err
			}
			note += fmt.Sprintf(", %d file(s) uploaded, %d already there", added, updated)
			if orphans := unreachableFiles(p.Files); len(orphans) > 0 {
				// Legal, and the admin allows it too, but it is almost always a
				// fixture mistake rather than an intention.
				note += fmt.Sprintf(" — %s reach no variant and nobody can download them",
					strings.Join(orphans, ", "))
			}
		}
		fmt.Printf("%-40s %s\n", out.Slug, note)
	}
	fmt.Printf("seeded %d product(s) from %s\n", len(products), *file)
	return nil
}

// unreachableFiles names the fixture's files that no variant grants. A file with
// no variant is stored and downloadable by nobody.
func unreachableFiles(files []seedFile) []string {
	var out []string
	for _, f := range files {
		if len(f.Variants) == 0 {
			out = append(out, filepath.Base(f.Path))
		}
	}
	return out
}

// readProducts decodes and validates the file before anything is written, so a
// typo in a fixture fails with the field that caused it rather than halfway
// through a partly loaded catalog.
func readProducts(path string) ([]seedProduct, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("seed: open %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var products []seedProduct
	if err := dec.Decode(&products); err != nil {
		return nil, fmt.Errorf("seed: parse %s: %w", path, err)
	}

	for i := range products {
		p := &products[i]
		if p.Slug == "" {
			p.Slug = catalog.Slugify(p.Title)
		}
		if errs := validate.Product(p.Product); errs.Any() {
			return nil, fmt.Errorf("seed: product %q: %s", p.Title, errs)
		}
		for _, v := range p.Variants {
			if errs := validate.Variant(v); errs.Any() {
				return nil, fmt.Errorf("seed: product %q variant %q: %s", p.Title, v.SKU, errs)
			}
		}
	}
	return products, nil
}
