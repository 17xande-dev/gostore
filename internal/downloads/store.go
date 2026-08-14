// Package downloads is the buyer's side of a digital product: turning a token
// from a URL into permission to read one file, recording that it happened, and
// reporting the totals back to the shop owner.
//
// # Why the store records the download
//
// The obvious shortcut is to ask the bucket. It does not work, and not because of
// an API gap that might be filled later: neither Google Cloud Storage nor R2
// exposes per-object read counts at all, and — the part that would still be true
// if they did — a presigned URL is *anonymous* to the bucket. It has no idea which
// buyer, which order or which entitlement, and that mapping exists only here. So
// the click is recorded on the path the bytes travel, where it cannot be skipped.
package downloads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/db/gen"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound means the token names no entitlement, or the file is not one
	// this entitlement grants. Both are the same answer on purpose — see Authorize.
	ErrNotFound = errors.New("downloads: not found")

	// ErrRevoked means the entitlement existed and has been withdrawn.
	ErrRevoked = errors.New("downloads: entitlement has been revoked")

	// ErrNotPaid means the order exists but has not been paid. It should be
	// unreachable, because entitlements are only minted inside the transaction
	// that records payment, but it is checked rather than assumed: this is the
	// gate on somebody else's money.
	ErrNotPaid = errors.New("downloads: order is not paid")
)

// Entitlement is one buyer's right to one purchased digital line.
type Entitlement struct {
	ID           string
	OrderID      string
	VariantID    string
	ProductID    string
	ProductTitle string
	ProductSlug  string
	Customer     string
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

// Revoked reports whether this entitlement has been withdrawn.
func (e Entitlement) Revoked() bool { return e.RevokedAt != nil }

// Store is the downloads' persistence.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	cat  *catalog.Store
}

func NewStore(pool *pgxpool.Pool, cat *catalog.Store) *Store {
	return &Store{pool: pool, q: gen.New(pool), cat: cat}
}

// Lookup resolves a token from a URL to the entitlement it names, refusing a
// revoked one.
//
// The token is hashed before the query, so the value in the database is never the
// credential. A token that names nothing and one whose hash does not exist are
// indistinguishable here, which is the intent: answering differently would confirm
// that a guessed token was once valid.
func (s *Store) Lookup(ctx context.Context, token string) (Entitlement, error) {
	if token == "" {
		return Entitlement{}, ErrNotFound
	}
	row, err := s.q.GetEntitlementByTokenHash(ctx, orders.HashToken(token))
	if err != nil {
		return Entitlement{}, translate(fmt.Errorf("downloads: lookup: %w", err))
	}

	e := Entitlement{
		ID:           row.ID,
		OrderID:      row.OrderID,
		VariantID:    row.VariantID,
		ProductID:    row.ProductID,
		ProductTitle: row.ProductTitle,
		ProductSlug:  row.ProductSlug,
		Customer:     row.CustomerEmail,
		RevokedAt:    row.RevokedAt,
		CreatedAt:    row.CreatedAt,
	}
	if e.Revoked() {
		// Distinguishable from "no such token", and deliberately so: the holder of a
		// revoked link is somebody who did once buy this, and telling them it was
		// withdrawn is more use than a blank 404 that reads as a broken site. It
		// leaks only that a token they already have was once valid.
		return e, ErrRevoked
	}
	if orders.Status(row.OrderStatus) != orders.StatusPaid {
		return e, ErrNotPaid
	}
	return e, nil
}

// Files lists what an entitlement grants, which is the files linked to the variant
// that was bought.
func (s *Store) Files(ctx context.Context, e Entitlement) ([]catalog.File, error) {
	return s.cat.VariantFiles(ctx, e.VariantID)
}

// Access describes one authorised download, and is what a handler needs to serve
// it.
type Access struct {
	File catalog.File
}

// Authorize checks that a file is one this entitlement grants and records the
// download, in one transaction so a click cannot be served without being counted.
//
// The membership check is a join through variant_files rather than a comparison
// in Go, which is what stops a buyer of the audio variant reading a file id off
// another page and fetching the video. A file that exists but is not in this
// variant's set returns ErrNotFound, the same as one that does not exist: from
// outside they are the same answer, and distinguishing them would confirm the id
// names something.
//
// What is recorded is the *authorisation*, not the completed transfer. A
// connection that drops at 80% is one row. Counting real bytes would mean proxying
// every file through this process, which is the cost the presigned redirect exists
// to avoid, and the admin says so rather than implying otherwise.
func (s *Store) Authorize(ctx context.Context, e Entitlement, fileID int64, ip, userAgent string) (Access, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Access{}, fmt.Errorf("downloads: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	row, err := q.GetEntitlementFile(ctx, gen.GetEntitlementFileParams{
		VariantID: e.VariantID,
		ID:        fileID,
	})
	if err != nil {
		return Access{}, translate(fmt.Errorf("downloads: file: %w", err))
	}

	if err := q.RecordDownload(ctx, gen.RecordDownloadParams{
		EntitlementID: e.ID,
		FileID:        row.ID,
		Ip:            ip,
		UserAgent:     truncate(userAgent, 500),
	}); err != nil {
		return Access{}, fmt.Errorf("downloads: record: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Access{}, fmt.Errorf("downloads: commit: %w", err)
	}

	return Access{File: catalog.File{
		ID:               row.ID,
		ProductID:        row.ProductID,
		Position:         row.Position,
		Title:            row.Title,
		ObjectKey:        row.ObjectKey,
		OriginalFilename: row.OriginalFilename,
		ContentType:      row.ContentType,
		SizeBytes:        row.SizeBytes,
		CreatedAt:        row.CreatedAt,
	}}, nil
}

