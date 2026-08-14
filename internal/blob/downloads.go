package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// Downloads is private object storage: bytes only a paying buyer may read.
//
// It is a second interface rather than three more methods on Storage, and the
// split is the point. Storage exists to put an image somewhere the whole internet
// can fetch it, which is why it has a URL method and no Get. A purchased file is
// the opposite in every respect — no public URL exists, reads are authorised one
// at a time, and the bucket must not be publicly readable — so giving the image
// path a Get it must never call would be an invitation rather than a feature.
//
// # How a download actually reaches somebody
//
// The store never hands out a bucket URL. A buyer's link points at this server,
// which checks the entitlement and records the click, and only then produces the
// bytes. PresignGet is the fast path: a URL signed with the bucket credentials,
// valid for minutes, that the browser is redirected to — so a 2 GB video is
// served by the bucket, with range requests and resume, and never passes through
// Go. Open is the fallback for a backend that cannot sign, where the server
// streams the file itself.
//
// Every implementation must provide both, because the caller decides which to use
// from PresignGet's ok, not from knowing which backend it has.
type Downloads interface {
	// Put stores size bytes read from r under key. A negative size means the
	// length is not known in advance.
	//
	// r is streamed, not buffered whole: the caller may hand this a two-gigabyte
	// video. What the S3 implementation does hold is one fixed-size part buffer —
	// see uploadPartSize — which is a constant rather than a share of the file.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error

	// Delete removes an object. Deleting something absent is not an error, for the
	// same reason it is not one in Storage: the caller wanted it gone.
	Delete(ctx context.Context, key string) error

	// PresignGet returns a short-lived URL for the object, with a
	// Content-Disposition that makes the browser save it under filename rather
	// than play it in a tab.
	//
	// ok is false when the backend cannot sign — the disk backend — and is not an
	// error: the caller streams with Open instead. That is why this returns a bool
	// rather than a sentinel error, since "cannot sign" is a routine property of a
	// configuration, not a failure.
	PresignGet(ctx context.Context, key, filename string, ttl time.Duration) (link string, ok bool, err error)

	// Open streams the object, returning its size so the caller can set
	// Content-Length. Reached only when PresignGet said ok=false.
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// DefaultPresignTTL is how long a signed download URL lives.
//
// Five minutes is a deliberate compromise. It has to outlast a slow connection
// starting a large transfer — the signature is checked when the request begins,
// not throughout — while being short enough that a link forwarded to a friend, or
// left in a browser history, is already dead. It also bounds how long a revoked
// entitlement can still be used: at most one URL, for at most this long.
const DefaultPresignTTL = 5 * time.Minute

// DefaultMaxDownloadBytes caps an uploaded download file.
//
// Two gigabytes, against MaxUploadBytes' five megabytes for an image, because
// these are audio and video. Memory does not scale with the file — see
// uploadPartSize — so this limit is about what a shop should store, how much
// temporary disk a request needs, and how long it may hold a connection open.
const DefaultMaxDownloadBytes int64 = 2 << 30

// ErrDownloadsNotConfigured is returned by NoDownloads. Distinct from
// ErrNotConfigured so a message can say which of the two stores is missing —
// they are configured separately and an operator who set up one and not the other
// needs to be told which.
var ErrDownloadsNotConfigured = errors.New("blob: download storage is not configured")

// NoDownloads is the Downloads for a deployment that sells no digital products.
// Every method refuses, on the same grounds as Unconfigured: an upload is
// something an operator is watching, so it must fail with a message.
type NoDownloads struct{}

func (NoDownloads) Put(context.Context, string, io.Reader, int64, string) error {
	return ErrDownloadsNotConfigured
}

func (NoDownloads) Delete(context.Context, string) error { return ErrDownloadsNotConfigured }

func (NoDownloads) PresignGet(context.Context, string, string, time.Duration) (string, bool, error) {
	return "", false, ErrDownloadsNotConfigured
}

func (NoDownloads) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, ErrDownloadsNotConfigured
}

