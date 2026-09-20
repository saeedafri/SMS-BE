package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ContactList struct {
	// VariableMapping joins this spreadsheet's columns to a template's slots.
	VariableMapping map[string]string
	ID              uuid.UUID
	Name            string
	ContactCount    int
	ConsentedCounts map[string]int
	CreatedAt       time.Time

	// WaSessionActive is how many members are both opted in to WhatsApp and
	// still inside its 24-hour service window. Outside that window WhatsApp
	// only permits a pre-approved template, so this is the number that decides
	// what a campaign is allowed to send — see listWithCounts.
	WaSessionActive int
}

// listWithCounts reads lists together with their membership and per-channel
// consent tallies. The counts come from a lateral join rather than N+1 queries:
// the audience screen renders every list at once.
//
// waSessionActive is counted separately from the consent tallies above rather
// than folded into them. It answers a different question: not "may we message
// this person on WhatsApp at all" but "may we message them freely right now".
// WhatsApp lets a business send anything it likes for 24 hours after the
// customer's last interaction; after that only a pre-approved template. A
// contact who opted in a year ago is consented but not in session, so the two
// numbers legitimately differ and merging them would hide the distinction the
// campaign wizard exists to show.
const listWithCounts = `
	SELECT l.id, l.name, l.created_at,
	       coalesce(m.total, 0),
	       coalesce(m.consented, '{}'::jsonb),
	       coalesce(w.in_session, 0),
	       l.variable_mapping
	FROM contact_lists l
	LEFT JOIN LATERAL (
	    -- now() - 24 hours, evaluated per query rather than cached: the window
	    -- is measured from the customer's last interaction, so a contact drifts
	    -- out of it silently as time passes and the count must reflect that.
	    SELECT count(*) AS in_session
	    FROM contact_list_members cm
	    JOIN contacts c ON c.id = cm.contact_id
	    WHERE cm.list_id = l.id
	      AND c.consent ->> 'WHATSAPP' = 'opted_in'
	      -- IS NOT NULL rather than the jsonb ? operator: pgx would have to be
	      -- told that ? is not a parameter placeholder, and this reads the same.
	      AND c.consented_at ->> 'WHATSAPP' IS NOT NULL
	      AND (c.consented_at ->> 'WHATSAPP')::timestamptz > now() - interval '24 hours'
	) w ON true
	LEFT JOIN LATERAL (
	    -- count(DISTINCT c.id), NOT count(*). The lateral jsonb_each_text below
	    -- expands each contact into one row PER opted-in channel, so a contact
	    -- consented to both SMS and RCS produces two rows. Counting rows
	    -- reported 5 members for a 4-contact list, and the error scales with
	    -- how many channels a contact has opted into.
	    SELECT count(DISTINCT id) AS total,
	           jsonb_object_agg(channel, tally) FILTER (WHERE channel IS NOT NULL) AS consented
	    FROM (
	        SELECT c.id,
	               consent.key AS channel,
	               count(*) OVER (PARTITION BY consent.key) AS tally
	        FROM contact_list_members cm
	        JOIN contacts c ON c.id = cm.contact_id
	        LEFT JOIN LATERAL jsonb_each_text(c.consent) AS consent(key, value)
	             ON consent.value = 'opted_in'
	             -- Suppression beats consent, and it beats it HERE rather than
	             -- only at the send gate. Somebody the send path correctly
	             -- refuses was still being counted as reachable on the four
	             -- screens that show this number, so a list of 1,000 with 200
	             -- STOPs read as 1,000 consented right up until the campaign
	             -- report said otherwise.
	             --
	             -- An opt-in is usually OLDER than the STOP that followed it,
	             -- which is why the order is this way round and not the other:
	             -- a "never contact" is the more recent instruction and the
	             -- only one with a legal consequence for ignoring it.
	             AND NOT EXISTS (
	                 SELECT 1 FROM suppressions s
	                 WHERE s.identity = CASE WHEN consent.key = 'EMAIL'
	                                         THEN c.email ELSE c.msisdn END)
	        WHERE cm.list_id = l.id
	    ) counted
	) m ON true`

