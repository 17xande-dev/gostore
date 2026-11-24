// Package auth holds the administrator accounts, their roles, and the passwords
// and sessions that get them in.
//
// A session is a row in admin_sessions and a cookie carrying 32 random bytes.
// The row is what makes it revocable, which is the whole reason the signed,
// self-describing cookie this package started with is gone: a cookie that
// verifies on its own cannot be taken back, and "end every session belonging to
// this account" is what a password change, a disable and a demotion all need.
//
// Only sha256(token) is stored. There is nothing else to store — the token
// carries no claims — so a leaked database backup hands over no live session,
// and the plain token exists only in the cookie the browser holds.
//
// The pieces: model.go has the roles, permissions and the User row; store.go has
// the accounts and the one-shot setup token; password.go hashes passwords; this
// file issues and verifies sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/17xande-dev/websec/cookie"
	websession "github.com/17xande-dev/websec/session"
	"github.com/jackc/pgx/v5"
)

// CookieName is the admin session cookie's bare name; the name on the wire gets
// a prefix from websec/cookie (__Secure- here, because of the /admin path).
const CookieName = "admin_session"

// NewSessionCookie builds the admin session cookie policy. Path is /admin, so
// the cookie is never sent with a storefront request and cannot leak into the
// embeddable, deliberately cookie-free catalog fragments. insecure is true only
// for a plain-HTTP deployment (BASE_URL not https), where a prefix and Secure
// would make the browser drop the cookie.
func NewSessionCookie(insecure bool) (*cookie.Cookie, error) {
	return cookie.New(cookie.Config{Name: CookieName, Path: "/admin", Insecure: insecure})
}

// SessionIdle is how long a session may go unused before it ends. The absolute
// limit is SESSION_TTL_HOURS, passed to IssueSession and stored as the row's
// expires_at.
const SessionIdle = 2 * time.Hour

// LifetimeFor is the session policy for an absolute limit: SessionIdle, but never
// longer than the absolute limit itself (a SESSION_TTL_HOURS of 1 leaves nothing
// for an idle limit to do).
func LifetimeFor(absolute time.Duration) websession.Lifetime {
	return websession.Lifetime{Idle: min(SessionIdle, absolute), Absolute: absolute}
}

// TokenBytes is how much randomness a session token carries. 32 bytes is the
// size below which nothing is gained by arguing about it and above which nothing
// is either: 256 bits of uniform randomness is not going to be guessed.
const TokenBytes = 32

// NewToken returns a fresh session token, URL-safe and unpadded so it needs no
// escaping in a cookie value.
//
// It is exported because the setup flow mints a token of its own with the same
// properties, and because a caller assembling their own bootstrap should not have
// to reimplement "32 bytes from crypto/rand".
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueSession records a new session for a user and returns the token to put in
// the cookie, alongside the row it created.
//
// The token is returned once and never again: the store keeps only its hash, so
// nothing can reconstruct it afterwards — including this package.
func (s *Store) IssueSession(ctx context.Context, userID string, ttl time.Duration) (string, Session, error) {
	if ttl <= 0 {
		return "", Session{}, fmt.Errorf("auth: session ttl must be positive, got %s", ttl)
	}
	token, err := NewToken()
	if err != nil {
		return "", Session{}, err
	}

	expires := time.Now().Add(ttl)
	err = s.q.CreateAdminSession(ctx, gen.CreateAdminSessionParams{
		TokenHash: hashToken(token),
		UserID:    userID,
		ExpiresAt: expires,
	})
	if err != nil {
		return "", Session{}, translate(fmt.Errorf("auth: create session: %w", err))
	}
	return token, Session{UserID: userID, CreatedAt: time.Now(), ExpiresAt: expires}, nil
}

// Session looks a token up, returning the session and the account it belongs to.
//
// ErrNotFound is the answer for a token that was never valid, has expired, or has
// been revoked — one answer for the three, because a request holding any of them
// is equally not signed in and telling them apart would only invite a caller to
// treat one of them as nearly authenticated.
//
// The absolute limit is enforced in the query's own predicate rather than by
// comparing ExpiresAt here, so a caller cannot forget to; the idle limit is
// judged here, and a live session has its last-seen time advanced (at most once
// a minute); the periodic sweep that deletes
// expired rows is housekeeping and explicitly not what makes this correct.
func (s *Store) Session(ctx context.Context, token string) (Session, User, error) {
	// A short-circuit, not an optimisation: without it an empty cookie would
	// hash to a perfectly well-formed lookup for sha256(""), which is a value an
	// attacker knows and could in principle insert against.
	if token == "" {
		return Session{}, User{}, ErrNotFound
	}

	row, err := s.q.GetAdminSession(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, User{}, ErrNotFound
		}
		return Session{}, User{}, translate(fmt.Errorf("auth: session: %w", err))
	}
	sess := Session{
		UserID:    row.AdminSession.UserID,
		CreatedAt: row.AdminSession.CreatedAt,
		ExpiresAt: row.AdminSession.ExpiresAt,
	}
	// The absolute limit is the one this row was issued with (its stored expiry),
	// not today's SESSION_TTL_HOURS; the idle limit is the policy's. Both are
	// judged at the database's clock, the one the expiry predicate used.
	lifetime := LifetimeFor(sess.ExpiresAt.Sub(sess.CreatedAt))
	now := row.DbNow
	if lifetime.Expired(sess.CreatedAt, row.AdminSession.LastSeen, now) {
		return Session{}, User{}, ErrNotFound
	}
	if websession.NeedsTouch(row.AdminSession.LastSeen, now) {
		// The statement repeats the throttle, so concurrent requests write once.
		if _, err := s.q.TouchAdminSession(ctx, gen.TouchAdminSessionParams{
			Now: now, TokenHash: hashToken(token), StaleBefore: now.Add(-websession.TouchEvery),
		}); err != nil {
			return Session{}, User{}, translate(fmt.Errorf("auth: touch session: %w", err))
		}
	}
	return sess, userOf(row.AdminUser), nil
}

// DeleteSession ends one session, by the token that holds it. Signing out is this
// and clearing the cookie, in that order of importance: the cookie is a copy, the
// row is the session.
//
// Deleting a token that is not there is not an error. A double-submitted sign-out
// and a sign-out with an already-expired cookie both arrive here, and neither is
// a problem worth reporting to somebody who has just left.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.q.DeleteAdminSession(ctx, hashToken(token)); err != nil {
		return translate(fmt.Errorf("auth: delete session: %w", err))
	}
	return nil
}

// hashToken is sha256 and deliberately not a KDF. A session or setup token is 32
// bytes of uniform randomness with nothing to guess, so a slow hash would buy no
// resistance to an offline attack that cannot succeed anyway — and would be paid
// on every request that carries one.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
