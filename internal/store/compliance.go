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

// SenderID is a registered sending identity. The channel-specific fields are
// pointers because the contract makes them null for channels that do not use
// them — an SMS sender has no WhatsApp business account.
type SenderID struct {
	ID              uuid.UUID
	Header          string
	Channel         string
	Country         string
	Status          string
	RejectionReason *string
	ExternalID      *string
	WabaID          *string
	DisplayName     *string
	PhoneNumber     *string
	EmailDomain     *string
	FromAddress     *string
	FromName        *string
	CallerIDNumber  *string
	VoiceCode       *string
	VoiceVerified   bool
	VoiceCodeSentAt *time.Time
	CreatedAt       time.Time
}

const senderColumns = `id, header, channel, country, status, rejection_reason,
	external_id, waba_id, display_name, phone_number, email_domain, from_address,
	from_name, caller_id_number, voice_code, voice_verified, created_at`

func scanSender(row pgx.Row) (SenderID, error) {
	var s SenderID
	err := row.Scan(&s.ID, &s.Header, &s.Channel, &s.Country, &s.Status,
		&s.RejectionReason, &s.ExternalID, &s.WabaID, &s.DisplayName, &s.PhoneNumber,
		&s.EmailDomain, &s.FromAddress, &s.FromName, &s.CallerIDNumber,
		&s.VoiceCode, &s.VoiceVerified, &s.CreatedAt)
	return s, err
}

func ListSenderIDs(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]SenderID, error) {
	var out []SenderID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+senderColumns+` FROM sender_ids ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sender, err := scanSender(rows)
			if err != nil {
				return err
			}
			out = append(out, sender)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list sender ids: %w", err)
	}
	return out, nil
}

func GetSenderID(ctx context.Context, pool *pgxpool.Pool, id Identity, senderID uuid.UUID) (SenderID, error) {
	var sender SenderID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		sender, err = scanSender(tx.QueryRow(ctx,
			`SELECT `+senderColumns+` FROM sender_ids WHERE id = $1`, senderID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SenderID{}, ErrNotFound
	}
	if err != nil {
		return SenderID{}, fmt.Errorf("store: get sender id: %w", err)
	}
	return sender, nil
}

func CreateSenderID(ctx context.Context, pool *pgxpool.Pool, id Identity, sender SenderID) (SenderID, error) {
	var created SenderID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanSender(tx.QueryRow(ctx, `
			INSERT INTO sender_ids (tenant_id, header, channel, country,
			    waba_id, display_name, phone_number, email_domain,
			    from_address, from_name, caller_id_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+senderColumns,
			id.TenantID, sender.Header, sender.Channel, sender.Country,
			sender.WabaID, sender.DisplayName, sender.PhoneNumber, sender.EmailDomain,
			sender.FromAddress, sender.FromName, sender.CallerIDNumber))
		return err
	})
	if isUniqueViolation(err) {
		return SenderID{}, ErrConflict
	}
	if err != nil {
		return SenderID{}, fmt.Errorf("store: create sender id: %w", err)
	}
	return created, nil
}

// SetSenderVoiceCode stores the code the verification call will speak.
func SetSenderVoiceCode(ctx context.Context, pool *pgxpool.Pool, id Identity,
	senderID uuid.UUID, code string) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender_ids SET voice_code = $1, voice_verified = false WHERE id = $2`,
			code, senderID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: set voice code: %w", err)
	}
	return nil
}

// MarkSenderVoiceVerified clears the code as it verifies, so the same code
// cannot be replayed against a later verification attempt.
func MarkSenderVoiceVerified(ctx context.Context, pool *pgxpool.Pool, id Identity, senderID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sender_ids SET voice_verified = true, voice_code = NULL WHERE id = $1`,
			senderID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: mark voice verified: %w", err)
	}
	return nil
}

// Template is a message body tied to a sender, with its variables extracted.
type Template struct {
	ID              uuid.UUID
	SenderID        uuid.UUID
	Name            string
	Channel         string
	Country         string
	Body            *string
	Category        *string
	Variables       []string
	CtaURL          *string
	Status          string
	RejectionReason *string
	CreatedAt       time.Time
}