func scanList(row pgx.Row) (ContactList, error) {
	var list ContactList
	var consented, mapping []byte
	if err := row.Scan(&list.ID, &list.Name, &list.CreatedAt, &list.ContactCount,
		&consented, &list.WaSessionActive, &mapping); err != nil {
		return ContactList{}, err
	}
	list.ConsentedCounts = map[string]int{}
	if len(consented) > 0 {
		_ = json.Unmarshal(consented, &list.ConsentedCounts)
	}
	list.VariableMapping = map[string]string{}
	if len(mapping) > 0 {
		_ = json.Unmarshal(mapping, &list.VariableMapping)
	}
	return list, nil
}

// ListContactLists answers one page of a tenant's lists, with the total across
// every page.
func ListContactLists(ctx context.Context, pool *pgxpool.Pool, id Identity,
	page, limit int) ([]ContactList, int, error) {

	limit, offset := pageWindow(page, limit)
	var out []ContactList
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM contact_lists`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, listWithCounts+`
			ORDER BY l.created_at DESC, l.id DESC
			LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			list, err := scanList(rows)
			if err != nil {
				return err
			}
			out = append(out, list)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list contact lists: %w", err)
	}
	return out, total, nil
}

func GetContactList(ctx context.Context, pool *pgxpool.Pool, id Identity, listID uuid.UUID) (ContactList, error) {
	var list ContactList
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		list, err = scanList(tx.QueryRow(ctx, listWithCounts+` WHERE l.id = $1`, listID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ContactList{}, ErrNotFound
	}
	if err != nil {
		return ContactList{}, fmt.Errorf("store: get contact list: %w", err)
	}
	return list, nil
}

func CreateContactList(ctx context.Context, pool *pgxpool.Pool, id Identity, name string) (ContactList, error) {
	var listID uuid.UUID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO contact_lists (tenant_id, name) VALUES ($1, $2) RETURNING id`,
			id.TenantID, name).Scan(&listID)
	})
	if isUniqueViolation(err) {
		return ContactList{}, ErrConflict
	}
	if err != nil {
		return ContactList{}, fmt.Errorf("store: create contact list: %w", err)
	}
	return GetContactList(ctx, pool, id, listID)
}

func RenameContactList(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID uuid.UUID, name string) (ContactList, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE contact_lists SET name = $1 WHERE id = $2`, name, listID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ContactList{}, ErrNotFound
	}
	if isUniqueViolation(err) {
		return ContactList{}, ErrConflict
	}
	if err != nil {
		return ContactList{}, fmt.Errorf("store: rename contact list: %w", err)
	}
	return GetContactList(ctx, pool, id, listID)
}

// DeleteContactList removes the list and its membership rows, but never the
// contacts themselves — a contact usually belongs to several lists, and
// deleting people because one segment was tidied up would be destructive.
func DeleteContactList(ctx context.Context, pool *pgxpool.Pool, id Identity, listID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM contact_lists WHERE id = $1`, listID)
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
		return fmt.Errorf("store: delete contact list: %w", err)
	}
	return nil
}

func RemoveContactListMember(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID, contactID uuid.UUID) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM contact_list_members WHERE list_id = $1 AND contact_id = $2`,
			listID, contactID)
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
		return fmt.Errorf("store: remove list member: %w", err)
	}
	return nil
}

type Contact struct {
	ID          uuid.UUID
	Msisdn      string
	Email       *string
	Country     string
	Fields      map[string]string
	Consent     map[string]string
	ConsentedAt map[string]time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// PhoneSuppressed and EmailSuppressed are read from the suppression list,
	// so they are always as current as the list the send gate checks.
	PhoneSuppressed bool
	EmailSuppressed bool
}

func scanContact(row pgx.Row) (Contact, error) {
	var contact Contact
	var fields, consent, consentedAt []byte
	if err := row.Scan(&contact.ID, &contact.Msisdn, &contact.Email, &contact.Country,
		&fields, &consent, &consentedAt, &contact.CreatedAt, &contact.UpdatedAt,
		&contact.PhoneSuppressed, &contact.EmailSuppressed); err != nil {
		return Contact{}, err
	}
	contact.Fields = map[string]string{}
	contact.Consent = map[string]string{}
	_ = json.Unmarshal(fields, &contact.Fields)
	_ = json.Unmarshal(consent, &contact.Consent)
	if len(consentedAt) > 0 {
		contact.ConsentedAt = map[string]time.Time{}
		_ = json.Unmarshal(consentedAt, &contact.ConsentedAt)
	}
	return contact, nil
}

