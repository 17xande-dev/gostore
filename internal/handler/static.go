package handler

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed static/htmx.min.js static/htmx.LICENSE
var staticFS embed.FS

// staticAsset is one embedded file, with the URL it is served at.
type staticAsset struct {
	body        []byte
	contentType string
	etag        string
	url         string
}

// staticAssets is an explicit list rather than a directory walk: whatever ends
// up in that directory later — notes, a licence, a half-finished experiment —
// should not become publicly reachable by being saved there.
var staticAssets = sync.OnceValue(func() map[string]staticAsset {
	files := map[string]string{
		"htmx.min.js":  "text/javascript; charset=utf-8",
		"htmx.LICENSE": "text/plain; charset=utf-8",
	}

	assets := make(map[string]staticAsset, len(files))
	for name, contentType := range files {
		body, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			// Embedded at compile time, so this can only mean the list above and
			// the go:embed pattern have drifted apart.
			panic(fmt.Sprintf("handler: embedded asset %s: %v", name, err))
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])[:12]
		assets[name] = staticAsset{
			body:        body,
			contentType: contentType,
			etag:        `"` + digest + `"`,
			// The URL carries a hash of the content, so it can be cached
			// forever and an upgrade invalidates it automatically — no version
			// number to remember to bump.
			url: "/static/" + name + "?v=" + digest,
		}
	}
	return assets
})

// assetURL is the template function behind {{asset "htmx.min.js"}}.
func assetURL(name string) string {
	a, ok := staticAssets()[name]
	if !ok {
		// Visible in the page rather than silently absent: a mistyped asset
		// name should fail loudly while it is still being written.
		return "/static/missing/" + name
	}
	return a.url
}

func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	a, ok := staticAssets()[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("ETag", a.etag)
	// Immutable is honest here only because the URL is content-addressed.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.body))
}
