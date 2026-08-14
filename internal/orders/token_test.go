package orders

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestNewToken(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	// URL-safe and unpadded, because it goes in a path segment and into an email a
	// person may retype.
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("the token is not raw URL-safe base64: %v", err)
	}
	if len(raw) != tokenBytes {
		t.Errorf("the token carries %d bytes of entropy, want %d", len(raw), tokenBytes)
	}

	// The stored value is not the credential. This is the whole property: a dump of
	// the entitlements table must not be a set of working download links.
	if bytes.Contains(hash, []byte(token)) {
		t.Error("the stored hash contains the token")
	}
	if len(hash) != 32 {
		t.Errorf("the hash is %d bytes, want a SHA-256 digest", len(hash))
	}
	if !bytes.Equal(hash, HashToken(token)) {
		t.Error("HashToken does not reproduce the stored hash, so no lookup could ever match")
	}
}

func TestNewTokenIsUnpredictable(t *testing.T) {
	// Two buyers of the same variant must never be handed the same link. A
	// generator seeded per process, or one derived from the order, would fail here
	// and pass every test that only ever mints one.
	seen := make(map[string]bool, 100)
	for range 100 {
		token, _, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if seen[token] {
			t.Fatal("NewToken repeated a token")
		}
		seen[token] = true
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	// The lookup hashes the token from the URL and compares. If this were salted or
	// otherwise varied per call, no download would ever resolve.
	if !bytes.Equal(HashToken("abc"), HashToken("abc")) {
		t.Error("HashToken is not deterministic")
	}
	if bytes.Equal(HashToken("abc"), HashToken("abd")) {
		t.Error("HashToken collides on a one-character difference")
	}
}