const contactColumns = `c.id, c.msisdn, c.email, c.country, c.fields, c.consent,
	c.consented_at, c.created_at, c.updated_at,
	EXISTS (SELECT 1 FROM suppressions s WHERE s.identity = c.msisdn),
	c.email IS NOT NULL AND EXISTS (SELECT 1 FROM suppressions s WHERE s.identity = c.email)`

// ListContacts pages contacts, optionally restricted to one list. Total is
// returned alongside because the audience screen shows "1–50 of 12,480".
// ListContactsAfter pages a contact list by keyset, for the campaign fan-out.
//
// Separate from ListContacts, which the API uses and which pages by number.
// See encodeDispatchCursor for why the send path does not share it.
func ListContactsAfter(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, channel, cursor string, limit int) ([]Contact, string, error) {

	if limit <= 0 || limit > maxContactPage {
		limit = 50
	}
	cursorTime, cursorID, err := decodeDispatchCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	var contacts []Contact
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+contactColumns+` FROM contacts c
			WHERE ($1::uuid IS NULL OR EXISTS (
			        SELECT 1 FROM contact_list_members m
			        WHERE m.contact_id = c.id AND m.list_id = $1))
			  AND ($4::timestamptz IS NULL OR (c.created_at, c.id) < ($4, $5))`+
			reachableOnChannel+`
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $6`, listID, channel == "EMAIL", channel, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			contact, err := scanContact(rows)
			if err != nil {
				return err
			}
			contacts = append(contacts, contact)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("store: list contacts after: %w", err)
	}

	next := ""
	if len(contacts) > limit {
		next = encodeDispatchCursor(contacts[limit-1].CreatedAt, contacts[limit-1].ID)
		contacts = contacts[:limit]
	}
	return contacts, next, nil
}

func ListContacts(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, page, limit int) ([]Contact, int, error) {

	// An out-of-range limit falls back to 50 rather than being clamped to the
	// maximum, because a caller asking for something impossible has a bug and a
	// small page makes that obvious instead of silently serving a huge one.
	// The ceiling is high enough for the campaign fan-out to page in real
	// batches; user-supplied limits are bounded at the API layer.
	if limit <= 0 || limit > maxContactPage {
		limit = 50
	}
	offset := pageOffset(page, limit)

	var contacts []Contact
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM contacts c
			WHERE ($1::uuid IS NULL OR EXISTS (
			    SELECT 1 FROM contact_list_members m
			    WHERE m.contact_id = c.id AND m.list_id = $1))`, listID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+contactColumns+` FROM contacts c
			WHERE ($1::uuid IS NULL OR EXISTS (
			        SELECT 1 FROM contact_list_members m
			        WHERE m.contact_id = c.id AND m.list_id = $1))
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $2 OFFSET $3`, listID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			contact, err := scanContact(rows)
			if err != nil {
				return err
			}
			contacts = append(contacts, contact)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list contacts: %w", err)
	}
	return contacts, total, nil
}

// ImportRow is one CSV row after client-side normalisation.
//
// Fields carries the customer's WHOLE row, keyed by their file's own header
// text — "First Name", "Order ID", "Loyalty Tier". It used to be three fixed
// names, so a column the customer actually had could never reach a template.
type ImportRow struct {
	Msisdn string
	Email  *string
	Fields map[string]string
	Line   *int
}

// filledFields drops the keys a row left blank, so a merge cannot erase.
//
// Whitespace-only counts as blank, matching audience.ResolveField: a value that
// would not be substituted is not a value worth storing over one that would.
func filledFields(fields map[string]string) map[string]string {
	kept := make(map[string]string, len(fields))
	for key, value := range fields {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			kept[key] = trimmed
		}
	}
	return kept
}

// ImportOutcome reports what happened to each row.
type ImportOutcome struct {
	Created   int
	Updated   int
	Skipped   int
	Invalid   int
	ListID    uuid.UUID
	Conflicts []ImportConflict
}

type ImportConflict struct {
	Line   *int
	Msisdn string
	Email  *string
	Reason string
}

// ImportContacts upserts rows and adds them to a list, all in one transaction.
// A partially-applied import would leave the user unable to tell what landed.
func ImportContacts(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID uuid.UUID, country string, consent map[string]string,
	rows []ImportRow) (ImportOutcome, error) {

	outcome := ImportOutcome{ListID: listID, Conflicts: []ImportConflict{}}
	consentJSON, err := json.Marshal(consent)
	if err != nil {
		return ImportOutcome{}, fmt.Errorf("store: encode consent: %w", err)
	}

	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		for _, row := range rows {
			// Only the keys this row actually carried, blanks dropped.
			//
			// The three names used to be marshalled unconditionally, so a file
			// with no name column sent "firstName": "" — and `||` is a
			// right-biased merge, so the blank won and every stored name was
			// replaced with an empty string. No error, no count, nothing on
			// screen; the customer found out when a campaign read "Dear ,".
			//
			// A blank cell in a spreadsheet means "I did not fill this in",
			// never "delete what you know". Clearing a value deliberately is
			// what PATCH /v1/contacts/{id} is for, where fields REPLACES.
			fields, err := json.Marshal(filledFields(row.Fields))
			if err != nil {
				return err
			}

			var contactID uuid.UUID
			var inserted bool
			// xmax = 0 identifies a freshly inserted row, distinguishing a
			// create from an update without a second round trip.
			if err := tx.QueryRow(ctx, `
				INSERT INTO contacts (tenant_id, msisdn, email, country, fields, consent)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (tenant_id, msisdn) DO UPDATE SET
				    email = coalesce(excluded.email, contacts.email),
				    fields = contacts.fields || excluded.fields,
				    consent = contacts.consent || excluded.consent,
				    updated_at = now()
				RETURNING id, (xmax = 0)`,
				id.TenantID, row.Msisdn, row.Email, country, fields, consentJSON,
			).Scan(&contactID, &inserted); err != nil {
				return err
			}
			if inserted {
				outcome.Created++
			} else {
				outcome.Updated++
			}

			if _, err := tx.Exec(ctx,
				`INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
				 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				listID, contactID, id.TenantID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ImportOutcome{}, fmt.Errorf("store: import contacts: %w", err)
	}
	return outcome, nil
}