// OrderEntitlement is one row of the admin's revoke list.
type OrderEntitlement struct {
	ID            string
	ProductTitle  string
	VariantLabel  string
	DownloadCount int64
	RevokedAt     *time.Time
	CreatedAt     time.Time
}

func (e OrderEntitlement) Revoked() bool { return e.RevokedAt != nil }

// ForOrder lists an order's entitlements with how many times each has been used.
// The count is there because an operator deciding whether to revoke wants to know
// whether the file has already been taken — revoking after the fact closes a door
// somebody has walked through.
func (s *Store) ForOrder(ctx context.Context, orderID string) ([]OrderEntitlement, error) {
	rows, err := s.q.ListOrderEntitlements(ctx, orderID)
	if err != nil {
		return nil, translate(fmt.Errorf("downloads: order entitlements: %w", err))
	}
	out := make([]OrderEntitlement, 0, len(rows))
	for _, r := range rows {
		out = append(out, OrderEntitlement{
			ID:            r.ID,
			ProductTitle:  r.ProductTitle,
			VariantLabel:  r.VariantLabel,
			DownloadCount: r.DownloadCount,
			RevokedAt:     r.RevokedAt,
			CreatedAt:     r.CreatedAt,
		})
	}
	return out, nil
}

// Revoke withdraws one entitlement. The order id is part of the WHERE clause, so
// an entitlement id from another order's page revokes nothing rather than cutting
// off an unrelated buyer.
//
// Revoking is not destructive: the row stays, the download history stays, and
// Restore puts it back. A shop owner acting on a suspicion should be able to
// change their mind.
func (s *Store) Revoke(ctx context.Context, orderID, id string) error {
	n, err := s.q.RevokeEntitlement(ctx, gen.RevokeEntitlementParams{ID: id, OrderID: orderID})
	if err != nil {
		return translate(fmt.Errorf("downloads: revoke: %w", err))
	}
	if n == 0 {
		// Either it does not exist, belongs to another order, or was already
		// revoked. All three mean the same thing to the operator — it is revoked
		// now — so only a genuinely absent one is an error worth showing.
		return ErrNotFound
	}
	return nil
}

