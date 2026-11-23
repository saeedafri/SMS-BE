package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WhatsAppFacts is what is known about a connected WhatsApp number. The token
// and PIN themselves never leave the store through this: HasToken says only
// that one is held.
type WhatsAppFacts struct {
	PhoneNumberID, BusinessID                                *string
	HasToken                                                 bool
	TokenStatus                                              *string
	Subscribed, Registered                                   bool
	RegistrationAttempts                                     int
	RegistrationSince                                        *time.Time
	PaymentMethod, BillingCurrency, DisplayNameStatus, Limit *string
	Restricted                                               *bool
	RestrictedUntil                                          *time.Time
	Disconnected                                             bool
}

// LoadWhatsAppFacts attaches the facts to the WhatsApp senders among these.
func LoadWhatsAppFacts(ctx context.Context, tx pgx.Tx, senders []SenderID) error {
	for i := range senders {
		if senders[i].Channel != "WHATSAPP" {
			continue
		}
		var f WhatsAppFacts
		if err := tx.QueryRow(ctx, `
			SELECT wa_phone_number_id, wa_business_id, wa_token_sealed IS NOT NULL, wa_token_status,
			       wa_subscribed, wa_registered, wa_registration_attempts, wa_registration_since,
			       wa_payment_method, wa_billing_currency, wa_display_name_status, wa_messaging_limit,
			       wa_restricted, wa_restricted_until, wa_disconnected
			FROM sender_ids WHERE id = $1`, senders[i].ID).Scan(&f.PhoneNumberID, &f.BusinessID,
			&f.HasToken, &f.TokenStatus, &f.Subscribed, &f.Registered, &f.RegistrationAttempts,
			&f.RegistrationSince, &f.PaymentMethod, &f.BillingCurrency, &f.DisplayNameStatus,
			&f.Limit, &f.Restricted, &f.RestrictedUntil, &f.Disconnected); err != nil {
			return err
		}
		senders[i].WhatsApp = &f
	}
	return nil
}

// NewWhatsAppSender is a number Embedded Signup just connected.
type NewWhatsAppSender struct {
	Header, WabaID                   string
	PhoneNumberID, BusinessID        *string
	TokenSealed, PinSealed           string
	Subscribed, Registered           bool
	RegistrationAttempts             int
	PaymentMethod, DisplayNameStatus string
	Limit                            *string
}

// CreateWhatsAppSender records the connected number and counts the
// onboarding, in one transaction. A number another tenant holds is a conflict.
func CreateWhatsAppSender(ctx context.Context, pool *pgxpool.Pool, id Identity, s NewWhatsAppSender) (uuid.UUID, error) {
	var senderID uuid.UUID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO sender_ids (tenant_id, header, channel, country, status, waba_id,
			    wa_phone_number_id, wa_business_id, wa_token_sealed, wa_token_status, wa_pin_sealed,
			    wa_subscribed, wa_registered, wa_registration_attempts, wa_registration_since,
			    wa_payment_method, wa_display_name_status, wa_messaging_limit)
			VALUES ($1, $2, 'WHATSAPP', 'IN', 'pending_review', $3, $4, $5, $6, 'ok', nullif($7, ''),
			    $8, $9, $10, now(), $11, $12, $13)
			RETURNING id`,
			id.TenantID, s.Header, s.WabaID, s.PhoneNumberID, s.BusinessID, s.TokenSealed, s.PinSealed,
			s.Subscribed, s.Registered, s.RegistrationAttempts, s.PaymentMethod, s.DisplayNameStatus,
			s.Limit).Scan(&senderID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO wa_onboardings (tenant_id) VALUES ($1)`, id.TenantID)
		return err
	})
	if isUniqueViolation(err) {
		return uuid.Nil, ErrConflict
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: create whatsapp sender: %w", err)
	}
	return senderID, nil
}

// WhatsAppToken is a sender's sealed business token, for the server's own
// calls to Meta. It is never returned by any endpoint.
func WhatsAppToken(ctx context.Context, pool *pgxpool.Pool, id Identity, senderID uuid.UUID) (string, error) {
	var sealed *string
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT wa_token_sealed FROM sender_ids WHERE id = $1`, senderID).Scan(&sealed)
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && sealed == nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: whatsapp token: %w", err)
	}
	return *sealed, nil
}

// SaveWhatsAppAccount records what Meta last said about a number, and
// promotes the sender to approved when ready says so.
func SaveWhatsAppAccount(ctx context.Context, pool *pgxpool.Pool, id Identity, senderID uuid.UUID,
	paymentMethod, billingCurrency, displayNameStatus, limit *string, tokenStatus string, ready bool) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE sender_ids SET wa_payment_method = $2, wa_billing_currency = $3,
			    wa_display_name_status = $4, wa_messaging_limit = $5, wa_token_status = $6,
			    status = CASE WHEN $7 THEN 'approved' ELSE status END
			WHERE id = $1`, senderID, paymentMethod, billingCurrency, displayNameStatus, limit,
			tokenStatus, ready)
		return err
	})
}

