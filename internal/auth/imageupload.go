package auth

// Image upload URLs are the MCP endpoint's way of taking a product image without
// the bytes passing through a tool call: a tool issues one, and the client sends
// the file to it over plain HTTP. Each is a single-use capability for one
// product, valid for minutes, and carried in the URL's path — like a buyer's
// download link, because the program sending the file may be a shell command
// with nothing but the URL to go on.
//
// It is issued against the API token that asked for it, and is only as good as
// that token: revoking the token, or anything that deletes an account's tokens,
// deletes the account's uploads in the same statement (by foreign key), and the
// lookup re-checks the token and the account as the bearer lookup does.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/jackc/pgx/v5"
)

// ImageUploadPrefix starts every upload token, so one seen in a log or a shell
// history is recognisable as an upload rather than an API token.
const ImageUploadPrefix = "gsu_"

// ImageUploadTTL is how long an upload URL works. Long enough to fetch or find
// a file and send it; short enough that one left in a shell history is dead.
const ImageUploadTTL = 15 * time.Minute

// ImageUpload is what an upload URL grants, and on whose behalf.
type ImageUpload struct {
	ProductID  string
	APITokenID string
	UserID     string
	// Role is the account's role now, not when the upload was issued, so the
	// caller can check the permission an upload needs at the moment it is used.
	Role Role
}

// IssueImageUpload creates an upload URL's token for one product, on behalf of
// an API token, and returns it with its expiry. Only its hash is kept.
func (s *Store) IssueImageUpload(ctx context.Context, apiTokenID, productID string) (string, time.Time, error) {
	raw, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	token := ImageUploadPrefix + raw
	expires := time.Now().Add(ImageUploadTTL)
	if err := s.q.CreateAdminImageUpload(ctx, gen.CreateAdminImageUploadParams{
		TokenHash:  hashToken(token),
		APITokenID: apiTokenID,
		ProductID:  productID,
		ExpiresAt:  expires,
	}); err != nil {
		return "", time.Time{}, translate(fmt.Errorf("auth: create image upload: %w", err))
	}
	return token, expires, nil
}

// ImageUploadFor looks an upload token up without spending it, so a request can
// be refused before its body is read. ErrNotFound is the one answer for a token
// that is malformed, unknown, expired, spent, or whose API token or account is
// no longer usable.
func (s *Store) ImageUploadFor(ctx context.Context, token string) (ImageUpload, error) {
	if !validUploadToken(token) {
		return ImageUpload{}, ErrNotFound
	}
	row, err := s.q.GetAdminImageUpload(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ImageUpload{}, ErrNotFound
		}
		return ImageUpload{}, translate(fmt.Errorf("auth: image upload: %w", err))
	}
	return ImageUpload{ProductID: row.ProductID, APITokenID: row.APITokenID, UserID: row.UserID, Role: Role(row.Role)}, nil
}

// SpendImageUpload uses an upload token up, returning what it granted. Of two
// requests racing with one token, exactly one gets it; the other, like any
// unusable token, gets ErrNotFound.
func (s *Store) SpendImageUpload(ctx context.Context, token string) (ImageUpload, error) {
	if !validUploadToken(token) {
		return ImageUpload{}, ErrNotFound
	}
	row, err := s.q.ConsumeAdminImageUpload(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ImageUpload{}, ErrNotFound
		}
		return ImageUpload{}, translate(fmt.Errorf("auth: spend image upload: %w", err))
	}
	return ImageUpload{ProductID: row.ProductID, APITokenID: row.APITokenID, UserID: row.UserID, Role: Role(row.Role)}, nil
}

// DeleteExpiredImageUploads is housekeeping, beside the session and API token
// sweeps.
func (s *Store) DeleteExpiredImageUploads(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredAdminImageUploads(ctx)
	if err != nil {
		return 0, translate(fmt.Errorf("auth: delete expired image uploads: %w", err))
	}
	return n, nil
}

// validUploadToken short-circuits a value nobody could have been issued, as
// APITokenUser does, so it never costs a lookup.
func validUploadToken(token string) bool {
	return strings.HasPrefix(token, ImageUploadPrefix) && len(token) > len(ImageUploadPrefix)
}
