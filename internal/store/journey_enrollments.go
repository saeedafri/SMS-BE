package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Enrollment is one contact's place in one journey.
type Enrollment struct {
	ID        uuid.UUID
	ContactID uuid.UUID
	StepIndex int
	Waiting   bool
	NextRunAt time.Time
	State     string
}

// ActiveJourney names an active journey and whose it is.
type ActiveJourney struct {
	ID       uuid.UUID
	TenantID uuid.UUID
}

// ActiveJourneys lists every active journey, across tenants.
func ActiveJourneys(ctx context.Context, operator *pgxpool.Pool) ([]ActiveJourney, error) {
	rows, err := operator.Query(ctx, `SELECT id, tenant_id FROM journeys WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("store: active journeys: %w", err)
	}
	defer rows.Close()
	var out []ActiveJourney
	for rows.Next() {
		var journey ActiveJourney
		if err := rows.Scan(&journey.ID, &journey.TenantID); err != nil {
			return nil, err
		}
		out = append(out, journey)
	}
	return out, rows.Err()
}

// EnrollJourney enrols the journey's trigger list, reporting how many contacts
// are new to it.
//
// A list_entry journey enrols whoever is on the list and not yet enrolled, on
// every call: that is how a contact added tomorrow is picked up tomorrow. A
// scheduled journey enrols the list once, at run_at, and marks itself swept in
// the same transaction so no later call sweeps it again.
func EnrollJourney(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journey Journey, now time.Time) (int, error) {

	if journey.TriggerListID == nil {
		return 0, nil
	}
	var enrolled int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if journey.TriggerType == "scheduled" {
			tag, err := tx.Exec(ctx, `
				UPDATE journeys SET trigger_swept_at = $2
				WHERE id = $1 AND trigger_swept_at IS NULL AND trigger_run_at <= $2`,
				journey.ID, now)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO journey_enrollments (tenant_id, journey_id, contact_id, next_run_at)
			SELECT $1, $2, m.contact_id, $4
			FROM contact_list_members m WHERE m.list_id = $3
			ON CONFLICT (journey_id, contact_id) DO NOTHING`,
			id.TenantID, journey.ID, *journey.TriggerListID, now)
		enrolled = int(tag.RowsAffected())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: enroll journey: %w", err)
	}
	return enrolled, nil
}

// ClaimDueEnrollments takes up to limit active enrollments whose time has
// come, pushing each one's next_run_at out to leaseUntil so a second worker
// passes over them. SKIP LOCKED keeps two claimers off the same rows; the
// lease is what brings a contact back if the process dies holding one.
func ClaimDueEnrollments(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journeyID uuid.UUID, now, leaseUntil time.Time, limit int) ([]Enrollment, error) {

	var out []Enrollment
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE journey_enrollments SET next_run_at = $3, updated_at = now()
			WHERE id IN (
			    SELECT id FROM journey_enrollments
			    WHERE journey_id = $1 AND state = 'active' AND next_run_at <= $2
			    ORDER BY next_run_at LIMIT $4
			    FOR UPDATE SKIP LOCKED)
			RETURNING id, contact_id, step_index, waiting, next_run_at, state`,
			journeyID, now, leaseUntil, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Enrollment
			if err := rows.Scan(&e.ID, &e.ContactID, &e.StepIndex, &e.Waiting,
				&e.NextRunAt, &e.State); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: claim due enrollments: %w", err)
	}
	return out, nil
}

// SaveEnrollment writes where a contact now is.
func SaveEnrollment(ctx context.Context, pool *pgxpool.Pool, id Identity, e Enrollment) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE journey_enrollments
			SET step_index = $2, waiting = $3, next_run_at = $4, state = $5, updated_at = now()
			WHERE id = $1`, e.ID, e.StepIndex, e.Waiting, e.NextRunAt, e.State)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: save enrollment: %w", err)
	}
	return nil
}

// JourneyFunnel is a journey's enrolment, counted.
type JourneyFunnel struct {
	TotalEnrolled    int
	Completed        int
	ExitedSuppressed int
	// AtStep is how many active contacts are on each step index right now.
	AtStep map[int]int
}

func CountJourneyFunnel(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journeyID uuid.UUID) (JourneyFunnel, error) {

	funnel := JourneyFunnel{AtStep: map[int]int{}}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT state, step_index, count(*) FROM journey_enrollments
			WHERE journey_id = $1 GROUP BY state, step_index`, journeyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var state string
			var step, count int
			if err := rows.Scan(&state, &step, &count); err != nil {
				return err
			}
			funnel.TotalEnrolled += count
			switch state {
			case "completed":
				funnel.Completed += count
			case "exited_suppressed":
				funnel.ExitedSuppressed += count
			default:
				funnel.AtStep[step] += count
			}
		}
		return rows.Err()
	})
	if err != nil {
		return JourneyFunnel{}, fmt.Errorf("store: count journey funnel: %w", err)
	}
	return funnel, nil
}