// FindOrCreateIdempotentResponse returns a stored response for a key, or
// records a new one. A resubmitted import must not run twice: duplicate
// contacts mean duplicate sends and duplicate charges.
func FindIdempotentResponse(ctx context.Context, pool *pgxpool.Pool, id Identity,
	scope, key string) ([]byte, bool, error) {

	var stored []byte
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT response FROM idempotency_keys WHERE scope = $1 AND key = $2`,
			scope, key).Scan(&stored)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: find idempotency key: %w", err)
	}
	return stored, true, nil
}

func SaveIdempotentResponse(ctx context.Context, pool *pgxpool.Pool, id Identity,
	scope, key string, response []byte) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO idempotency_keys (tenant_id, scope, key, response)
			 VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			id.TenantID, scope, key, response)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: save idempotency key: %w", err)
	}
	return nil
}

type Suppression struct {
	Identity  string
	Msisdn    *string
	Email     *string
	Reason    string
	Note      string
	CreatedAt time.Time
}

func ListSuppressions(ctx context.Context, pool *pgxpool.Pool, id Identity,
	page, limit int) ([]Suppression, int, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := pageOffset(page, limit)

	var out []Suppression
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Counted without the page: this is the footer's denominator, and one
		// that shrank as the reader paged would be worse than none.
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM suppressions`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, identity, msisdn, email, reason, note, created_at
			FROM suppressions
			ORDER BY created_at DESC, id DESC
			LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var suppression Suppression
			var rowID uuid.UUID
			if err := rows.Scan(&rowID, &suppression.Identity, &suppression.Msisdn,
				&suppression.Email, &suppression.Reason, &suppression.Note,
				&suppression.CreatedAt); err != nil {
				return err
			}
			out = append(out, suppression)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list suppressions: %w", err)
	}
	return out, total, nil
}

