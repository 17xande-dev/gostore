package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FakeDownloads keeps purchased files in memory, so the download path can be
// exercised end to end without MinIO running.
//
// It can act as either kind of backend, which is the reason it exists rather than
// the tests using DiskDownloads with a t.TempDir(). The handler has two endings —
// redirect to a signed URL, or stream the bytes — and which one runs is decided by
// PresignGet's ok. A test that only ever exercised one of them would leave the
// other unproven, so Presign flips between them.
type FakeDownloads struct {
	// Err, when set, is returned by Put and Delete, standing in for a bucket that
	// is unreachable or credentials that have been revoked.
	Err error

	// Presign chooses the backend this fake imitates. True is a bucket: PresignGet
	// returns a URL and the handler redirects. False is a directory: PresignGet
	// declines and the handler streams through Open.
	Presign bool

	// Base is the origin the fake claims to sign URLs against.
	Base string

	mu      sync.Mutex
	objects map[string]Object
	deleted []string
	// presigned records every URL handed out, so a test can assert that a link was
	// minted per click rather than reused.
	presigned []string
}

func NewFakeDownloads() *FakeDownloads {
	return &FakeDownloads{
		Presign: true,
		Base:    "https://downloads.example",
		objects: map[string]Object{},
	}
}

func (f *FakeDownloads) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	if f.Err != nil {
		return f.Err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("blob: fake put download %s: %w", key, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = Object{Body: body, ContentType: contentType}
	return nil
}

func (f *FakeDownloads) Delete(_ context.Context, key string) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *FakeDownloads) PresignGet(_ context.Context, key, filename string, ttl time.Duration) (string, bool, error) {
	if !f.Presign {
		return "", false, nil
	}
	base := f.Base
	if base == "" {
		base = "https://downloads.example"
	}
	q := url.Values{}
	q.Set("response-content-disposition", contentDisposition(filename))
	// An expiry in the URL, so a test can see that one is set and that it is the
	// TTL the caller asked for rather than a hardcoded value.
	q.Set("expires", fmt.Sprintf("%d", int(ttl.Seconds())))

	link := base + "/" + key + "?" + q.Encode()
	f.mu.Lock()
	f.presigned = append(f.presigned, link)
	f.mu.Unlock()
	return link, true, nil
}

func (f *FakeDownloads) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	if !ok {
		return nil, 0, fmt.Errorf("blob: fake download %s does not exist", key)
	}
	return io.NopCloser(bytes.NewReader(o.Body)), int64(len(o.Body)), nil
}

// Get returns a stored object.
func (f *FakeDownloads) Get(key string) (Object, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return o, ok
}

// Keys returns every stored key.
func (f *FakeDownloads) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		keys = append(keys, k)
	}
	return keys
}

// Deleted returns the keys Delete was called with, in order.
func (f *FakeDownloads) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// Presigned returns every URL handed out, in order.
func (f *FakeDownloads) Presigned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.presigned...)
}

// SniffLimit is how many leading bytes are read to identify an upload. 512 is
// what http.DetectContentType looks at, so reading more would tell nobody
// anything.
const SniffLimit = 512

// DownloadExtension returns the file extension to store an upload under, from
// its sniffed content type and — only as a fallback — the extension of the name
// it was uploaded with.
//
// Unlike Validate this is not an allow-list. An image goes into a public bucket
// where serving evil.html would be a cross-site scripting hole on a hostname the
// shop owns; a download goes into a private bucket and reaches one buyer who paid
// for it, so refusing an unusual format would only stop a shop selling what it
// sells. The extension is cosmetic here — it never decides how anything is served,
// because Content-Type comes from the stored column.
func DownloadExtension(contentType, filename string) string {
	switch base, _, _ := strings.Cut(contentType, ";"); strings.TrimSpace(base) {
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/epub+zip":
		return ".epub"
	}

	// http.DetectContentType answers application/octet-stream for most media it
	// does not know, including flac and several mp4 profiles, so the uploaded name
	// is the only remaining evidence. It is used for the extension alone and never
	// for the key or the served type, which is what makes trusting it this far safe.
	if i := strings.LastIndex(filename, "."); i >= 0 && i < len(filename)-1 {
		ext := strings.ToLower(filename[i:])
		if len(ext) <= 6 && !strings.ContainsAny(ext, `/\ `) {
			return ext
		}
	}
	return ""
}
