package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// SendAllowance is how much of a tenant's daily volume ceiling is still free.
//
// It is read once per campaign launch and once per single send, never per
// recipient: the fan-out keeps its own running count between pages, the same
// way it keeps a running wallet balance, so a hundred thousand recipients cost
// one read of this rather than a hundred thousand.
type SendAllowance struct {
	// Day is the tenant's OWN calendar day the allowance belongs to. Held here
	// rather than recomputed by the caller so that the read and the write that
	// follows it cannot land on two different days — which they would, for one
	// message a night, on every campaign that runs across midnight.
	Day time.Time

	// CapPerDay is nil for a tenant with no ceiling, which is the default and
	// the overwhelming majority. Zero is a real ceiling and means "send
	// nothing", told apart from nil on purpose.
	CapPerDay *int

	// Accepted is what this tenant has already been let through today.
	Accepted int64

	// CapPercent is the share of each send this tenant may have, 0-100. Nil
	// means no share cap, which is the default.
	//
	// A different control from CapPerDay above, not a second spelling of it.
	// CapPerDay bounds a DAY across every send; this bounds each send against
	// its own audience, so one setting covers a campaign of a hundred and one
	// of a lakh. They compose: a send is cut to its share, and the day's
	// ceiling still applies to what is left.
	CapPercent *int
}

// ShareCapped says whether each send is cut to a share of its audience.
func (a SendAllowance) ShareCapped() bool { return a.CapPercent != nil }

// Share is the percentage this tenant may send, or 100 when uncapped.
func (a SendAllowance) Share() int {
	if a.CapPercent == nil {
		return 100
	}
	return *a.CapPercent
}

// Room is how many of want this allowance admits: want itself when the tenant
// is uncapped, fewer when the ceiling is close, zero when it is spent.
//
// Phrased as "how much of what you are asking for" rather than as a bare
// remaining count so that an uncapped tenant has no number at all to overflow
// or to accidentally compare against.
func (a SendAllowance) Room(want int) int {
	if a.CapPerDay == nil || want <= 0 {
		return max(want, 0)
	}
	left := int64(*a.CapPerDay) - a.Accepted
	if left <= 0 {
		return 0
	}
	if left < int64(want) {
		return int(left)
	}
	return want
}

// Spend records n against this allowance in memory.
//
// A fan-out reads the ceiling ONCE and then spends it down page by page, the
// same way it spends down its wallet snapshot. Re-reading it per page would
// cost a round trip per five hundred recipients to learn a number the loop
// already knows.
func (a *SendAllowance) Spend(n int) {
	if n > 0 {
		a.Accepted += int64(n)
	}
}

// Capped says whether this tenant has a ceiling at all. A tenant without one
// must not pay any of the cost of the feature — no clipping, no usage write.
func (a SendAllowance) Capped() bool { return a.CapPerDay != nil }

