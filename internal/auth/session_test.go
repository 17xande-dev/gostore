package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/securecookie"
	"golang.org/x/crypto/bcrypt"
)

var (
	testSecret = bytes.Repeat([]byte("k"), MinSecretLen)
	otherKey   = bytes.Repeat([]byte("j"), MinSecretLen)
)

func newTestSessions(t *testing.T, secret, previous []byte, ttl time.Duration) *Sessions {
	t.Helper()
	s, err := NewSessions(secret, previous, ttl)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	return s
}

func TestNewSessions_RejectsWeakInput(t *testing.T) {
	short := bytes.Repeat([]byte("k"), MinSecretLen-1)
	cases := map[string]struct {
		secret, previous []byte
		ttl              time.Duration
	}{
		"short secret":          {short, nil, time.Hour},
		"short previous secret": {testSecret, short, time.Hour},
		"no secret":             {nil, nil, time.Hour},
		"zero ttl":              {testSecret, nil, 0},
		"negative ttl":          {testSecret, nil, -time.Hour},
	}
	for name, tc := range cases {
		if _, err := NewSessions(tc.secret, tc.previous, tc.ttl); err == nil {
			t.Errorf("%s: NewSessions accepted it", name)
		}
	}
}

func TestSession_RoundTrips(t *testing.T) {
	s := newTestSessions(t, testSecret, nil, time.Hour)
	now := time.Now()

	value, err := s.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	expiry, err := s.Verify(value, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Whole seconds: the expiry is carried as a unix timestamp.
	if want := now.Add(time.Hour).Truncate(time.Second); !expiry.Equal(want) {
		t.Errorf("expiry = %s, want %s", expiry, want)
	}

	if _, err := s.Verify(value, now.Add(59*time.Minute)); err != nil {
		t.Errorf("Verify before expiry: %v", err)
	}
	if _, err := s.Verify(value, now.Add(time.Hour+2*time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify after expiry = %v, want ErrExpired", err)
	}
}

func TestVerify_RejectsForgeries(t *testing.T) {
	s := newTestSessions(t, testSecret, nil, time.Hour)
	now := time.Now()

	value, err := s.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The encoded form is base64 over "timestamp|payload|mac", so there is no
	// separately submittable payload to strip — truncating or flipping a byte is
	// what tampering actually looks like here.
	cases := map[string]string{
		"empty":        "",
		"garbage":      "not-a-cookie",
		"truncated":    value[:len(value)-4],
		"flipped byte": flipLast(value),
	}
	for name, bad := range cases {
		if _, err := s.Verify(bad, now); err == nil {
			t.Errorf("%s: Verify accepted %q", name, bad)
		}
	}

	// Another deployment's cookie, or one signed with a rotated-out key.
	other := newTestSessions(t, otherKey, nil, time.Hour)
	otherValue, err := other.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := s.Verify(otherValue, now); err == nil {
		t.Error("a session signed with another secret was accepted")
	}

	// The genuine value still works, so none of the above was a false pass.
	if _, err := s.Verify(value, now); err != nil {
		t.Errorf("the genuine value stopped verifying: %v", err)
	}
}

func TestVerify_MACCoversTheCookieName(t *testing.T) {
	// securecookie signs the name along with the value, so a value lifted from
	// a different cookie of ours could not be replayed as a session. Prove the
	// property holds by verifying under a different name.
	s := newTestSessions(t, testSecret, nil, time.Hour)
	now := time.Now()

	value, err := s.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var payload string
	if err := decodeUnderName("some_other_cookie", value, &payload, testSecret); err == nil {
		t.Error("the session value verified under a different cookie name")
	}
}

func TestSession_PreviousSecretVerifiesButDoesNotSign(t *testing.T) {
	now := time.Now()

	// Before rotation: a session signed with the old secret.
	old := newTestSessions(t, otherKey, nil, time.Hour)
	oldValue, err := old.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// After rotation: new secret signs, old secret still verifies, so the
	// operator is not signed out by a deploy.
	rotated := newTestSessions(t, testSecret, otherKey, time.Hour)
	if _, err := rotated.Verify(oldValue, now); err != nil {
		t.Errorf("a session from the previous secret was rejected: %v", err)
	}

	newValue, err := rotated.Issue(now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := rotated.Verify(newValue, now); err != nil {
		t.Errorf("a freshly issued session does not verify: %v", err)
	}
	// New sessions must be signed with the new secret only: once the previous
	// secret is dropped from the config, they have to keep working.
	current := newTestSessions(t, testSecret, nil, time.Hour)
	if _, err := current.Verify(newValue, now); err != nil {
		t.Errorf("a session issued after rotation was signed with the old secret: %v", err)
	}
	if _, err := old.Verify(newValue, now); err == nil {
		t.Error("a session issued after rotation still verifies under the old secret alone")
	}
}

func TestTTL(t *testing.T) {
	s := newTestSessions(t, testSecret, nil, 3*time.Hour)
	if s.TTL() != 3*time.Hour {
		t.Errorf("TTL() = %s, want 3h", s.TTL())
	}
}

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the hash contains the password")
	}

	if !CheckPassword(hash, "correct horse battery staple") {
		t.Error("the correct password was rejected")
	}
	for _, wrong := range []string{"", "correct horse battery stapl", "Correct Horse Battery Staple"} {
		if CheckPassword(hash, wrong) {
			t.Errorf("password %q was accepted", wrong)
		}
	}

	// A missing or malformed ADMIN_PASSWORD_HASH must fail closed, not open.
	for _, badHash := range []string{"", "not-a-bcrypt-hash", "$2a$10$tooshort"} {
		if CheckPassword(badHash, "anything") {
			t.Errorf("hash %q accepted a password", badHash)
		}
	}

	if _, err := HashPassword("", bcrypt.MinCost); err == nil {
		t.Error("HashPassword accepted an empty password")
	}
}

func TestHashPassword_IsSalted(t *testing.T) {
	a, err := HashPassword("same", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not doing its job")
	}
}

// decodeUnderName verifies a cookie value as if it had come from a differently
// named cookie, which is what the name-binding test needs and the package's own
// API deliberately does not expose.
func decodeUnderName(name, value string, dst *string, secret []byte) error {
	return securecookie.DecodeMulti(name, value, dst, securecookie.New(secret, nil))
}

func flipLast(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}