// AddSuppression records an opt-out. Re-suppressing an identity is a no-op
// rather than an error: the desired end state already holds.
func AddSuppression(ctx context.Context, pool *pgxpool.Pool, id Identity,
	suppression Suppression) (created bool, err error) {

	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO suppressions (tenant_id, identity, msisdn, email, reason, note)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, identity) DO NOTHING`,
			id.TenantID, suppression.Identity, suppression.Msisdn, suppression.Email,
			suppression.Reason, suppression.Note)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: add suppression: %w", err)
	}
	return created, nil
}

func RemoveSuppression(ctx context.Context, pool *pgxpool.Pool, id Identity, identity string) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM suppressions WHERE identity = $1`, identity)
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
		return fmt.Errorf("store: remove suppression: %w", err)
	}
	return nil
}

// IsSuppressed is what the send path will call before every message. It lives
// here now so Stage 5 has it ready and so suppression is testable today.
func IsSuppressed(ctx context.Context, pool *pgxpool.Pool, id Identity, identity string) (bool, error) {
	var suppressed bool
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM suppressions WHERE identity = $1)`,
			identity).Scan(&suppressed)
	})
	if err != nil {
		return false, fmt.Errorf("store: is suppressed: %w", err)
	}
	return suppressed, nil
}

// maxContactPage bounds one page of contacts. The campaign fan-out pages at
// batchSize; anything above this is a caller error rather than a big request.
const maxContactPage = 1000

// SuppressedSet returns which of the given identities are suppressed.
//
// The batched send path checks a whole page of recipients in one query rather
// than one query per recipient. At campaign scale that is the difference
// between 200 round trips and one, and the suppression check runs on every
// single message so it is on the hottest path in the system.
func SuppressedSet(ctx context.Context, pool *pgxpool.Pool, id Identity,
	identities []string) (map[string]bool, error) {

	suppressed := make(map[string]bool, len(identities))
	if len(identities) == 0 {
		return suppressed, nil
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT identity FROM suppressions WHERE identity = ANY($1)`, identities)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var identity string
			if err := rows.Scan(&identity); err != nil {
				return err
			}
			suppressed[identity] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: suppressed set: %w", err)
	}
	return suppressed, nil
}

// ReachableOnChannel counts the contacts in a list who can actually be reached
// on a channel.
//
// Two things have to hold, and the campaign estimate used to check neither: the
// contact must have the identity that channel is addressed by — an email
// address for Email, a phone number for everything else — and they must have
// opted in on it.
//
// Counting everyone instead quoted an Email campaign for the whole list, so a
// list of 1,000 contacts of whom 40 had an email address was priced, approved
// and launched as 1,000 sends. The customer sees a number they did not agree
// to and a delivery rate that looks catastrophic.
// reachableOnChannel is the audience rule: addressable on this channel AND
// explicitly opted in on it.
//
// One fragment rather than three copies because the estimate, the fan-out and
// the recipients endpoint must agree on who a campaign's audience is. They did
// not: the estimate counted only opted-in contacts and the fan-out paged the
// list with no consent predicate at all, so a campaign could quote zero
// recipients and then send to every one of them. Measured on production before
// this was written — 2,500 contacts with no SMS consent, a campaign quoting 0,
// and 2,500 messages dispatched.
//
// $1 is the list id, $2 the by-email flag, $3 the channel.
const reachableOnChannel = `
	  AND CASE WHEN $2::boolean
	           THEN c.email IS NOT NULL AND c.email <> ''
	           ELSE c.msisdn IS NOT NULL AND c.msisdn <> ''
	      END
	  -- Consent is per channel and stored as a jsonb map. A channel the contact
	  -- has never answered on is 'unknown', which is not consent: only an
	  -- explicit opt-in counts.
	  AND coalesce(c.consent ->> $3, '') = 'opted_in'
	  -- And suppression overrides all of it. Without this the estimate quoted
	  -- for people the gate then refused one by one: the customer approved a
	  -- number, was charged nothing for the refusals, and read a delivery rate
	  -- that counted them as failures.
	  AND NOT EXISTS (
	      SELECT 1 FROM suppressions s
	      WHERE s.identity = CASE WHEN $2::boolean THEN c.email ELSE c.msisdn END)`

