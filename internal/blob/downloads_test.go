package blob

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiskDownloads_RoundTrip(t *testing.T) {
	d, err := NewDiskDownloads(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskDownloads: %v", err)
	}
	ctx := t.Context()
	body := strings.Repeat("audio", 1000)

	if err := d.Put(ctx, "downloads/p/a.mp3", strings.NewReader(body), int64(len(body)), "audio/mpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	r, size, err := d.Open(ctx, "downloads/p/a.mp3")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if size != int64(len(body)) {
		t.Errorf("size = %d, want %d", size, len(body))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Error("the bytes read back are not the bytes written")
	}

	if err := d.Delete(ctx, "downloads/p/a.mp3"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := d.Open(ctx, "downloads/p/a.mp3"); err == nil {
		t.Error("the object is still readable after Delete")
	}
	// Deleting something absent is not an error: the caller wanted it gone.
	if err := d.Delete(ctx, "downloads/p/a.mp3"); err != nil {
		t.Errorf("deleting a missing object: %v", err)
	}
}

func TestDiskDownloads_CannotSignAndSaysSo(t *testing.T) {
	// The whole reason PresignGet returns a bool rather than an error: a directory
	// having no signing key is a property of the configuration, not a failure, and
	// the caller streams instead.
	d, err := NewDiskDownloads(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskDownloads: %v", err)
	}
	link, ok, err := d.PresignGet(t.Context(), "k", "a.mp3", time.Minute)
	if err != nil {
		t.Errorf("PresignGet returned an error rather than declining: %v", err)
	}
	if ok || link != "" {
		t.Errorf("a directory claimed it could sign a URL: %q", link)
	}
}

func TestDiskDownloads_RefusesToEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDiskDownloads(dir)
	if err != nil {
		t.Fatalf("NewDiskDownloads: %v", err)
	}
	ctx := t.Context()

	// A secret next to, but outside, the download directory.
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, key := range []string{
		"../outside.txt",
		"downloads/../../outside.txt",
		"/etc/passwd",
		"",
	} {
		if _, _, err := d.Open(ctx, key); err == nil {
			t.Errorf("Open(%q) succeeded", key)
		}
		if err := d.Put(ctx, key, strings.NewReader("x"), 1, ""); err == nil {
			t.Errorf("Put(%q) succeeded", key)
		}
	}

	// A symlink planted inside the directory must not escape either, which is what
	// os.Root buys over a filepath.Clean check.
	link := filepath.Join(dir, "sneaky")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := d.Open(ctx, "sneaky"); err == nil {
		t.Error("a symlink out of the directory was followed")
	}
}

func TestDiskDownloads_RefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDiskDownloads(dir)
	if err != nil {
		t.Fatalf("NewDiskDownloads: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Without the IsRegular check the handler would answer 200 and then copy
	// something meaningless.
	if _, _, err := d.Open(t.Context(), "sub"); err == nil {
		t.Error("a directory was served as a download")
	}
}

func TestNoDownloads_RefusesEverything(t *testing.T) {
	// Refusing, where email.Discard reports success. An upload is something an
	// operator is doing and watching, so it has to fail with a message.
	var d Downloads = NoDownloads{}
	ctx := context.Background()

	if err := d.Put(ctx, "k", strings.NewReader("x"), 1, ""); err == nil {
		t.Error("Put succeeded with no storage configured")
	}
	if _, _, err := d.PresignGet(ctx, "k", "f", time.Minute); err == nil {
		t.Error("PresignGet succeeded with no storage configured")
	}
	if _, _, err := d.Open(ctx, "k"); err == nil {
		t.Error("Open succeeded with no storage configured")
	}
}

func TestContentDisposition(t *testing.T) {
	// The filename comes from an upload form, so a quote or a newline in it would
	// end the quoted string or inject a second header field.
	cases := map[string]string{
		"session-one.mp3":     `attachment; filename="session-one.mp3"`,
		`ev"il.mp3`:           `attachment; filename="evil.mp3"`,
		"a\r\nX-Evil: yes":    `attachment; filename="aX-Evil: yes"`,
		"naïve.mp3":           `attachment; filename="nave.mp3"`,
		`back\slash.mp3`:      `attachment; filename="backslash.mp3"`,
		"":                    `attachment; filename="download"`,
		"\x00\x01\x02":        `attachment; filename="download"`,
		"  spaced out.mp3   ": `attachment; filename="spaced out.mp3"`,
	}
	for in, want := range cases {
		if got := contentDisposition(in); got != want {
			t.Errorf("contentDisposition(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownloadKey(t *testing.T) {
	first, err := DownloadKey("prod-1", ".mp4")
	if err != nil {
		t.Fatalf("DownloadKey: %v", err)
	}
	second, _ := DownloadKey("prod-1", ".mp4")
	if first == second {
		t.Error("two keys for the same product collided")
	}
	if !strings.HasPrefix(first, "downloads/prod-1/") || !strings.HasSuffix(first, ".mp4") {
		t.Errorf("key %q is not the documented shape", first)
	}
	// Sixteen random bytes, hex-encoded: more than an image key, because these
	// bytes were paid for and the key should not be the weak link if a bucket is
	// ever misconfigured.
	name := strings.TrimSuffix(strings.TrimPrefix(first, "downloads/prod-1/"), ".mp4")
	if len(name) != 32 {
		t.Errorf("the random component is %d hex characters, want 32", len(name))
	}

	// An extensionless upload is legal — DetectContentType often cannot name a
	// media file, and the extension is cosmetic since Content-Type is stored.
	bare, err := DownloadKey("prod-1", "")
	if err != nil || strings.Contains(strings.TrimPrefix(bare, "downloads/prod-1/"), ".") {
		t.Errorf("DownloadKey with no extension = %q, %v", bare, err)
	}
}

func TestDownloadExtension(t *testing.T) {
	cases := []struct {
		contentType, filename, want string
	}{
		{"audio/mpeg", "whatever", ".mp3"},
		// The spelling net/http actually emits for a RIFF/WAVE file, which is what
		// testdata/downloads ships.
		{"audio/wave", "whatever", ".wav"},
		{"video/mp4", "whatever", ".mp4"},
		{"application/pdf; charset=binary", "notes", ".pdf"},
		// DetectContentType answers octet-stream for flac and several mp4
		// profiles, so the uploaded name is the only remaining evidence. Used for
		// the extension alone, never for the key or the served type.
		{"application/octet-stream", "session.flac", ".flac"},
		{"application/octet-stream", "no-extension", ""},
		// An unexpected extension is *not* refused, and that is deliberate: the
		// object goes into a private bucket under a key this store chose, is served
		// with the Content-Type from its own column plus nosniff and an attachment
		// disposition, and only ever reaches somebody who paid. The extension is
		// cosmetic, so refusing one would only stop a shop selling what it sells.
		{"application/octet-stream", "archive.sh", ".sh"},
		// What is refused is anything that could become a second path segment, or a
		// suffix long enough not to be an extension at all.
		{"application/octet-stream", "a/b", ""},
		{"application/octet-stream", "odd.name with space", ""},
		{"application/octet-stream", "x.verylongsuffix", ""},
		{"application/octet-stream", "trailing.", ""},
	}
	for _, tc := range cases {
		if got := DownloadExtension(tc.contentType, tc.filename); got != tc.want {
			t.Errorf("DownloadExtension(%q, %q) = %q, want %q",
				tc.contentType, tc.filename, got, tc.want)
		}
	}
}