// Restore reverses a revocation.
func (s *Store) Restore(ctx context.Context, orderID, id string) error {
	n, err := s.q.RestoreEntitlement(ctx, gen.RestoreEntitlementParams{ID: id, OrderID: orderID})
	if err != nil {
		return translate(fmt.Errorf("downloads: restore: %w", err))
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Stats is what the admin's download page reports for one product.
type Stats struct {
	TotalDownloads      int64
	UniqueBuyers        int64
	EntitlementsIssued  int64
	EntitlementsRevoked int64
	LastDownload        *time.Time
	Files               []FileStats
	Recent              []Event
}

// FileStats is one file's share of the total.
type FileStats struct {
	FileID        int64
	Title         string
	SizeBytes     int64
	DownloadCount int64
	LastDownload  *time.Time
}

// HumanSize renders the size the way the admin's file list does.
func (f FileStats) HumanSize() string { return catalog.HumanBytes(f.SizeBytes) }

// Event is one recorded download, for the recent list.
type Event struct {
	At            time.Time
	IP            string
	FileTitle     string
	EntitlementID string
	Customer      string
	OrderID       string
}

// RecentLimit is how many individual downloads the stats page lists. Enough to
// see the shape of a suspicious count without turning the page into a log viewer.
const RecentLimit = 25

// Stats gathers a product's download figures.
func (s *Store) Stats(ctx context.Context, productID string) (Stats, error) {
	totals, err := s.q.ProductDownloadStats(ctx, productID)
	if err != nil {
		return Stats{}, translate(fmt.Errorf("downloads: stats: %w", err))
	}
	out := Stats{
		TotalDownloads:      totals.TotalDownloads,
		UniqueBuyers:        totals.UniqueBuyers,
		EntitlementsIssued:  totals.EntitlementsIssued,
		EntitlementsRevoked: totals.EntitlementsRevoked,
		LastDownload:        timePtr(totals.LastDownload),
	}

	frows, err := s.q.ProductFileDownloadStats(ctx, productID)
	if err != nil {
		return Stats{}, fmt.Errorf("downloads: file stats: %w", err)
	}
	out.Files = make([]FileStats, 0, len(frows))
	for _, r := range frows {
		out.Files = append(out.Files, FileStats{
			FileID:        r.ID,
			Title:         r.Title,
			SizeBytes:     r.SizeBytes,
			DownloadCount: r.DownloadCount,
			LastDownload:  timePtr(r.LastDownload),
		})
	}

	rrows, err := s.q.RecentProductDownloads(ctx, gen.RecentProductDownloadsParams{
		ProductID: productID,
		Limit:     RecentLimit,
	})
	if err != nil {
		return Stats{}, fmt.Errorf("downloads: recent: %w", err)
	}
	out.Recent = make([]Event, 0, len(rrows))
	for _, r := range rrows {
		out.Recent = append(out.Recent, Event{
			At:            r.CreatedAt,
			IP:            r.Ip,
			FileTitle:     r.FileTitle,
			EntitlementID: r.EntitlementID,
			Customer:      r.CustomerEmail,
			OrderID:       r.OrderID,
		})
	}
	return out, nil
}

// timePtr normalises the several shapes an aggregate's nullable timestamp can
// arrive in. MAX() over no rows is NULL, which sqlc may type as interface{}
// depending on how much it can prove, so this accepts both rather than assuming.
func timePtr(v any) *time.Time {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		if t.IsZero() {
			return nil
		}
		return &t
	case *time.Time:
		return t
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// translate turns the driver's vocabulary into this package's, matching what the
// catalog and orders stores do: a missing row and a malformed UUID are both
// ErrNotFound, because from outside an id that could never exist and one that does
// not are the same answer.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return ErrNotFound
	}
	return err
}

// ForOrderID resolves one entitlement of one order, for a caller that has
// identified the buyer some other way than by their token.
//
// The post-payment page is that caller. It cannot show the emailed link — the
// plaintext token exists only for the moment the email is rendered, and only its
// hash is stored — but it has already identified the buyer by their cart cookie,
// which is the same credential that lets it show their name, address and total.
// Extending it to the files they just bought grants nothing further.
//
// The order id is part of the lookup, so an entitlement id belonging to somebody
// else's order finds nothing.
func (s *Store) ForOrderID(ctx context.Context, orderID, entitlementID string) (Entitlement, error) {
	row, err := s.q.GetOrderEntitlement(ctx, gen.GetOrderEntitlementParams{
		ID: entitlementID, OrderID: orderID,
	})
	if err != nil {
		return Entitlement{}, translate(fmt.Errorf("downloads: order entitlement: %w", err))
	}
	e := Entitlement{
		ID:           row.ID,
		OrderID:      row.OrderID,
		VariantID:    row.VariantID,
		ProductID:    row.ProductID,
		ProductTitle: row.ProductTitle,
		ProductSlug:  row.ProductSlug,
		Customer:     row.CustomerEmail,
		RevokedAt:    row.RevokedAt,
		CreatedAt:    row.CreatedAt,
	}
	if e.Revoked() {
		return e, ErrRevoked
	}
	if orders.Status(row.OrderStatus) != orders.StatusPaid {
		return e, ErrNotPaid
	}
	return e, nil
}

// Grant is one entitlement together with the files it grants, for a page that
// lists both.
type Grant struct {
	Entitlement Entitlement
	Files       []catalog.File
}

// GrantsForOrder lists an order's entitlements with their files.
//
// One query per entitlement, which is right here: an order has one or two digital
// lines, and a single joined query would fan each file out per entitlement and
// need deduplicating in Go.
func (s *Store) GrantsForOrder(ctx context.Context, orderID string) ([]Grant, error) {
	rows, err := s.q.ListOrderEntitlements(ctx, orderID)
	if err != nil {
		return nil, translate(fmt.Errorf("downloads: order grants: %w", err))
	}
	out := make([]Grant, 0, len(rows))
	for _, r := range rows {
		// A revoked entitlement is listed but carries no files, so the page says
		// what happened instead of silently showing one fewer row.
		g := Grant{Entitlement: Entitlement{
			ID:           r.ID,
			OrderID:      orderID,
			VariantID:    r.VariantID,
			ProductTitle: r.ProductTitle,
			RevokedAt:    r.RevokedAt,
			CreatedAt:    r.CreatedAt,
		}}
		if !g.Entitlement.Revoked() {
			if g.Files, err = s.cat.VariantFiles(ctx, r.VariantID); err != nil {
				return nil, err
			}
		}
		out = append(out, g)
	}
	return out, nil
}
