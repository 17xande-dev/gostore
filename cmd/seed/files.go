package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
)

// seedProduct is a product as a fixture writes it: the domain type plus the one
// thing a fixture can express that the domain type deliberately cannot.
//
// catalog.Product keeps Files un-decodable — a file's object key is storage's to
// choose and its size and content type are facts about bytes, none of which a
// JSON file has any business asserting. What a fixture *can* say is "this path on
// disk, under this title, granted by these variants", which is what seedFile is.
type seedProduct struct {
	catalog.Product
	Files []seedFile `json:"files,omitempty"`
}

type seedFile struct {
	// Path is relative to the directory the seed file itself is in, so a fixture
	// and its payloads move together. Absolute paths and anything climbing out of
	// that directory are refused: a seed file is data, and data should not be able
	// to name /etc/passwd and have it uploaded to a bucket.
	Path string `json:"path"`

	// Title is what the buyer sees. Defaults to the file's name.
	Title string `json:"title,omitempty"`

	// Variants are the SKUs that grant this file — the fixture's natural key for a
	// variant, the same one Upsert matches on. A file with none is stored and
	// downloadable by nobody, which the seed warns about rather than refusing,
	// because it is a legitimate half-finished state in the admin too.
	Variants []string `json:"variants,omitempty"`
}

// openDownloads builds the private store the fixture's files go into.
//
// It mirrors the server's newDownloadStorage and stays separate from it on
// purpose: the server logs what it chose at boot because an operator is reading
// those lines, and a command-line tool says so in its output instead.
func openDownloads(cfg config.Config) (blob.Downloads, error) {
	switch {
	case cfg.DownloadDir != "":
		return blob.NewDiskDownloads(cfg.DownloadDir)
	case cfg.Downloads.Configured():
		return blob.NewS3Downloads(blob.S3Config{
			Endpoint:       cfg.Downloads.Endpoint,
			Bucket:         cfg.Downloads.Bucket,
			AccessKey:      cfg.Downloads.AccessKey,
			SecretKey:      cfg.Downloads.SecretKey,
			Region:         cfg.Downloads.Region,
			UseTLS:         cfg.Downloads.UseTLS,
			PublicEndpoint: cfg.Downloads.PublicEndpoint,
		})
	default:
		return blob.NoDownloads{}, nil
	}
}

// seedFiles attaches a fixture's files to a product that has just been upserted.
//
// Rerunnable, on the same terms as everything else in this command: files match
// on the name they were seeded from, so a second run retitles and re-links rather
// than uploading a second copy. That matters for more than tidiness — replacing
// the row would mint a new file id, and a buyer holding an entitlement would find
// the file they paid for had quietly become a different one.
func seedFiles(ctx context.Context, store *catalog.Store, files blob.Downloads,
	dir string, p catalog.Product, want []seedFile) (added, updated int, err error) {

	bySKU := make(map[string]string, len(p.Variants))
	for _, v := range p.Variants {
		bySKU[v.SKU] = v.ID
	}

	existing, err := store.Files(ctx, p.ID)
	if err != nil {
		return 0, 0, err
	}
	byName := make(map[string]catalog.File, len(existing))
	for _, f := range existing {
		// First match wins. The column is not unique — an operator may legitimately
		// upload two files with the same name through the admin — so this is a
		// convention the fixture keeps rather than one the schema enforces.
		if _, seen := byName[f.OriginalFilename]; !seen {
			byName[f.OriginalFilename] = f
		}
	}

	for i, sf := range want {
		path, err := resolve(dir, sf.Path)
		if err != nil {
			return added, updated, err
		}
		name := filepath.Base(path)

		variantIDs := make([]string, 0, len(sf.Variants))
		for _, sku := range sf.Variants {
			id, ok := bySKU[sku]
			if !ok {
				return added, updated, fmt.Errorf(
					"seed: %s: file %q names SKU %q, which is not a variant of this product",
					p.Slug, sf.Path, sku)
			}
			variantIDs = append(variantIDs, id)
		}

		title := sf.Title
		if title == "" {
			title = name
		}

		if old, ok := byName[name]; ok {
			// The bytes are already in storage under a key somebody may hold a link
			// to. Only the parts a fixture actually owns are rewritten.
			old.Title, old.Position = title, i
			if _, err := store.UpdateFile(ctx, old, variantIDs); err != nil {
				return added, updated, fmt.Errorf("seed: %s: update file %q: %w", p.Slug, name, err)
			}
			updated++
			continue
		}

		f, err := uploadFile(ctx, files, path, p.ID)
		if err != nil {
			return added, updated, fmt.Errorf("seed: %s: %w", p.Slug, err)
		}
		f.Title, f.Position = title, i
		if _, err := store.AddFile(ctx, f, variantIDs); err != nil {
			// The object is stored and the row is not, which is the same orphan the
			// admin upload path accepts and for the same reason: deleting here would
			// race a retry already uploading the same bytes.
			return added, updated, fmt.Errorf("seed: %s: record file %q (object %s is now orphaned): %w",
				p.Slug, name, f.ObjectKey, err)
		}
		added++
	}
	return added, updated, nil
}

