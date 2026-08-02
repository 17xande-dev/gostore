// Command seed loads a products JSON file into the database.
//
// It is rerunnable: products match on slug and variants on SKU, so seeding
// twice updates rather than duplicates, and stock counts on rows that already
// exist are left where the operator put them.
//
//	DATABASE_URL=... go run ./cmd/seed -file testdata/products.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := catalog.NewStore(pool)
	for _, p := range products {
		out, err := store.Upsert(ctx, p)
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.Slug, err)
		}
		fmt.Printf("%-40s %d variant(s)\n", out.Slug, len(out.Variants))
	}
	fmt.Printf("seeded %d product(s) from %s\n", len(products), *file)
	return nil
}

// readProducts decodes and validates the file before anything is written, so a
// typo in a fixture fails with the field that caused it rather than halfway
// through a partly loaded catalog.
func readProducts(path string) ([]catalog.Product, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("seed: open %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var products []catalog.Product
	if err := dec.Decode(&products); err != nil {
		return nil, fmt.Errorf("seed: parse %s: %w", path, err)
	}

	for i := range products {
		p := &products[i]
		if p.Slug == "" {
			p.Slug = catalog.Slugify(p.Title)
		}
		if errs := validate.Product(*p); errs.Any() {
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