const templateColumns = `id, sender_id, name, channel, country, body, category,
	variables, cta_url, status, rejection_reason, created_at`

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	err := row.Scan(&t.ID, &t.SenderID, &t.Name, &t.Channel, &t.Country, &t.Body,
		&t.Category, &t.Variables, &t.CtaURL, &t.Status, &t.RejectionReason, &t.CreatedAt)
	return t, err
}

func ListTemplates(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]Template, error) {
	var out []Template
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+templateColumns+` FROM templates ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			template, err := scanTemplate(rows)
			if err != nil {
				return err
			}
			out = append(out, template)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	return out, nil
}

func GetTemplate(ctx context.Context, pool *pgxpool.Pool, id Identity, templateID uuid.UUID) (Template, error) {
	var template Template
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		template, err = scanTemplate(tx.QueryRow(ctx,
			`SELECT `+templateColumns+` FROM templates WHERE id = $1`, templateID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("store: get template: %w", err)
	}
	return template, nil
}

func CreateTemplate(ctx context.Context, pool *pgxpool.Pool, id Identity, template Template) (Template, error) {
	var created Template
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanTemplate(tx.QueryRow(ctx, `
			INSERT INTO templates (tenant_id, sender_id, name, channel, country,
			    body, category, variables, cta_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING `+templateColumns,
			id.TenantID, template.SenderID, template.Name, template.Channel,
			template.Country, template.Body, template.Category,
			template.Variables, template.CtaURL))
		return err
	})
	if isUniqueViolation(err) {
		return Template{}, ErrConflict
	}
	if err != nil {
		return Template{}, fmt.Errorf("store: create template: %w", err)
	}
	return created, nil
}

// Registration is one regulatory filing.
type Registration struct {
	ID              uuid.UUID
	Country         string
	ObjectKey       string
	Status          string
	RejectionReason *string
	ExternalID      *string
	Fields          map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const registrationColumns = `id, country, object_key, status, rejection_reason,
	external_id, fields, created_at, updated_at`

func scanRegistration(row pgx.Row) (Registration, error) {
	var r Registration
	var raw []byte
	err := row.Scan(&r.ID, &r.Country, &r.ObjectKey, &r.Status, &r.RejectionReason,
		&r.ExternalID, &raw, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Registration{}, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r.Fields); err != nil {
			return Registration{}, fmt.Errorf("decode fields: %w", err)
		}
	}
	if r.Fields == nil {
		r.Fields = map[string]any{}
	}
	return r, nil
}

func ListRegistrations(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]Registration, error) {
	var out []Registration
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+registrationColumns+` FROM registrations ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			registration, err := scanRegistration(rows)
			if err != nil {
				return err
			}
			out = append(out, registration)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list registrations: %w", err)
	}
	return out, nil
}

func GetRegistration(ctx context.Context, pool *pgxpool.Pool, id Identity, registrationID uuid.UUID) (Registration, error) {
	var registration Registration
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		registration, err = scanRegistration(tx.QueryRow(ctx,
			`SELECT `+registrationColumns+` FROM registrations WHERE id = $1`, registrationID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Registration{}, ErrNotFound
	}
	if err != nil {
		return Registration{}, fmt.Errorf("store: get registration: %w", err)
	}
	return registration, nil
}

func CreateRegistration(ctx context.Context, pool *pgxpool.Pool, id Identity, registration Registration) (Registration, error) {
	encoded, err := json.Marshal(registration.Fields)
	if err != nil {
		return Registration{}, fmt.Errorf("store: encode fields: %w", err)
	}
	var created Registration
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanRegistration(tx.QueryRow(ctx, `
			INSERT INTO registrations (tenant_id, country, object_key, fields)
			VALUES ($1, $2, $3, $4)
			RETURNING `+registrationColumns,
			id.TenantID, registration.Country, registration.ObjectKey, encoded))
		return err
	})
	if isUniqueViolation(err) {
		return Registration{}, ErrConflict
	}
	if err != nil {
		return Registration{}, fmt.Errorf("store: create registration: %w", err)
	}
	return created, nil
}

// RegistrationStatus reports one object's approval state, for the dependsOn
// check. Absent means never filed, which is distinct from filed-and-pending.
func RegistrationStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	country, objectKey string) (string, error) {

	var status string
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM registrations WHERE country = $1 AND object_key = $2`,
			country, objectKey).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: registration status: %w", err)
	}
	return status, nil
}
