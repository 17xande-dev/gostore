// Package auth authenticates the single store operator.
//
// There is one admin, one password, and no sessions table: a session is a
// signed, self-describing cookie value, so verifying a request costs no
// database round trip and expiry needs no cleanup job. Adding a second admin,
// or needing to revoke one session immediately, is the documented point at
// which this should become a `sessions` table instead.
//
// The signing and encoding are gorilla/securecookie's rather than ours. It is
// the standard implementation of exactly this, its MAC covers the cookie's name
// as well as its value, and it supports verifying against a previous key so a
// secret can be rotated without signing the operator out. Hand-rolled crypto in
// a project other people copy as an example is a worse trade than one small,
// well-reviewed dependency.
package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gorilla/securecookie"
	"golang.org/x/crypto/bcrypt"
)

// CookieName is the admin session cookie. It is scoped to /admin by the
// handler that sets it, so it is never sent with storefront requests. It is
// also part of the signed payload, so a value lifted from another cookie will
// not verify here.
const CookieName = "admin_session"

// HashCost is the bcrypt cost for admin passwords. Higher than bcrypt's default
// because this hash guards everything and is verified at most a few times a
// day, so the cost is paid by an attacker far more often than by the operator.
const HashCost = 12

// MinSecretLen is the shortest session secret accepted. HMAC-SHA256 gains
// nothing from a key longer than its 32-byte block, and loses meaningfully to
// one shorter.
const MinSecretLen = 32

// ErrExpired distinguishes a session that was genuine but has run out from one
// that was never valid, so a caller can log the second and ignore the first.
var ErrExpired = errors.New("auth: session expired")

// Sessions issues and verifies admin session cookies.
//
// It holds one codec per accepted key: the first signs, and any of them may
// verify. That is the whole mechanism behind rotating SESSION_SECRET without
// signing the operator out — move the old value to SESSION_SECRET_PREVIOUS,
// deploy, and remove it once the old sessions have expired.
type Sessions struct {
	codecs []securecookie.Codec
	ttl    time.Duration
}

// NewSessions builds the session codecs. previous may be nil.
func NewSessions(secret, previous []byte, ttl time.Duration) (*Sessions, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("auth: session secret is %d bytes, want at least %d", len(secret), MinSecretLen)
	}
	if previous != nil && len(previous) < MinSecretLen {
		return nil, fmt.Errorf("auth: previous session secret is %d bytes, want at least %d", len(previous), MinSecretLen)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("auth: session ttl must be positive, got %s", ttl)
	}

	// nil block keys: the payload is an expiry timestamp, which needs to be
	// unforgeable, not secret. Signing without encrypting keeps the cookie
	// readable in a debugger and one fewer key to manage.
	keyPairs := [][]byte{secret, nil}
	if previous != nil {
		keyPairs = append(keyPairs, previous, nil)
	}

	codecs := securecookie.CodecsFromPairs(keyPairs...)
	for _, c := range codecs {
		if sc, ok := c.(*securecookie.SecureCookie); ok {
			// securecookie's own timestamp check, in addition to the expiry we
			// sign into the payload below. Two independent bounds on the same
			// thing, because a session that outlives its welcome is the failure
			// that matters here.
			sc.MaxAge(int(ttl.Seconds()))
		}
	}
	return &Sessions{codecs: codecs, ttl: ttl}, nil
}

// Issue returns a cookie value that proves the holder authenticated and says
// when that stops being true.
//
// The expiry travels inside the signed payload rather than relying only on the
// cookie's Expires attribute, which a client controls and can simply not
// honour.
func (s *Sessions) Issue(now time.Time) (string, error) {
	payload := strconv.FormatInt(now.Add(s.ttl).Unix(), 10)
	value, err := securecookie.EncodeMulti(CookieName, payload, s.codecs...)
	if err != nil {
		return "", fmt.Errorf("auth: issue session: %w", err)
	}
	return value, nil
}

// TTL is how long an issued session lasts, for setting the cookie's own
// lifetime to match.
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Verify checks a cookie value's signature and expiry, returning when it
// expires so a caller can log or refresh it.
func (s *Sessions) Verify(value string, now time.Time) (time.Time, error) {
	var payload string
	if err := securecookie.DecodeMulti(CookieName, value, &payload, s.codecs...); err != nil {
		// securecookie rejects a stale timestamp as a decode error, which is
		// indistinguishable from tampering without matching on its message. The
		// signed expiry below is what tells the two apart, so treat anything
		// that fails to decode as not genuine and let the caller log it.
		return time.Time{}, fmt.Errorf("auth: session does not verify: %w", err)
	}

	unix, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return time.Time{}, errors.New("auth: malformed session expiry")
	}
	expiry := time.Unix(unix, 0)
	if now.After(expiry) {
		return expiry, ErrExpired
	}
	return expiry, nil
}

// HashPassword returns a bcrypt hash for storing in ADMIN_PASSWORD_HASH. Tests
// pass a lower cost; everything else should pass HashCost.
func HashPassword(password string, cost int) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches the stored bcrypt hash.
// bcrypt's comparison is constant time in the parts that matter.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