// S3Downloads is Downloads against a private S3-compatible bucket: Google Cloud
// Storage through its interoperability XML API, Cloudflare R2, or MinIO.
//
// It reuses the S3 client for the same reasons documented on S3 — minio-go speaks
// the conservative subset all three agree on — but against a different bucket,
// with no public base URL. There is no URL method here at all, and that is the
// enforcement: a key from this store has no address anybody can be given.
type S3Downloads struct {
	client *minio.Client
	bucket string
}

// NewS3Downloads validates the configuration and returns Downloads.
//
// Like NewS3 it does not connect. A shop whose download storage is misconfigured
// should still sell its physical products, so the failure belongs at the first
// upload where an operator sees it, not at boot where it stops the shop.
func NewS3Downloads(cfg S3Config) (*S3Downloads, error) {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"endpoint", cfg.Endpoint},
		{"bucket", cfg.Bucket},
		{"access key", cfg.AccessKey},
		{"secret key", cfg.SecretKey},
	} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("blob: missing download configuration: %s", strings.Join(missing, ", "))
	}
	if strings.Contains(cfg.Endpoint, "://") {
		return nil, fmt.Errorf("blob: download endpoint %q must be host[:port] with no scheme", cfg.Endpoint)
	}

	client, err := newMinioClient(cfg)
	if err != nil {
		return nil, err
	}
	return &S3Downloads{client: client, bucket: cfg.Bucket}, nil
}

// uploadPartSize is how much of an upload the client holds in memory at once.
//
// Pinned rather than left to minio-go, which derives a part size from the object
// size — so the buffer, and the memory, would grow with the file.
//
// Sixteen mebibytes is comfortably above the 5 MiB protocol minimum and makes the
// cost a constant. Measured against the compose MinIO: a 477 MB upload grew the
// server's resident memory by 75 MB and a 1.43 GB upload by 65 MB. Three times the
// file, no more memory — which is the property that matters, and the reason the
// figure is not the 16 MiB this constant names: it also covers the client's other
// buffers and whatever the collector has not yet returned.
//
// It bounds an object at 16 MiB × 10 000 parts, about 160 GB, far past anything
// DOWNLOAD_MAX_BYTES will allow.
const uploadPartSize = 16 << 20

func (s *S3Downloads) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	// No CacheControl, unlike an image. These objects are fetched through
	// short-lived signed URLs and must not be cached by anything in between.
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
		PartSize:    uploadPartSize,
	})
	if err != nil {
		return fmt.Errorf("blob: put download %s: %w", key, err)
	}
	return nil
}

func (s *S3Downloads) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("blob: delete download %s: %w", key, err)
	}
	return nil
}

func (s *S3Downloads) PresignGet(ctx context.Context, key, filename string, ttl time.Duration) (string, bool, error) {
	params := url.Values{}
	params.Set("response-content-disposition", contentDisposition(filename))

	link, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return "", false, fmt.Errorf("blob: presign download %s: %w", key, err)
	}
	return link.String(), true, nil
}

func (s *S3Downloads) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("blob: open download %s: %w", key, err)
	}
	// GetObject is lazy — it does not talk to the bucket until the first read — so
	// Stat is what turns a missing object into an error here rather than halfway
	// through a response that has already claimed 200.
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, fmt.Errorf("blob: stat download %s: %w", key, err)
	}
	return obj, info.Size, nil
}

// DiskDownloads keeps purchased files in a directory, for a shop that wants no
// object storage at all.
//
// It carries Disk's caveat — two instances do not share a directory — and adds
// one of its own: it cannot sign a URL, so every download is streamed through
// this server. That is correct but not free, and a shop selling large video to
// many people wants a bucket.
//
// The directory must not be the one images are served from. Nothing here enforces
// that, because nothing here can, but the config refuses the overlap.
type DiskDownloads struct {
	dir  string
	root *os.Root
}

