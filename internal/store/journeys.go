package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Journey is an automated sequence of send and wait steps.
type Journey struct {
	ID            uuid.UUID
	Name          string
	Status        string
	TriggerType   string
	TriggerListID *uuid.UUID
	TriggerRunAt  *time.Time
	Steps         []byte
	Recipients    int
	ActivatedAt   *time.Time
	CreatedAt     time.Time
}

const journeyColumns = `
	id, name, status, trigger_type, trigger_list_id, trigger_run_at,
	steps, recipients, activated_at, created_at`

func scanJourney(row pgx.Row) (Journey, error) {
	var journey Journey
	err := row.Scan(&journey.ID, &journey.Name, &journey.Status, &journey.TriggerType,
		&journey.TriggerListID, &journey.TriggerRunAt, &journey.Steps,
		&journey.Recipients, &journey.ActivatedAt, &journey.CreatedAt)
	return journey, err
}

func ListJourneys(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]Journey, error) {
	var out []Journey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+journeyColumns+` FROM journeys ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			journey, err := scanJourney(rows)
			if err != nil {
				return err
			}
			out = append(out, journey)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list journeys: %w", err)
	}
	return out, nil
}

func GetJourney(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journeyID uuid.UUID) (Journey, error) {

	var journey Journey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		journey, err = scanJourney(tx.QueryRow(ctx,
			`SELECT `+journeyColumns+` FROM journeys WHERE id = $1`, journeyID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Journey{}, ErrNotFound
	}
	if err != nil {
		return Journey{}, fmt.Errorf("store: get journey: %w", err)
	}
	return journey, nil
}

func CreateJourney(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journey Journey) (Journey, error) {

	if len(journey.Steps) == 0 {
		journey.Steps = []byte("[]")
	}
	var created Journey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanJourney(tx.QueryRow(ctx, `
			INSERT INTO journeys (tenant_id, name, trigger_type, trigger_list_id,
			    trigger_run_at, steps, recipients)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING `+journeyColumns,
			id.TenantID, journey.Name, journey.TriggerType, journey.TriggerListID,
			journey.TriggerRunAt, journey.Steps, journey.Recipients))
		return err
	})
	if err != nil {
		return Journey{}, fmt.Errorf("store: create journey: %w", err)
	}
	return created, nil
}

// SetJourneyStatus moves a journey between draft, active, paused and archived.
// Activation stamps activated_at once and never again, so a pause/resume cycle
// does not rewrite when the journey actually went live.
func SetJourneyStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journeyID uuid.UUID, status string) (Journey, error) {

	var journey Journey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		journey, err = scanJourney(tx.QueryRow(ctx, `
			UPDATE journeys
			SET status = $2,
			    activated_at = CASE
			        WHEN $2 = 'active' AND activated_at IS NULL THEN now()
			        ELSE activated_at END,
			    updated_at = now()
			WHERE id = $1
			RETURNING `+journeyColumns, journeyID, status))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Journey{}, ErrNotFound
	}
	if err != nil {
		return Journey{}, fmt.Errorf("store: set journey status: %w", err)
	}
	return journey, nil
}

// StepsOf decodes the stored step list.
func StepsOf(journey Journey) []map[string]any {
	var steps []map[string]any
	if len(journey.Steps) > 0 {
		_ = json.Unmarshal(journey.Steps, &steps)
	}
	return steps
}

// UpdateJourney changes a journey's name, steps or trigger. Nil means "leave
// this alone", so a rename does not have to resend the whole step list.
//
// Status is deliberately not updatable here: moving between draft, active and
// paused goes through SetJourneyStatus, which owns the activated_at stamping
// rule. Two paths that can both change status would eventually disagree about
// it.
func UpdateJourney(ctx context.Context, pool *pgxpool.Pool, id Identity,
	journeyID uuid.UUID, name *string, steps []byte, triggerType *string,
	triggerListID *uuid.UUID) (Journey, error) {

	var journey Journey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		journey, err = scanJourney(tx.QueryRow(ctx, `
			UPDATE journeys
			SET name            = coalesce($2, name),
			    steps           = coalesce($3::jsonb, steps),
			    trigger_type    = coalesce($4, trigger_type),
			    -- The list only moves when the trigger does. Coalescing it
			    -- independently would leave a journey triggered by an event
			    -- still pointing at the list it used to watch.
			    trigger_list_id = CASE WHEN $4 IS NULL THEN trigger_list_id
			                           ELSE $5 END,
			    updated_at      = now()
			WHERE id = $1
			RETURNING `+journeyColumns,
			journeyID, name, steps, triggerType, triggerListID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Journey{}, ErrNotFound
	}
	if err != nil {
		return Journey{}, fmt.Errorf("store: update journey: %w", err)
	}
	return journey, nil
}
