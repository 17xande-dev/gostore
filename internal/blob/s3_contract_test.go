package blob

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// The tests here talk to a real S3-compatible server, so they run only when
// BLOB_TEST_ENDPOINT is set. They exist because the fakes cannot tell you that
// the client library still agrees with R2-style servers after an update.
//
//	docker compose --profile s3 up -d --wait rustfs
//	for b in gostore-test-images gostore-test-downloads; do
//	  curl -s --aws-sigv4 aws:amz:auto:s3 --user rustfsadmin:rustfsadmin -X PUT http://127.0.0.1:9000/$b
//	done
//	BLOB_TEST_ENDPOINT=127.0.0.1:9000 go test ./internal/blob/
//
// BLOB_TEST_ACCESS_KEY and BLOB_TEST_SECRET_KEY override the RustFS defaults, and
// BLOB_TEST_IMAGES_BUCKET / BLOB_TEST_DOWNLOADS_BUCKET the two bucket names. The
// buckets must already exist and are used only for keys under "contract-test/".
func contractConfig(t *testing.T, bucketEnv, bucketDefault string) S3Config {
	t.Helper()
	endpoint := os.Getenv("BLOB_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_TEST_ENDPOINT is not set; no S3-compatible server to test against")
	}
	getenv := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	return S3Config{
		Endpoint:      endpoint,
		Bucket:        getenv(bucketEnv, bucketDefault),
		AccessKey:     getenv("BLOB_TEST_ACCESS_KEY", "rustfsadmin"),
		SecretKey:     getenv("BLOB_TEST_SECRET_KEY", "rustfsadmin"),
		Region:        "auto",
		UseTLS:        false,
		PublicBaseURL: "http://" + endpoint + "/" + getenv(bucketEnv, bucketDefault),
	}
}

func TestS3_RealServerRoundTrip(t *testing.T) {
	store, err := NewS3(contractConfig(t, "BLOB_TEST_IMAGES_BUCKET", "gostore-test-images"))
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	const key = "contract-test/pixel.png"
	png := []byte("\x89PNG\r\n\x1a\n contract test")

	u, err := store.Put(t.Context(), key, bytes.NewReader(png), int64(len(png)), "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if u != store.URL(key) {
		t.Errorf("Put returned %q, URL(key) is %q", u, store.URL(key))
	}
	// Buckets created by the recipe above are private, so the object is read
	// back through the S3 API rather than the public URL.
	got, err := readObject(t, store.client, store.bucket, key)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Errorf("read back %q, want %q", got, png)
	}
	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(t.Context(), key); err != nil {
		t.Errorf("Delete of a missing key = %v, want nil", err)
	}
}

func TestS3Downloads_RealServerPresignedGet(t *testing.T) {
	store, err := NewS3Downloads(contractConfig(t, "BLOB_TEST_DOWNLOADS_BUCKET", "gostore-test-downloads"))
	if err != nil {
		t.Fatalf("NewS3Downloads: %v", err)
	}
	const key = "contract-test/book.pdf"
	body := bytes.Repeat([]byte("purchased bytes "), 1000)

	if err := store.Put(t.Context(), key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(t.Context(), key) })

	link, ok, err := store.PresignGet(t.Context(), key, "My Book.pdf", time.Minute)
	if err != nil || !ok {
		t.Fatalf("PresignGet = %q, %v, %v", link, ok, err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET presigned URL: %v", err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("presigned GET = %d with %d bytes, want 200 with %d", res.StatusCode, len(got), len(body))
	}
	if cd := res.Header.Get("Content-Disposition"); cd == "" {
		t.Errorf("no Content-Disposition on the presigned response; the filename override was ignored")
	}

	rc, size, err := store.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rc.Close()
	if size != int64(len(body)) {
		t.Errorf("Open size = %d, want %d", size, len(body))
	}

	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Open(t.Context(), key); err == nil {
		t.Errorf("Open after Delete succeeded, want an error")
	}
}

func readObject(t *testing.T, c *minio.Client, bucket, key string) ([]byte, error) {
	t.Helper()
	obj, err := c.GetObject(t.Context(), bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}
