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

	// QualityRating and MessagingTier are assigned by WhatsApp, not by us, and
	// stay nil for every other channel and for an account Meta has not yet
	// rated. See db/migrations/00022_sender_wa_quality.sql.
	QualityRating *string
	MessagingTier *int32

	// DNSRecords is populated for email senders only. The contract declares it
	// on SenderId, and the onboarding screen shows one row per record with its
	// own state, so it is loaded alongside rather than behind a second call the
	// frontend would have to know to make.
	DNSRecords []SenderDNSRecord
}

// SenderDNSRecord is one DNS record an email sender must publish.
type SenderDNSRecord struct {
	Type   string
	Host   string
	Value  string
	Status string
}

// LoadSenderDNSRecords attaches DNS records to the senders that have them.
//
// One query for the whole set rather than one per sender: the senders list is
// short but this is on a page load, and a per-row query is the shape that turns
// into an N+1 the moment a customer has twenty senders.
func LoadSenderDNSRecords(ctx context.Context, tx pgx.Tx, senders []SenderID) error {
	ids := make([]uuid.UUID, 0, len(senders))
	for _, sender := range senders {
		if sender.Channel == "EMAIL" {
			ids = append(ids, sender.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT sender_id, record_type, host, value, status
		FROM sender_dns_records WHERE sender_id = ANY($1)
		ORDER BY sender_id, record_type`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	bySender := map[uuid.UUID][]SenderDNSRecord{}
	for rows.Next() {
		var senderID uuid.UUID
		var record SenderDNSRecord
		if err := rows.Scan(&senderID, &record.Type, &record.Host,
			&record.Value, &record.Status); err != nil {
			return err
		}
		bySender[senderID] = append(bySender[senderID], record)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range senders {
		if records, ok := bySender[senders[i].ID]; ok {
			senders[i].DNSRecords = records
		}
	}
	return nil
}

const senderColumns = `id, header, channel, country, status, rejection_reason,
	external_id, waba_id, display_name, phone_number, email_domain, from_address,
	from_name, caller_id_number, voice_code, voice_verified, created_at,
	quality_rating, messaging_tier`

func scanSender(row pgx.Row) (SenderID, error) {
	var s SenderID
	err := row.Scan(&s.ID, &s.Header, &s.Channel, &s.Country, &s.Status,
		&s.RejectionReason, &s.ExternalID, &s.WabaID, &s.DisplayName, &s.PhoneNumber,
		&s.EmailDomain, &s.FromAddress, &s.FromName, &s.CallerIDNumber,
		&s.VoiceCode, &s.VoiceVerified, &s.CreatedAt,
		&s.QualityRating, &s.MessagingTier)
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
		if err := rows.Err(); err != nil {
			return err
		}
		// Closed explicitly before the next query: pgx allows only one active
		// result set per connection, so leaving this open makes the DNS query
		// fail with a "conn busy" that looks nothing like its cause.
		rows.Close()
		return LoadSenderDNSRecords(ctx, tx, out)
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
			    from_address, from_name, caller_id_number, external_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING `+senderColumns,
			id.TenantID, sender.Header, sender.Channel, sender.Country,
			sender.WabaID, sender.DisplayName, sender.PhoneNumber, sender.EmailDomain,
			sender.FromAddress, sender.FromName, sender.CallerIDNumber,
			// Written verbatim, only ever from client input. Nothing derives or
			// defaults this: a DLT id is issued by DLT, not by us.
			sender.ExternalID))
		if err != nil {
			return err
		}
		// An email sender is not usable until its domain is authenticated, and
		// the records to publish are derived from the domain the customer just
		// gave us. Generating them here — in the same transaction as the sender
		// — means the onboarding screen never renders a sender with nowhere to
		// send the customer next.
		if sender.Channel == "EMAIL" && sender.EmailDomain != nil && *sender.EmailDomain != "" {
			domain := *sender.EmailDomain
			_, err = tx.Exec(ctx, `
				INSERT INTO sender_dns_records (tenant_id, sender_id, record_type, host, value)
				VALUES ($1, $2, 'SPF',   $3,            $4),
				       ($1, $2, 'DKIM',  $5,            $6),
				       ($1, $2, 'DMARC', $7,            $8)`,
				id.TenantID, created.ID,
				domain, "v=spf1 include:mail.relay-platform.example ~all",
				"relay1._domainkey."+domain, "v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCB",
				"_dmarc."+domain, "v=DMARC1; p=quarantine; rua=mailto:dmarc@"+domain)
			if err != nil {
				return err
			}
			// Load them onto the sender being returned.
			//
			// `created` was scanned from the INSERT's RETURNING clause, which
			// ran before these records existed, so it carried an empty list.
			// The onboarding screen reads exactly this response to tell the
			// customer which records to publish — so registering an email
			// sender showed them a sender with nothing to do and no way
			// forward, until they happened to reload the page.
			senders := []SenderID{created}
			if err := LoadSenderDNSRecords(ctx, tx, senders); err != nil {
				return err
			}
			created = senders[0]
		}
		return nil
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

	// ExternalID is the DLT content-template id and DltCategory the category
	// DLT approved it under. Both come from the customer's own DLT
	// registration; nothing here derives or defaults either.
	//
	// DltCategory is deliberately separate from Category. Category is Meta's
	// WhatsApp taxonomy, DltCategory is India's, and both spell TRANSACTIONAL
	// while meaning different things — Meta's is ordinary, DLT's is restricted
	// to banking and OTP traffic. One column for both would mis-file Indian
	// traffic with nothing to catch it until DLT complained.
	ExternalID  *string
	DltCategory *string

	// Channel-specific message content, carried as raw JSON exactly as the
	// contract defines it. Kept as bytes rather than decoded into a Go type
	// here because each is a discriminated union whose variants are generated
	// from the contract — the API layer owns those types, and decoding twice
	// would mean two definitions of the same shape. Exactly one of the three is
	// non-nil for any template; the database enforces that against the channel.
	RCSContent   []byte
	WAContent    []byte
	EmailContent []byte

	// The carrier's own approval, which is not the same approval as Status.
	// See db/migrations/00037_rcs_carrier_templates.sql — a template can be
	// approved here and unknown to Airtel, and every send of it then fails at
	// the gateway.
	CarrierVendor          *string
	CarrierTemplateID      *string
	CarrierStatus          string
	CarrierRejectionReason *string
	CarrierSubmittedAt     *time.Time
	CarrierUpdatedAt       *time.Time
}

const templateColumns = `id, sender_id, name, channel, country, body, category,
	external_id, dlt_category,
	variables, cta_url, status, rejection_reason, created_at,
	rcs_content, wa_content, email_content,
	carrier_vendor, carrier_template_id, carrier_status,
	carrier_rejection_reason, carrier_submitted_at, carrier_updated_at`

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	err := row.Scan(&t.ID, &t.SenderID, &t.Name, &t.Channel, &t.Country, &t.Body,
		&t.Category, &t.ExternalID, &t.DltCategory,
		&t.Variables, &t.CtaURL, &t.Status, &t.RejectionReason, &t.CreatedAt,
		&t.RCSContent, &t.WAContent, &t.EmailContent,
		&t.CarrierVendor, &t.CarrierTemplateID, &t.CarrierStatus,
		&t.CarrierRejectionReason, &t.CarrierSubmittedAt, &t.CarrierUpdatedAt)
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

// nullableJSON keeps an absent payload as SQL NULL rather than the two bytes
// "{}" or an empty string. jsonb rejects the empty string outright, and "{}"
// would store "this template has WhatsApp content, and it is empty" — which the
// channel-matching CHECK then reads as a WhatsApp payload on whatever channel
// the template actually is.
func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func CreateTemplate(ctx context.Context, pool *pgxpool.Pool, id Identity, template Template) (Template, error) {
	var created Template
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanTemplate(tx.QueryRow(ctx, `
			INSERT INTO templates (tenant_id, sender_id, name, channel, country,
			    body, category, external_id, dlt_category, variables, cta_url,
			    rcs_content, wa_content, email_content)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING `+templateColumns,
			id.TenantID, template.SenderID, template.Name, template.Channel,
			template.Country, template.Body, template.Category,
			template.ExternalID, template.DltCategory,
			template.Variables, template.CtaURL,
			// Columns are named explicitly above. An insert bound to table
			// order broke every send once already, the day a column was added
			// to the message table — see internal/store/messages.go.
			nullableJSON(template.RCSContent),
			nullableJSON(template.WAContent),
			nullableJSON(template.EmailContent)))
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
		// A REJECTED registration is resubmittable; anything else is a conflict.
		//
		// This was a plain INSERT, so the UNIQUE (tenant_id, country,
		// object_key) turned every resubmission into a 409 — including the
		// legitimate one. The console showed the rejection reason, showed the
		// remediation telling the customer exactly what to correct, offered a
		// "Resubmit registration" button, and then refused with "already
		// registered for India (DLT)". A rejection was a dead end: the only way
		// out was for an operator to edit the database.
		//
		// The regulator holds one record per tenant per object, so correcting
		// it is an UPDATE of that record — not a second application. The
		// rejection reason is cleared with it, otherwise the screen keeps
		// showing why the PREVIOUS attempt failed next to a submission that has
		// not been judged yet.
		//
		// WHERE status = 'rejected' is what keeps this from being a way to
		// silently overwrite an approved registration, or to reset one that is
		// mid-review: those still raise the unique violation below.
		created, err = scanRegistration(tx.QueryRow(ctx, `
			INSERT INTO registrations (tenant_id, country, object_key, fields, external_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, country, object_key) DO UPDATE
			   SET fields           = EXCLUDED.fields,
			       -- A resubmission carries whatever id the NEW submission
			       -- supplied, including none. Never the previously approved
			       -- value carried forward silently.
			       external_id      = EXCLUDED.external_id,
			       status           = 'pending_review',
			       rejection_reason = NULL,
			       updated_at       = now()
			   WHERE registrations.status = 'rejected'
			RETURNING `+registrationColumns,
			id.TenantID, registration.Country, registration.ObjectKey, encoded,
			registration.ExternalID))
		// A DO UPDATE whose WHERE excludes the row returns NO rows rather than
		// raising — so without this, resubmitting over a pending or approved
		// registration would surface as a scan error instead of the conflict it
		// actually is.
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	})
	if errors.Is(err, ErrConflict) {
		return Registration{}, ErrConflict
	}
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

