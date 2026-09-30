package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAPIToken_RoundTripStoresOnlyTheHash(t *testing.T) {
	s, pool, ctx := newStore(t)
	u := mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)

	token, rec, err := s.IssueAPIToken(ctx, u.ID, "  laptop  ", 90*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueAPIToken: %v", err)
	}
	if !strings.HasPrefix(token, APITokenPrefix) || rec.Name != "laptop" {
		t.Fatalf("token %q, record %+v", token, rec)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM admin_api_tokens`).Scan(&stored); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if strings.Contains(string(stored), token) {
		t.Fatal("the plain token is in the row")
	}

	got, user, err := s.APITokenUser(ctx, token)
	if err != nil {
		t.Fatalf("APITokenUser: %v", err)
	}
	if got.ID != rec.ID || user.ID != u.ID || user.Role != RoleOwner {
		t.Fatalf("got %+v as %+v", got, user)
	}
}

func TestAPITokenUser_RefusesWhatCannotAuthenticate(t *testing.T) {
	s, pool, ctx := newStore(t)
	u := mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)
	other := mustCreate(t, s, ctx, "viewer@example.com", "correct horse battery", RoleViewer)

	expired, _, err := s.IssueAPIToken(ctx, u.ID, "old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_api_tokens SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	mustChange, _, err := s.IssueAPIToken(ctx, other.ID, "x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET must_change_password = true WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"empty":         "",
		"prefix only":   APITokenPrefix,
		"no prefix":     "abcdef",
		"unknown":       APITokenPrefix + "not-a-real-token",
		"expired":       expired,
		"must change":   mustChange,
		"session-shape": strings.TrimPrefix(expired, APITokenPrefix),
	} {
		if _, _, err := s.APITokenUser(ctx, token); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestIssueAPIToken_RefusesBadNameAndLifetime(t *testing.T) {
	s, _, ctx := newStore(t)
	u := mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)

	if _, _, err := s.IssueAPIToken(ctx, u.ID, "   ", time.Hour); !errors.Is(err, ErrInvalidTokenName) {
		t.Errorf("blank name: %v", err)
	}
	if _, _, err := s.IssueAPIToken(ctx, u.ID, strings.Repeat("n", 101), time.Hour); !errors.Is(err, ErrInvalidTokenName) {
		t.Errorf("long name: %v", err)
	}
	if _, _, err := s.IssueAPIToken(ctx, u.ID, "ok", 0); err == nil {
		t.Error("zero lifetime accepted")
	}
	if _, _, err := s.IssueAPIToken(ctx, u.ID, "ok", MaxAPITokenTTL+time.Hour); err == nil {
		t.Error("lifetime past the maximum accepted")
	}
}

// A token is its account acting, so everything that ends the account's sessions
// ends its tokens — in the same transaction.
func TestAPITokens_RevokedWithTheAccountsAccess(t *testing.T) {
	cases := map[string]func(s *Store, u User) error{
		"password change": func(s *Store, u User) error {
			return s.SetPassword(t.Context(), u.ID, hash(t, "a different password entirely"), false)
		},
		"disable": func(s *Store, u User) error { return s.SetDisabled(t.Context(), u.ID, true) },
		"role change": func(s *Store, u User) error {
			return s.SetRole(t.Context(), u.ID, RoleViewer)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, ctx := newStore(t)
			mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)
			u := mustCreate(t, s, ctx, "manager@example.com", "correct horse battery", RoleManager)
			token, _, err := s.IssueAPIToken(ctx, u.ID, "laptop", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := change(s, u); err != nil {
				t.Fatalf("change: %v", err)
			}
			if _, _, err := s.APITokenUser(ctx, token); !errors.Is(err, ErrNotFound) {
				t.Fatalf("token still works after %s: %v", name, err)
			}
		})
	}
}

func TestSetRoleToTheSameRoleKeepsAPITokens(t *testing.T) {
	s, _, ctx := newStore(t)
	u := mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)
	token, _, err := s.IssueAPIToken(ctx, u.ID, "laptop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, u.ID, RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.APITokenUser(ctx, token); err != nil {
		t.Fatalf("an unchanged role revoked the token: %v", err)
	}
}

func TestRevokeAPIToken_OnlyYourOwn(t *testing.T) {
	s, _, ctx := newStore(t)
	a := mustCreate(t, s, ctx, "a@example.com", "correct horse battery", RoleOwner)
	b := mustCreate(t, s, ctx, "b@example.com", "correct horse battery", RoleOwner)
	token, rec, err := s.IssueAPIToken(ctx, a.ID, "laptop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RevokeAPIToken(ctx, b.ID, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account revoked it: %v", err)
	}
	if _, _, err := s.APITokenUser(ctx, token); err != nil {
		t.Fatalf("token gone after a refused revoke: %v", err)
	}
	if err := s.RevokeAPIToken(ctx, a.ID, rec.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, _, err := s.APITokenUser(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token still works: %v", err)
	}
	list, err := s.APITokens(ctx, a.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("APITokens = %v, %v", list, err)
	}
}

func TestTouchAndSweepAPITokens(t *testing.T) {
	s, pool, ctx := newStore(t)
	u := mustCreate(t, s, ctx, "owner@example.com", "correct horse battery", RoleOwner)
	_, live, err := s.IssueAPIToken(ctx, u.ID, "live", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IssueAPIToken(ctx, u.ID, "dead", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_api_tokens SET expires_at = now() - interval '1 second' WHERE name = 'dead'`); err != nil {
		t.Fatal(err)
	}

	if err := s.TouchAPIToken(ctx, live.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteExpiredAPITokens(ctx)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredAPITokens = %d, %v", n, err)
	}
	list, err := s.APITokens(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("after sweep: %+v, %v", list, err)
	}
}