func ReachableOnChannel(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, channel string) (int, error) {

	// Email is the only channel addressed by an address rather than a number.
	// Written as a flag rather than a channel comparison inside the SQL so the
	// day a second such channel appears, this is the one line that changes.
	byEmail := channel == "EMAIL"

	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM contacts c
			WHERE ($1::uuid IS NULL OR EXISTS (
			        SELECT 1 FROM contact_list_members m
			        WHERE m.contact_id = c.id AND m.list_id = $1))
			  `+reachableOnChannel, listID, byEmail, channel).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("store: count reachable contacts: %w", err)
	}
	return total, nil
}

// SuppressedOnChannel counts the contacts a list would otherwise reach on a
// channel who have opted out of being contacted at all.
//
// It exists so a reduced recipient count has a reason printed beside it.
// ReachableOnChannel now subtracts these people, and a recipient total that
// drops with nothing explaining the drop reads as a bug in the estimate — the
// customer approves a smaller number than the list they just uploaded and has
// no way to tell whether it is right.
//
// Counted with the same consent and addressability rules, so the two numbers
// partition the same audience rather than overlapping it.
func SuppressedOnChannel(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, channel string) (int, error) {

	byEmail := channel == "EMAIL"
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM contacts c
			WHERE ($1::uuid IS NULL OR EXISTS (
			        SELECT 1 FROM contact_list_members m
			        WHERE m.contact_id = c.id AND m.list_id = $1))
			  AND CASE WHEN $2::boolean
			           THEN c.email IS NOT NULL AND c.email <> ''
			           ELSE c.msisdn IS NOT NULL AND c.msisdn <> ''
			      END
			  AND coalesce(c.consent ->> $3, '') = 'opted_in'
			  AND EXISTS (
			      SELECT 1 FROM suppressions s
			      WHERE s.identity = CASE WHEN $2::boolean THEN c.email ELSE c.msisdn END)`,
			listID, byEmail, channel).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("store: count suppressed contacts: %w", err)
	}
	return total, nil
}