// ClearSenderVoiceCode discards a pending verification code.
//
// Called when someone enters the wrong one. The code is six digits, and leaving
// it valid after a failed attempt makes it brute-forceable by anyone holding a
// session: a million guesses is minutes of scripted requests, and the prize is
// the right to place calls under a number you do not own. Burning it on the
// first miss costs an honest user one extra call and costs an attacker the
// whole attack.
func ClearSenderVoiceCode(ctx context.Context, pool *pgxpool.Pool, id Identity,
	senderID uuid.UUID) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE sender_ids SET voice_code = NULL WHERE id = $1`, senderID)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: clear voice code: %w", err)
	}
	return nil
}

// SaveCarrierTemplateRegistration records what a carrier said about one of our
// templates — either the id it issued when we submitted it, or a code the
// customer obtained in the carrier's own portal.
//
// Scoped to the tenant like every other template write. The webhook path, which
// arrives with no tenant at all, uses ApplyCarrierTemplateStatus instead.
func SaveCarrierTemplateRegistration(ctx context.Context, pool *pgxpool.Pool, id Identity,
	templateID uuid.UUID, vendor, carrierTemplateID, status, rejectionReason string) (Template, error) {

	var updated Template
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		updated, err = scanTemplate(tx.QueryRow(ctx, `
			UPDATE templates
			   SET carrier_vendor           = $2,
			       carrier_template_id      = $3,
			       carrier_status           = $4,
			       carrier_rejection_reason = NULLIF($5, ''),
			       carrier_submitted_at     = COALESCE(carrier_submitted_at, now()),
			       carrier_updated_at       = now()
			 WHERE id = $1
			RETURNING `+templateColumns,
			templateID, vendor, carrierTemplateID, status, rejectionReason))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	// Two tenants cannot hold the same carrier template id: the unique index
	// exists so a status webhook, which matches on that id alone, can never
	// deliver one tenant's approval to another.
	if isUniqueViolation(err) {
		return Template{}, ErrConflict
	}
	if err != nil {
		return Template{}, fmt.Errorf("store: save carrier template registration: %w", err)
	}
	return updated, nil
}

// ApplyCarrierTemplateStatus records an approval or rejection that arrived on a
// carrier webhook.
//
// It runs on the OPERATOR pool, not a tenant one, because the payload carries
// the carrier's template id and nothing else — there is no tenant to scope to
// until the row itself is found. That is exactly why the (vendor, id) pair is
// unique: without it this would be a cross-tenant write with a guessable key.
//
// Returns ErrNotFound for an id we do not hold. Callers must treat that as an
// ordinary outcome and still answer the carrier 200: a webhook for a template
// registered by some other system is not our error to retry forever.
func ApplyCarrierTemplateStatus(ctx context.Context, operatorPool *pgxpool.Pool,
	vendor, carrierTemplateID, status, rejectionReason string) (uuid.UUID, error) {

	var templateID uuid.UUID
	err := operatorPool.QueryRow(ctx, `
		UPDATE templates
		   SET carrier_status           = $3,
		       carrier_rejection_reason = NULLIF($4, ''),
		       carrier_updated_at       = now()
		 WHERE carrier_vendor = $1 AND carrier_template_id = $2
		RETURNING id`,
		vendor, carrierTemplateID, status, rejectionReason).Scan(&templateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: apply carrier template status: %w", err)
	}
	return templateID, nil
}
