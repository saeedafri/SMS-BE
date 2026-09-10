package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MediaAsset is one uploaded file.
type MediaAsset struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Purpose     string
	ContentType string
	ByteSize    int64
	Width       *int
	Height      *int
	StorageKey  string
	CreatedAt   time.Time
}

const mediaAssetColumns = `id, tenant_id, purpose, content_type, byte_size,
	width, height, storage_key, created_at`

func scanMediaAsset(row pgx.Row) (MediaAsset, error) {
	var a MediaAsset
	err := row.Scan(&a.ID, &a.TenantID, &a.Purpose, &a.ContentType, &a.ByteSize,
		&a.Width, &a.Height, &a.StorageKey, &a.CreatedAt)
	return a, err
}

// CreateMediaAsset records the row and writes the bytes, in that order, with
// the write inside the transaction.
//
// write is called with the storage key AFTER the row exists and BEFORE the
// transaction commits, so a failed write rolls the row back. The other order
// leaves a row pointing at bytes that were never written — an asset that reads
// as present and 404s when a carrier fetches it, which is the worse of the two
// failures because nothing on any screen says so.
//
// The residue this cannot prevent is the opposite one: bytes on disk with no
// row, if the commit fails after the write. That is a retention sweep's problem
// rather than a correctness one — an orphan nobody can reach.
func CreateMediaAsset(ctx context.Context, pool *pgxpool.Pool, id Identity,
	asset MediaAsset, write func(storageKey string) error) (MediaAsset, error) {

	var created MediaAsset
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		assetID := uuid.New()
		key := mediaStorageKey(id.TenantID, assetID)
		row := tx.QueryRow(ctx, `
			INSERT INTO media_assets (id, tenant_id, purpose, content_type,
			                          byte_size, width, height, storage_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+mediaAssetColumns,
			assetID, id.TenantID, asset.Purpose, asset.ContentType,
			asset.ByteSize, asset.Width, asset.Height, key)
		var err error
		if created, err = scanMediaAsset(row); err != nil {
			return err
		}
		return write(key)
	})
	if err != nil {
		return MediaAsset{}, fmt.Errorf("store: create media asset: %w", err)
	}
	return created, nil
}

// FindMediaAsset reads an asset by id alone, WITHOUT a tenant scope.
//
// Deliberate and narrow: the signed-URL read has no session, so there is no
// tenant to scope by — the caller is a carrier fetching artwork or a browser
// following a link. It returns the tenant id so the signature can be checked
// against it, and the signature is what authorises the read. This must stay the
// only unscoped read of this table.
func FindMediaAsset(ctx context.Context, admin *pgxpool.Pool, assetID uuid.UUID) (MediaAsset, error) {
	asset, err := scanMediaAsset(admin.QueryRow(ctx,
		`SELECT `+mediaAssetColumns+` FROM media_assets WHERE id = $1`, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaAsset{}, ErrNotFound
	}
	if err != nil {
		return MediaAsset{}, fmt.Errorf("store: find media asset: %w", err)
	}
	return asset, nil
}

// GetMediaAsset reads an asset within the caller's tenant, which is what every
// path that has a session should use.
func GetMediaAsset(ctx context.Context, pool *pgxpool.Pool, id Identity,
	assetID uuid.UUID) (MediaAsset, error) {

	var asset MediaAsset
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		asset, err = scanMediaAsset(tx.QueryRow(ctx,
			`SELECT `+mediaAssetColumns+` FROM media_assets WHERE id = $1`, assetID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaAsset{}, ErrNotFound
	}
	if err != nil {
		return MediaAsset{}, fmt.Errorf("store: get media asset: %w", err)
	}
	return asset, nil
}

// mediaStorageKey is tenant-prefixed so isolation is enforceable at the path.
func mediaStorageKey(tenantID, assetID uuid.UUID) string {
	return tenantID.String() + "/" + assetID.String()
}
