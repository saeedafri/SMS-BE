package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReportS3 is a tenant's export bucket. SealedSecret is the secret key as the
// server key encrypts it.
type ReportS3 struct {
	Bucket, Region, Prefix, AccessKeyID, SealedSecret string
	LastStatus, LastError                             *string
	LastAt                                            *time.Time
}

func GetReportS3(ctx context.Context, pool *pgxpool.Pool, id Identity) (ReportS3, bool, error) {
	var d ReportS3
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT bucket, region, prefix, access_key_id, sealed_secret_key,
			last_status, last_error, last_at FROM report_s3_destinations`).
			Scan(&d.Bucket, &d.Region, &d.Prefix, &d.AccessKeyID, &d.SealedSecret,
				&d.LastStatus, &d.LastError, &d.LastAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportS3{}, false, nil
	}
	if err != nil {
		return ReportS3{}, false, fmt.Errorf("store: get report s3: %w", err)
	}
	return d, true, nil
}

func SaveReportS3(ctx context.Context, pool *pgxpool.Pool, id Identity, d ReportS3) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO report_s3_destinations (tenant_id, bucket, region, prefix, access_key_id, sealed_secret_key)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id) DO UPDATE SET bucket=EXCLUDED.bucket, region=EXCLUDED.region,
			    prefix=EXCLUDED.prefix, access_key_id=EXCLUDED.access_key_id,
			    sealed_secret_key=EXCLUDED.sealed_secret_key, last_status=NULL, last_error=NULL,
			    last_at=NULL, updated_at=now()`,
			id.TenantID, d.Bucket, d.Region, d.Prefix, d.AccessKeyID, d.SealedSecret)
		return err
	})
}

func DeleteReportS3(ctx context.Context, pool *pgxpool.Pool, id Identity) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM report_s3_destinations`)
		return err
	})
}

// NoteReportS3Result records how the latest upload went.
func NoteReportS3Result(ctx context.Context, pool *pgxpool.Pool, id Identity, ok bool, detail string) {
	status := "ok"
	if !ok {
		status = "failed"
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	_ = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE report_s3_destinations SET last_status=$1,
			last_error=NULLIF($2,''), last_at=now()`, status, detail)
		return err
	})
}