// ContactIDsForIdentities maps each address to the contact that holds it, for
// the identities on one page.
//
// The message log stores the address a campaign sent to rather than a contact
// id, so this is what turns a dispatched message back into the contact the
// screen links to. Bounded by the page rather than by the campaign: the
// alternative is loading every contact a 100,000-recipient campaign touched in
// order to annotate twenty rows.
func ContactIDsForIdentities(ctx context.Context, pool *pgxpool.Pool, id Identity,
	identities []string) (map[string]uuid.UUID, error) {

	out := map[string]uuid.UUID{}
	if len(identities) == 0 {
		return out, nil
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, msisdn, coalesce(email, '') FROM contacts
			WHERE msisdn = ANY($1) OR email = ANY($1)`, identities)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var contactID uuid.UUID
			var msisdn, email string
			if err := rows.Scan(&contactID, &msisdn, &email); err != nil {
				return err
			}
			if msisdn != "" {
				out[msisdn] = contactID
			}
			if email != "" {
				out[email] = contactID
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: contact ids for identities: %w", err)
	}
	return out, nil
}

// ContactFilter narrows a contact listing by the customer's own columns.
//
// A column is "msisdn", "email", or any key of the fields map. An unknown
// column matches nothing and is NOT an error: a list can lose a column between
// one import and the next, and a 4xx there would break a bookmarked URL.
type ContactFilter map[string]string

// contactFilterSQL builds the WHERE fragment and arguments for a filter.
//
// Every term is a case-insensitive substring and they AND together. The column
// name is never interpolated — it is passed as a parameter and looked up inside
// the jsonb, so a customer whose header row says `'; DROP` is a column name and
// not a problem.
func contactFilterSQL(filter ContactFilter, args *[]any) string {
	if len(filter) == 0 {
		return ""
	}
	// Sorted so the same filter always produces the same statement, which is
	// what lets Postgres reuse a plan and what makes a slow query log readable.
	columns := make([]string, 0, len(filter))
	for column := range filter {
		columns = append(columns, column)
	}
	sort.Strings(columns)

	var clauses strings.Builder
	for _, column := range columns {
		value := filter[column]
		switch column {
		case "msisdn":
			*args = append(*args, value)
			fmt.Fprintf(&clauses, " AND c.msisdn ILIKE '%%' || $%d || '%%'", len(*args))
		case "email":
			*args = append(*args, value)
			fmt.Fprintf(&clauses, " AND c.email ILIKE '%%' || $%d || '%%'", len(*args))
		default:
			*args = append(*args, column, value)
			fmt.Fprintf(&clauses,
				" AND c.fields ->> $%d ILIKE '%%' || $%d || '%%'", len(*args)-1, len(*args))
		}
	}
	return clauses.String()
}

// FilterContacts pages contacts under a filter, and reports the total and the
// column names over the WHOLE filtered collection.
//
// Total and fieldNames are both computed over everything matching, never over
// the page: a total summed from the page is a count of the page, and a column
// whose only values sit on page 84 would vanish for a reader on page 1 and
// reappear later, which looks like data loss.
func FilterContacts(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, filter ContactFilter, page, limit int) (
	[]Contact, int, []string, error) {

	if limit <= 0 || limit > maxContactPage {
		limit = 50
	}
	offset := pageOffset(page, limit)

	where := `WHERE ($1::uuid IS NULL OR EXISTS (
	              SELECT 1 FROM contact_list_members m
	              WHERE m.contact_id = c.id AND m.list_id = $1))`
	args := []any{listID}
	where += contactFilterSQL(filter, &args)

	var contacts []Contact
	var total int
	names := []string{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM contacts c `+where, args...).Scan(&total); err != nil {
			return err
		}
		// Every distinct key across the filtered set. jsonb_object_keys over the
		// matching rows rather than over the page.
		keys, err := tx.Query(ctx,
			`SELECT DISTINCT k FROM contacts c, jsonb_object_keys(c.fields) k `+where+
				` ORDER BY k`, args...)
		if err != nil {
			return err
		}
		for keys.Next() {
			var name string
			if err := keys.Scan(&name); err != nil {
				keys.Close()
				return err
			}
			names = append(names, name)
		}
		keys.Close()
		if err := keys.Err(); err != nil {
			return err
		}

		paged := append(append([]any{}, args...), limit, offset)
		rows, err := tx.Query(ctx,
			`SELECT `+contactColumns+` FROM contacts c `+where+
				fmt.Sprintf(` ORDER BY c.created_at DESC, c.id DESC LIMIT $%d OFFSET $%d`,
					len(paged)-1, len(paged)), paged...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			contact, err := scanContact(rows)
			if err != nil {
				return err
			}
			contacts = append(contacts, contact)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, nil, fmt.Errorf("store: filter contacts: %w", err)
	}
	return contacts, total, names, nil
}

// ContactUpdate is the change PATCH /v1/contacts/{id} applies. A nil pointer
// means the caller did not mention the field; the distinction matters because
// email's own null CLEARS it.
type ContactUpdate struct {
	Msisdn *string
	// Email is a two-level pointer on purpose: nil means "not mentioned",
	// a pointer to nil means "clear it", and a pointer to a value sets it.
	Email  **string
	Fields map[string]string
}

// UpdateContact corrects one contact.
//
// Fields REPLACES the stored map rather than merging. A merge cannot express a
// deletion, and a customer who empties a box on screen means it — which is the
// mirror of the import, where a blank cell means "not filled in" and must NOT
// erase. The two doors mean opposite things by a blank, deliberately.
//
// ErrConflict when the new number or address already belongs to another
// contact, with NOTHING written: the uniqueness is enforced by the index inside
// the same statement rather than by a check above it, so a guard cannot store
// the bad value and then report an error about it.
func UpdateContact(ctx context.Context, pool *pgxpool.Pool, id Identity,
	contactID uuid.UUID, change ContactUpdate) (Contact, error) {

	var contact Contact
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{contactID}
		if change.Msisdn != nil {
			args = append(args, *change.Msisdn)
			sets = append(sets, fmt.Sprintf("msisdn = $%d", len(args)))
		}
		if change.Email != nil {
			args = append(args, *change.Email)
			sets = append(sets, fmt.Sprintf("email = $%d", len(args)))
		}
		if change.Fields != nil {
			encoded, err := json.Marshal(filledFields(change.Fields))
			if err != nil {
				return err
			}
			args = append(args, encoded)
			sets = append(sets, fmt.Sprintf("fields = $%d", len(args)))
		}

		row := tx.QueryRow(ctx, `
			UPDATE contacts c SET `+strings.Join(sets, ", ")+`
			WHERE c.id = $1
			RETURNING `+contactColumns, args...)
		updated, err := scanContact(row)
		if isUniqueViolation(err) {
			return ErrConflict
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		contact = updated
		return nil
	})
	if err != nil {
		return Contact{}, err
	}
	return contact, nil
}

// SetListVariableMapping records which column feeds which template slot.
func SetListVariableMapping(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID uuid.UUID, mapping map[string]string) error {

	encoded, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE contact_lists SET variable_mapping = $2 WHERE id = $1`, listID, encoded)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListVariableMapping reads one list's mapping. The fan-out needs it for every
// page it walks, so it is its own small read rather than part of a wider one.
func ListVariableMapping(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID uuid.UUID) (map[string]string, error) {

	mapping := map[string]string{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx,
			`SELECT variable_mapping FROM contact_lists WHERE id = $1`, listID).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		return json.Unmarshal(raw, &mapping)
	})
	if err != nil {
		return nil, err
	}
	return mapping, nil
}

// ConsentRecord is one declaration that a list's contacts agreed to a channel.
type ConsentRecord struct {
	ListID      uuid.UUID
	Channel     string
	State       string
	OnlyUnknown bool
	Declaration string
	RecordedBy  string
	Updated     int
	RecordedAt  time.Time
}

// RecordConsent writes consent for every contact in a list, and files the
// declaration that says who claimed it.
//
// onlyUnknown is the safe default and the one that matters: a person who has
// explicitly opted OUT is not opted back in because somebody ticked a box about
// a list they happen to be on. Only "unknown" is filled.
//
// consented_at is stamped only where the state NEWLY becomes opted_in. The
// session clocks — the WhatsApp 24h window — start at that moment, and
// re-stamping somebody already opted in would restart a clock that never
// stopped.
//
// Consent belongs to the PERSON, not to the membership: opting in the members
// of one list opts those same people in wherever else they appear, because a
// person either agreed or did not.
func RecordConsent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	record ConsentRecord) (ConsentRecord, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM contact_lists WHERE id = $1)`,
			record.ListID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}

		// Only rows whose state actually CHANGES are touched, so `updated`
		// counts people rather than attempts and an unchanged contact keeps the
		// timestamp it already had. RowsAffected is that count — deriving it
		// afterwards by counting everyone now in the state would include the
		// ones who were already there, and report a number nobody did.
		tag, err := tx.Exec(ctx, `
			UPDATE contacts c
			SET consent = c.consent || jsonb_build_object($2::text, $3::text),
			    consented_at = CASE
			        WHEN $3 = 'opted_in'
			        THEN c.consented_at || jsonb_build_object($2::text, to_jsonb(now()))
			        ELSE c.consented_at
			    END,
			    updated_at = now()
			WHERE EXISTS (SELECT 1 FROM contact_list_members m
			              WHERE m.contact_id = c.id AND m.list_id = $1)
			  AND coalesce(c.consent ->> $2, 'unknown') <> $3
			  AND (NOT $4::boolean OR coalesce(c.consent ->> $2, 'unknown') = 'unknown')`,
			record.ListID, record.Channel, record.State, record.OnlyUnknown)
		if err != nil {
			return err
		}
		record.Updated = int(tag.RowsAffected())

		return tx.QueryRow(ctx, `
			INSERT INTO contact_consent_records
			    (tenant_id, list_id, channel, state, only_unknown, declaration, updated, recorded_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING recorded_at`,
			id.TenantID, record.ListID, record.Channel, record.State, record.OnlyUnknown,
			record.Declaration, record.Updated, record.RecordedBy).Scan(&record.RecordedAt)
	})
	if err != nil {
		return ConsentRecord{}, err
	}
	return record, nil
}

// GetContact reads one contact. The edit path needs the country before it can
// normalise a phone number, because "98765 00011" is only a number once you
// know where it is.
func GetContact(ctx context.Context, pool *pgxpool.Pool, id Identity,
	contactID uuid.UUID) (Contact, error) {

	var contact Contact
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+contactColumns+` FROM contacts c WHERE c.id = $1`, contactID)
		found, err := scanContact(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		contact = found
		return nil
	})
	return contact, err
}
