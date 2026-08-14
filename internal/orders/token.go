package orders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/17xande-dev/gostore/internal/db/gen"
)

// tokenBytes is how much entropy a download token carries.
//
// Thirty-two bytes, which is the same order as the cart token and for a stronger
// reason. A cart token names an anonymous basket and grants nothing worth taking;
// this one is the *only* credential standing between the internet and files
// somebody paid for. There is no account, no password and no second factor, so
// the token's unguessability is the entire access-control story and it should not
// be the cheap part.
const tokenBytes = 32

// NewToken returns a fresh download token and the hash to store for it.
//
// The plaintext is returned once, to go into the confirmation email, and is
// unrecoverable afterwards. That is the point: what sits in the database is a
// SHA-256 digest, so a dump of the entitlements table is a list of hashes rather
// than a set of working download links.
//
// SHA-256 rather than a password hash, deliberately. bcrypt and argon2 are slow on
// purpose because a password is short, human-chosen and guessable; this token is
// 32 bytes of crypto/rand, so there is nothing to brute-force and the only
// property needed is that the digest cannot be reversed. A slow hash here would
// buy nothing and would put a deliberate delay on every download click.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("orders: generate download token: %w", err)
	}
	// URL-safe and unpadded, because this goes in a path segment and an "=" there
	// is legal but ugly in an email a person reads.
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is how a token from a URL is turned into the value to look up. It is
// exported because the download handler does the same computation the mint did.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// mintEntitlements issues one entitlement per digital line of a paid order.
//
// One per line rather than one per unit: buying quantity 2 of a download is
// buying the same files twice, and issuing two links for identical bytes would
// only give the shop two things to revoke and the buyer a choice to make.
//
// Called inside MarkPaid's transaction, so entitlements and the paid status
// commit together.
func mintEntitlements(ctx context.Context, q *gen.Queries, orderID string) ([]Grant, error) {
	lines, err := q.ListDigitalOrderItems(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("orders: read digital items: %w", err)
	}
	if len(lines) == 0 {
		return nil, nil
	}

	grants := make([]Grant, 0, len(lines))
	for _, l := range lines {
		token, hash, err := NewToken()
		if err != nil {
			return nil, err
		}
		row, err := q.CreateEntitlement(ctx, gen.CreateEntitlementParams{
			OrderID:     orderID,
			OrderItemID: l.ID,
			VariantID:   l.VariantID,
			TokenHash:   hash,
		})
		if err != nil {
			return nil, fmt.Errorf("orders: create entitlement: %w", err)
		}
		grants = append(grants, Grant{
			EntitlementID: row.ID,
			OrderItemID:   l.ID,
			VariantID:     l.VariantID,
			Title:         l.Title,
			VariantLabel:  l.VariantLabel,
			Token:         token,
		})
	}
	return grants, nil
}