// SetWhatsAppStanding records an account event: disconnected, or restricted
// until a time (nil clears it).
func SetWhatsAppStanding(ctx context.Context, operator *pgxpool.Pool, wabaID string,
	disconnected *bool, restrictedUntil *time.Time, clearRestriction bool) ([]SenderRef, error) {

	rows, err := operator.Query(ctx, `
		UPDATE sender_ids SET
		    wa_disconnected = coalesce($2, wa_disconnected),
		    wa_restricted = CASE WHEN $4 THEN false WHEN $3::timestamptz IS NOT NULL THEN true
		                         ELSE wa_restricted END,
		    wa_restricted_until = CASE WHEN $4 THEN NULL ELSE coalesce($3, wa_restricted_until) END
		WHERE channel = 'WHATSAPP' AND waba_id = $1
		RETURNING id, tenant_id`, wabaID, disconnected, restrictedUntil, clearRestriction)
	if err != nil {
		return nil, fmt.Errorf("store: whatsapp standing: %w", err)
	}
	defer rows.Close()
	var out []SenderRef
	for rows.Next() {
		var ref SenderRef
		if err := rows.Scan(&ref.SenderID, &ref.TenantID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// SenderRef names one sender of one tenant.
type SenderRef struct{ SenderID, TenantID uuid.UUID }

// WhatsAppOnboardingsSince counts every number Relay onboarded since a time,
// across tenants. Read through the operator pool.
func WhatsAppOnboardingsSince(ctx context.Context, operator *pgxpool.Pool, since time.Time) (int, error) {
	var count int
	if err := operator.QueryRow(ctx, `SELECT count(*) FROM wa_onboardings WHERE onboarded_at >= $1`,
		since).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: whatsapp onboardings: %w", err)
	}
	return count, nil
}

// SetTemplateWAState records Meta's standing for a template, found by its
// Meta template id, across tenants. It returns the template and its tenant.
func SetTemplateWAState(ctx context.Context, operator *pgxpool.Pool, metaTemplateID string,
	state []byte) (uuid.UUID, uuid.UUID, error) {

	var templateID, tenantID uuid.UUID
	err := operator.QueryRow(ctx, `UPDATE templates SET wa_state = $2
		WHERE channel = 'WHATSAPP' AND wa_state ->> 'metaTemplateId' = $1
		RETURNING id, tenant_id`, metaTemplateID, state).Scan(&templateID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	return templateID, tenantID, err
}

// WhatsAppRegistrationAttempts is Meta's allowance per number in 72 hours.
const WhatsAppRegistrationAttempts = 10

// SetupStepFact is one step of a number's setup: pending, in_progress, done
// or blocked, with plain words about what is missing.
type SetupStepFact struct{ Key, Status, Detail string }

// Steps derives the number's setup from its facts on every read; a step the
// facts do not support is never done. billing_currency is advice only.
func (f *WhatsAppFacts) Steps() []SetupStepFact {
	var steps []SetupStepFact
	add := func(key, status, detail string) { steps = append(steps, SetupStepFact{key, status, detail}) }
	switch {
	case f.Disconnected:
		add("token_exchanged", "blocked", "The number was disconnected from Relay in WhatsApp Manager. Connect it again.")
	case !f.HasToken:
		add("token_exchanged", "pending", "")
	case f.TokenStatus != nil && *f.TokenStatus == "invalid":
		add("token_exchanged", "blocked", "The connection to Meta has expired. Connect the number again.")
	default:
		add("token_exchanged", "done", "")
	}
	if f.Subscribed {
		add("webhooks_subscribed", "done", "")
	} else {
		add("webhooks_subscribed", "pending", "")
	}
	switch {
	case f.Registered:
		add("number_registered", "done", "")
	case f.PhoneNumberID == nil:
		add("number_registered", "pending", "Add a phone number to this WhatsApp account in WhatsApp Manager.")
	case f.AttemptsLeft() == 0:
		add("number_registered", "blocked",
			"Meta allows 10 registration attempts in 72 hours and they are used. Try again later.")
	default:
		add("number_registered", "pending", "")
	}
	if f.PaymentMethod != nil && *f.PaymentMethod != "none" && *f.PaymentMethod != "unknown" {
		add("payment_method_added", "done", "")
	} else {
		add("payment_method_added", "pending", "Add a way to pay Meta in WhatsApp Manager. Relay cannot add one for you.")
	}
	display := ""
	if f.DisplayNameStatus != nil {
		display = *f.DisplayNameStatus
	}
	switch display {
	case "approved":
		add("display_name_approved", "done", "")
	case "declined":
		add("display_name_approved", "blocked", "Meta declined the display name. Change it in WhatsApp Manager.")
	case "pending_review":
		add("display_name_approved", "in_progress", "Meta is reviewing the display name.")
	default:
		add("display_name_approved", "pending", "")
	}
	if f.BillingCurrency != nil && *f.BillingCurrency != "" && *f.BillingCurrency != "INR" {
		add("billing_currency", "pending",
			"Meta asks Indian businesses to move WhatsApp billing to INR by 31 December 2026.")
	}
	return steps
}

// Ready says every step that gates sending is done.
func (f *WhatsAppFacts) Ready() bool {
	for _, step := range f.Steps() {
		if step.Key != "billing_currency" && step.Status != "done" {
			return false
		}
	}
	return true
}

// AttemptsLeft is how many registration attempts Meta still allows.
func (f *WhatsAppFacts) AttemptsLeft() int {
	if f.RegistrationSince != nil && time.Since(*f.RegistrationSince) > 72*time.Hour {
		return WhatsAppRegistrationAttempts
	}
	if left := WhatsAppRegistrationAttempts - f.RegistrationAttempts; left > 0 {
		return left
	}
	return 0
}

// WhatsAppContactsLast24h counts the distinct people a WhatsApp number has
// messaged in the last 24 hours, refusals excluded.
func WhatsAppContactsLast24h(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	header string, since time.Time) (int, error) {

	var count uint64
	if err := conn.QueryRow(ctx, `SELECT uniqExact(msisdn) FROM messages FINAL
		WHERE tenant_id = ? AND channel = 'WHATSAPP' AND sender_header = ? AND created_at >= ?
		  AND status != 'rejected'`, tenantID, header, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: whatsapp contacts: %w", err)
	}
	return int(count), nil
}

// WhatsAppTemplateCreation says whether a sender already has a WhatsApp
// template of this name in this language, and how many WhatsApp templates the
// tenant created since a time. A refused creation is never stored, so it never
// counts against the allowance.
func WhatsAppTemplateCreation(ctx context.Context, pool *pgxpool.Pool, id Identity,
	senderID uuid.UUID, name, language string, since time.Time) (bool, int, error) {

	var clash bool
	var recent int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			COALESCE(bool_or(sender_id = $1 AND wa_definition ->> 'name' = $2
			                 AND wa_definition ->> 'language' = $3), false),
			count(*) FILTER (WHERE created_at >= $4)
			FROM templates WHERE wa_definition IS NOT NULL`,
			senderID, name, language, since).Scan(&clash, &recent)
	})
	if err != nil {
		return false, 0, fmt.Errorf("store: whatsapp template creation: %w", err)
	}
	return clash, recent, nil
}

// SenderChange is a sender an account event touched, with what it read before.
type SenderChange struct {
	SenderRef
	Quality, Limit *string
}

// SetWhatsAppQuality records what Meta says about an account's numbers:
// their quality, their daily limit, or both. A nil value is left alone.
func SetWhatsAppQuality(ctx context.Context, operator *pgxpool.Pool, wabaID string,
	quality, limit *string) ([]SenderChange, error) {

	rows, err := operator.Query(ctx, `
		UPDATE sender_ids s SET
		    quality_rating     = coalesce($2, s.quality_rating),
		    wa_messaging_limit = coalesce($3, s.wa_messaging_limit)
		FROM (SELECT id, quality_rating, wa_messaging_limit FROM sender_ids
		      WHERE channel = 'WHATSAPP' AND waba_id = $1 FOR UPDATE) before
		WHERE s.id = before.id
		RETURNING s.id, s.tenant_id, before.quality_rating, before.wa_messaging_limit`,
		wabaID, quality, limit)
	if err != nil {
		return nil, fmt.Errorf("store: whatsapp quality: %w", err)
	}
	defer rows.Close()
	var out []SenderChange
	for rows.Next() {
		var change SenderChange
		if err := rows.Scan(&change.SenderID, &change.TenantID, &change.Quality, &change.Limit); err != nil {
			return nil, err
		}
		out = append(out, change)
	}
	return out, rows.Err()
}

// WhatsAppTemplateByMetaID finds a template and its tenant by Meta's id for
// it, with its definition and state. Read through the operator pool.
func WhatsAppTemplateByMetaID(ctx context.Context, operator *pgxpool.Pool,
	metaTemplateID string) (Template, uuid.UUID, error) {

	var t Template
	var tenantID uuid.UUID
	err := operator.QueryRow(ctx, `SELECT id, tenant_id, coalesce(wa_definition, '{}'), wa_state
		FROM templates WHERE channel = 'WHATSAPP' AND wa_state ->> 'metaTemplateId' = $1`,
		metaTemplateID).Scan(&t.ID, &tenantID, &t.WADefinition, &t.WAState)
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return Template{}, uuid.Nil, fmt.Errorf("store: whatsapp template: %w", err)
	}
	return t, tenantID, nil
}

// WhatsAppDisconnected says a tenant has a WhatsApp number Meta disconnected
// and none still able to send.
func WhatsAppDisconnected(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (bool, error) {
	var disconnected bool
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(bool_and(wa_disconnected), false)
			FROM sender_ids WHERE channel = 'WHATSAPP' AND waba_id IS NOT NULL`).Scan(&disconnected)
	})
	if err != nil {
		return false, fmt.Errorf("store: whatsapp disconnected: %w", err)
	}
	return disconnected, nil
}