// SendDay is the tenant's own calendar day for an instant.
//
// A UTC day would roll an Indian tenant's allowance over at 05:30 in the
// morning, which is neither midnight nor any boundary a customer would
// recognise. Same zone table the invoicing month end uses, for the same
// reason: two features disagreeing about when a day ends is the kind of thing
// that only surfaces in an argument with a customer.
func SendDay(country string, at time.Time) time.Time {
	local := at.In(billing.BillingLocationFor(country))
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// ReadSendAllowance reads a tenant's ceiling and what they have spent of it.
func ReadSendAllowance(ctx context.Context, pool *pgxpool.Pool, id Identity,
	at time.Time) (SendAllowance, error) {

	allowance := SendAllowance{Day: SendDay(id.Country, at)}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT send_cap_per_day, send_cap_percent FROM tenants WHERE id = $1`,
			id.TenantID).Scan(&allowance.CapPerDay, &allowance.CapPercent); err != nil {
			return err
		}
		// An uncapped tenant's usage is never read and never written. The
		// feature costs them one column on a row that was going to be read
		// anyway, and nothing else.
		if allowance.CapPerDay == nil {
			return nil
		}
		err := tx.QueryRow(ctx,
			`SELECT accepted FROM tenant_send_usage WHERE tenant_id = $1 AND day = $2`,
			id.TenantID, allowance.Day).Scan(&allowance.Accepted)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return SendAllowance{}, fmt.Errorf("store: read send allowance: %w", err)
	}
	return allowance, nil
}

// RecordSendUsage adds to a tenant's day: what was let through, and what the
// ceiling took off.
//
// The addition happens inside the statement rather than in Go, so two campaigns
// running at once for one tenant cannot each read 40,000 and each write 40,500.
// The READ above can still be a moment stale, which is why the fan-out counts
// down its own room between pages; the overshoot that leaves is bounded by one
// page per campaign in flight, and a margin control does not need to be exact
// to the message.
func RecordSendUsage(ctx context.Context, pool *pgxpool.Pool, id Identity,
	day time.Time, accepted, withheld int) error {

	if accepted == 0 && withheld == 0 {
		return nil
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_send_usage (tenant_id, day, accepted, withheld)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, day) DO UPDATE
			    SET accepted = tenant_send_usage.accepted + EXCLUDED.accepted,
			        withheld = tenant_send_usage.withheld + EXCLUDED.withheld`,
			id.TenantID, day, accepted, withheld)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record send usage: %w", err)
	}
	return nil
}

// SetSendCap applies or lifts a tenant's daily volume ceiling. Operator-only:
// it takes a pool that can see across tenants, and there is no tenant-facing
// caller anywhere.
func SetSendCap(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	perDay *int) error {

	tag, err := pool.Exec(ctx,
		`UPDATE tenants SET send_cap_per_day = $2 WHERE id = $1`, tenantID, perDay)
	if err != nil {
		return fmt.Errorf("store: set send cap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReadSendUsage reports what a ceiling has let through and taken off. Operator
// side only — nothing on a tenant's own routes reads it.
func ReadSendUsage(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	day time.Time) (accepted, withheld int64, err error) {

	err = pool.QueryRow(ctx,
		`SELECT accepted, withheld FROM tenant_send_usage WHERE tenant_id = $1 AND day = $2`,
		tenantID, day).Scan(&accepted, &withheld)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("store: read send usage: %w", err)
	}
	return accepted, withheld, nil
}

// SetSendCapPercent applies or lifts the share of each send a tenant may have.
// Operator-only, like SetSendCap.
func SetSendCapPercent(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	percent *int) error {

	tag, err := pool.Exec(ctx,
		`UPDATE tenants SET send_cap_percent = $2 WHERE id = $1`, tenantID, percent)
	if err != nil {
		return fmt.Errorf("store: set send cap percent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkCarriedUnderCap records that a capped send reached these contacts, which
// is what puts them at the back of the queue next time.
//
// Written only for a capped tenant. An uncapped one is never cut, so there is
// nothing to rotate and no reason to make them pay a write per page for it.
func MarkCarriedUnderCap(ctx context.Context, pool *pgxpool.Pool, id Identity,
	contactIDs []uuid.UUID, at time.Time) error {

	if len(contactIDs) == 0 {
		return nil
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE contacts SET last_capped_send_at = $2 WHERE id = ANY($1)`,
			contactIDs, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: mark carried under cap: %w", err)
	}
	return nil
}

// CountAlwaysSendOnChannel counts the contacts in a list that no cap may
// withhold. The quote needs it: a send whose exempt contacts outnumber its
// share is quoted at the exempt count, not at the share.
func CountAlwaysSendOnChannel(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, channels ...string) (int, error) {

	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM contacts c
			WHERE c.always_send
			  AND ($1::uuid IS NULL OR EXISTS (
			        SELECT 1 FROM contact_list_members m
			        WHERE m.contact_id = c.id AND m.list_id = $1))
			  `+reachableOnChannel, listID, channels).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("store: count always-send contacts: %w", err)
	}
	return total, nil
}
