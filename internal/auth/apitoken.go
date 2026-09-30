package auth

// API tokens are an administrator's credential for a program acting as them —
// the MCP endpoint's bearer token. They are sessions in every way that matters:
// 32 random bytes, only the sha256 stored, revocable because the row is the
// token, expiry enforced in the lookup. What differs is lifetime (days, not
// hours), that an account may hold several with names it chose, and that each
// is shown exactly once, when it is made.
//
// A token carries no role or scope. It is its account acting, so what it may do
// is whatever that account's role allows at the moment of the request — which is
// also why changing the role, the password or the enabled state deletes every
// token the account holds, in the same transaction that ends its sessions.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/jackc/pgx/v5"
)

// APITokenPrefix starts every API token, so one pasted into a chat, a commit or
// a log is recognisable for what it is — by a person, or a secret scanner.
const APITokenPrefix = "gst_"

// MaxAPITokenTTL is the longest an API token may live. A credential that never
// expires is one nobody remembers to revoke.
const MaxAPITokenTTL = 366 * 24 * time.Hour

// APIToken is one token's record. It never holds the token itself: that exists
// only in what IssueAPIToken returned, once.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  time.Time
}

// IssueAPIToken creates a token for an account and returns it with its record.
// The token is returned this once; only its hash is kept.
func (s *Store) IssueAPIToken(ctx context.Context, userID, name string, ttl time.Duration) (string, APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", APIToken{}, ErrInvalidTokenName
	}
	if ttl <= 0 || ttl > MaxAPITokenTTL {
		return "", APIToken{}, fmt.Errorf("auth: api token ttl must be between 0 and %s, got %s", MaxAPITokenTTL, ttl)
	}
	raw, err := NewToken()
	if err != nil {
		return "", APIToken{}, err
	}
	token := APITokenPrefix + raw
	row, err := s.q.CreateAdminAPIToken(ctx, gen.CreateAdminAPITokenParams{
		UserID:    userID,
		Name:      name,
		TokenHash: hashToken(token),
		ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		return "", APIToken{}, translate(fmt.Errorf("auth: create api token: %w", err))
	}
	return token, apiTokenOf(row), nil
}

// ErrInvalidTokenName is a blank or overlong token name.
var ErrInvalidTokenName = errors.New("auth: a token needs a name of 1 to 100 characters")

// APITokenUser looks a bearer token up and returns the account it acts as.
//
// ErrNotFound is the one answer for a token that is malformed, unknown, expired,
// revoked, or whose account is disabled or must change its password — for the
// reason Session gives one answer: none of them is nearly authenticated.
func (s *Store) APITokenUser(ctx context.Context, token string) (APIToken, User, error) {
	// The prefix check is a short-circuit for the same reason Session's empty
	// check is: a lookup is never made for a value nobody could have been issued.
	if !strings.HasPrefix(token, APITokenPrefix) || len(token) == len(APITokenPrefix) {
		return APIToken{}, User{}, ErrNotFound
	}
	row, err := s.q.GetAdminAPIToken(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIToken{}, User{}, ErrNotFound
		}
		return APIToken{}, User{}, translate(fmt.Errorf("auth: api token: %w", err))
	}
	return apiTokenOf(row.AdminAPIToken), userOf(row.AdminUser), nil
}

// TouchAPIToken records that a token was used. The query writes at most once a
// minute per token, so calling it on every request is cheap.
func (s *Store) TouchAPIToken(ctx context.Context, id string) error {
	if err := s.q.TouchAdminAPIToken(ctx, id); err != nil {
		return translate(fmt.Errorf("auth: touch api token: %w", err))
	}
	return nil
}

// APITokens lists an account's tokens, newest first.
func (s *Store) APITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.q.ListAdminAPITokensForUser(ctx, userID)
	if err != nil {
		return nil, translate(fmt.Errorf("auth: list api tokens: %w", err))
	}
	out := make([]APIToken, len(rows))
	for i, r := range rows {
		out[i] = apiTokenOf(r)
	}
	return out, nil
}

// RevokeAPIToken deletes one of an account's tokens. The account is part of the
// match, so an id belonging to somebody else is ErrNotFound rather than a
// revocation.
func (s *Store) RevokeAPIToken(ctx context.Context, userID, id string) error {
	n, err := s.q.DeleteAdminAPIToken(ctx, gen.DeleteAdminAPITokenParams{ID: id, UserID: userID})
	if err != nil {
		return translate(fmt.Errorf("auth: revoke api token: %w", err))
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredAPITokens is housekeeping, beside the expired-session sweep:
// expiry is enforced on read, so this only keeps the table bounded.
func (s *Store) DeleteExpiredAPITokens(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredAdminAPITokens(ctx)
	if err != nil {
		return 0, translate(fmt.Errorf("auth: delete expired api tokens: %w", err))
	}
	return n, nil
}

// revokeAccess ends everything that lets an account in without its password:
// its sessions and its API tokens. It runs inside the caller's transaction, so
// there is no moment at which a changed password, a demotion or a disable has
// happened but a credential issued before it still works.
func revokeAccess(ctx context.Context, q *gen.Queries, userID string) error {
	if _, err := q.DeleteAdminSessionsForUser(ctx, userID); err != nil {
		return translate(fmt.Errorf("auth: end sessions: %w", err))
	}
	if _, err := q.DeleteAdminAPITokensForUser(ctx, userID); err != nil {
		return translate(fmt.Errorf("auth: revoke api tokens: %w", err))
	}
	return nil
}

func apiTokenOf(r gen.AdminAPIToken) APIToken {
	return APIToken{
		ID:         r.ID,
		UserID:     r.UserID,
		Name:       r.Name,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: r.LastUsedAt,
		ExpiresAt:  r.ExpiresAt,
	}
}