// uploadFile puts one file into private storage and returns the row to record.
//
// The content type is sniffed rather than guessed from the extension, exactly as
// the admin upload does — so a fixture that names a file .mp3 and fills it with
// something else is stored as what it actually is.
func uploadFile(ctx context.Context, files blob.Downloads, path, productID string) (catalog.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return catalog.File{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return catalog.File{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return catalog.File{}, fmt.Errorf("%s is empty", path)
	}

	head := make([]byte, blob.SniffLimit)
	n, err := io.ReadFull(f, head)
	if n == 0 {
		return catalog.File{}, fmt.Errorf("%s is empty", path)
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return catalog.File{}, fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return catalog.File{}, fmt.Errorf("rewind %s: %w", path, err)
	}

	contentType := http.DetectContentType(head[:n])
	name := filepath.Base(path)
	key, err := blob.DownloadKey(productID, blob.DownloadExtension(contentType, name))
	if err != nil {
		return catalog.File{}, err
	}
	if err := files.Put(ctx, key, f, info.Size(), contentType); err != nil {
		return catalog.File{}, fmt.Errorf("upload %s: %w", name, err)
	}

	return catalog.File{
		ProductID:        productID,
		ObjectKey:        key,
		OriginalFilename: name,
		ContentType:      contentType,
		SizeBytes:        info.Size(),
	}, nil
}

// resolve turns a fixture's path into one inside the seed file's directory.
//
// A seed file is data. Data that can name any path on the machine and have it
// uploaded to a bucket is a way to exfiltrate a private key by editing a JSON
// file, so absolute paths and anything climbing out are refused rather than
// cleaned.
func resolve(dir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("seed: a file needs a path")
	}
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf(
			"seed: file path %q must be relative to the seed file and must not climb out of its directory", path)
	}
	return filepath.Join(dir, filepath.FromSlash(path)), nil
}

// checkFiles validates every fixture's files before anything is written, so a
// mistake fails with the field that caused it rather than halfway through a
// partly loaded catalog.
func checkFiles(products []seedProduct, dir string, downloads bool) error {
	for _, p := range products {
		if len(p.Files) == 0 {
			continue
		}
		if !p.Digital() {
			return fmt.Errorf(
				"seed: product %q has files but its kind is %q; only a digital product can have them",
				p.Slug, p.kindOrPhysical())
		}
		if !downloads {
			return fmt.Errorf(
				"seed: product %q has files but no download storage is configured; "+
					"set DOWNLOAD_DIR or the DOWNLOAD_* variables", p.Slug)
		}

		skus := make([]string, 0, len(p.Variants))
		for _, v := range p.Variants {
			skus = append(skus, v.SKU)
		}
		seen := make(map[string]bool, len(p.Files))
		for _, f := range p.Files {
			path, err := resolve(dir, f.Path)
			if err != nil {
				return err
			}
			name := filepath.Base(path)
			// Two fixture files with the same base name would match the same row on a
			// re-run and fight over it, so the second would silently retitle the first.
			if seen[name] {
				return fmt.Errorf("seed: product %q has two files named %q", p.Slug, name)
			}
			seen[name] = true

			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("seed: product %q: %w", p.Slug, err)
			}
			for _, sku := range f.Variants {
				if !slices.Contains(skus, sku) {
					return fmt.Errorf("seed: product %q: file %q names SKU %q, which is not one of its variants (%s)",
						p.Slug, f.Path, sku, strings.Join(skus, ", "))
				}
			}
		}
	}
	return nil
}

// kindOrPhysical is what to call a product's kind in a message, given that an
// unset one means physical.
func (p seedProduct) kindOrPhysical() catalog.Kind {
	if p.Kind == "" {
		return catalog.KindPhysical
	}
	return p.Kind
}
