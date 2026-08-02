// Package auth authenticates the single store operator.
//
// There is one admin, one password, and no sessions table: a session is a
// signed, self-describing cookie value, so verifying a request costs no
// database round trip and expiry needs no cleanup job. Adding a second admin,
// or needing to revoke a session immediately, is the documented point at which
// this should become a `sessions` table instead.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// CookieName is the admin session cookie. It is scoped to /admin by the
// handler that sets it, so it is never sent with storefront requests.
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
// that was never valid, so a handler can say "signed out" rather than
// implying tampering.
var ErrExpired = errors.New("auth: session expired")

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

// IssueSession returns a cookie value that proves the holder authenticated and
// says when that stops being true: base64(expiry unix seconds) + "." +
// base64(HMAC-SHA256 of the expiry).
//
// The expiry travels inside the signed value rather than relying on the
// cookie's own Expires attribute, which a client controls and can simply not
// honour.
func IssueSession(secret []byte, ttl time.Duration, now time.Time) string {
	payload := strconv.FormatInt(now.Add(ttl).Unix(), 10)
	return encode(payload) + "." + encode(string(sign(secret, payload)))
}

// VerifySession checks a cookie value's signature and expiry, returning when it
// expires so a caller can log or refresh it.
func VerifySession(secret []byte, value string, now time.Time) (time.Time, error) {
	rawPayload, rawMAC, found := cut(value)
	if !found {
		return time.Time{}, errors.New("auth: malformed session value")
	}

	payload, err := base64.RawURLEncoding.DecodeString(rawPayload)
	if err != nil {
		return time.Time{}, errors.New("auth: malformed session payload")
	}
	mac, err := base64.RawURLEncoding.DecodeString(rawMAC)
	if err != nil {
		return time.Time{}, errors.New("auth: malformed session signature")
	}

	// Signature first, always, and in constant time: nothing inside the value
	// is trustworthy until it has been shown to be ours, and a comparison that
	// leaks how many bytes matched leaks the signature itself.
	if subtle.ConstantTimeCompare(mac, sign(secret, string(payload))) != 1 {
		return time.Time{}, errors.New("auth: session signature does not match")
	}

	unix, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil {
		return time.Time{}, errors.New("auth: malformed session expiry")
	}
	expiry := time.Unix(unix, 0)
	if now.After(expiry) {
		return expiry, ErrExpired
	}
	return expiry, nil
}

func sign(secret []byte, payload string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func encode(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// cut splits on the last separator, so a payload that somehow contains one
// cannot shift the boundary and be verified against the wrong bytes.
func cut(value string) (payload, mac string, found bool) {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == '.' {
			return value[:i], value[i+1:], true
		}
	}
	return "", "", false
}