// NewDiskDownloads checks the directory is usable and returns Downloads.
func NewDiskDownloads(dir string) (*DiskDownloads, error) {
	if dir == "" {
		return nil, errors.New("blob: download directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("blob: download directory %q: %w", dir, err)
	}
	// 0700, not 0755: unlike images these bytes are paid for, and on a shared host
	// the directory should not be readable by every other account on the machine.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("blob: create download directory %q: %w", abs, err)
	}
	probe, err := os.CreateTemp(abs, ".writable-*")
	if err != nil {
		return nil, fmt.Errorf("blob: download directory %q is not writable: %w", abs, err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)

	// os.Root rather than path checking: a symlink planted inside the directory
	// cannot escape it either, which filepath.Clean would not catch.
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("blob: open download directory %q: %w", abs, err)
	}
	return &DiskDownloads{dir: abs, root: root}, nil
}

// Dir is the directory being used, for logging at boot.
func (d *DiskDownloads) Dir() string { return d.dir }

// Put writes the object through a temporary file and a rename, so a crash or a
// full disk cannot leave a truncated file that a buyer would receive as a
// corrupt download.
func (d *DiskDownloads) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	full, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fmt.Errorf("blob: create directory for %s: %w", key, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return fmt.Errorf("blob: create temporary file for %s: %w", key, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("blob: write %s: %w", key, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("blob: sync %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob: close %s: %w", key, err)
	}
	// 0600, against Disk's 0644: this file is never served by anything but this
	// process, after it has checked an entitlement.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("blob: chmod %s: %w", key, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("blob: install %s: %w", key, err)
	}
	return nil
}

func (d *DiskDownloads) Delete(_ context.Context, key string) error {
	full, err := d.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: delete %s: %w", key, err)
	}
	// Take the product's directory with it when it empties. A non-empty directory
	// fails here, which is the intended no-op.
	os.Remove(filepath.Dir(full))
	return nil
}

// PresignGet always reports that it cannot sign. A directory has no signing key
// and no address, so the caller streams instead.
func (d *DiskDownloads) PresignGet(context.Context, string, string, time.Duration) (string, bool, error) {
	return "", false, nil
}

func (d *DiskDownloads) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	if key == "" || !filepath.IsLocal(key) {
		return nil, 0, fmt.Errorf("blob: refusing unsafe object key %q", key)
	}
	f, err := d.root.Open(filepath.ToSlash(key))
	if err != nil {
		return nil, 0, fmt.Errorf("blob: open %s: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("blob: stat %s: %w", key, err)
	}
	// A directory or a device is not a download. Without this the handler would
	// answer 200 and then copy something meaningless.
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("blob: %s is not a regular file", key)
	}
	return f, info.Size(), nil
}

func (d *DiskDownloads) resolve(key string) (string, error) {
	if key == "" || !filepath.IsLocal(key) {
		return "", fmt.Errorf("blob: refusing unsafe object key %q", key)
	}
	return filepath.Join(d.dir, filepath.FromSlash(key)), nil
}

// contentDisposition builds a header that makes a browser save the file rather
// than render it, under a name the buyer will recognise.
//
// The filename is quoted and stripped of anything that could end the quoted
// string or inject a second header field. It arrives from an upload form, so it
// is not this store's to trust — and RFC 6266's filename* form is deliberately
// not used, because the ASCII fallback is what every client understands and the
// stakes here are a download's suggested name.
func contentDisposition(filename string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '\\' || r == '\r' || r == '\n':
			return -1
		case r < 0x20 || r == 0x7f:
			return -1
		case r > 0x7e:
			return -1
		}
		return r
	}, filename)

	clean = strings.TrimSpace(clean)
	if clean == "" {
		clean = "download"
	}
	return `attachment; filename="` + clean + `"`
}
