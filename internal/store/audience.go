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

type ContactList struct {
	ID              uuid.UUID
	Name            string
	ContactCount    int
	ConsentedCounts map[string]int
	CreatedAt       time.Time
}

// listWithCounts reads lists together with their membership and per-channel
// consent tallies. The counts come from a lateral join rather than N+1 queries:
// the audience screen renders every list at once.
const listWithCounts = `
	SELECT l.id, l.name, l.created_at,
	       coalesce(m.total, 0),
	       coalesce(m.consented, '{}'::jsonb)
	FROM contact_lists l
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
	        WHERE cm.list_id = l.id
	    ) counted
	) m ON true`

func scanList(row pgx.Row) (ContactList, error) {
	var list ContactList
	var consented []byte
	if err := row.Scan(&list.ID, &list.Name, &list.CreatedAt, &list.ContactCount,
		&consented); err != nil {
		return ContactList{}, err
	}
	list.ConsentedCounts = map[string]int{}
	if len(consented) > 0 {
		_ = json.Unmarshal(consented, &list.ConsentedCounts)
	}
	return list, nil
}

func ListContactLists(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]ContactList, error) {
	var out []ContactList
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listWithCounts+` ORDER BY l.created_at DESC`)
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
		return nil, fmt.Errorf("store: list contact lists: %w", err)
	}
	return out, nil
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
}

func scanContact(row pgx.Row) (Contact, error) {
	var contact Contact
	var fields, consent, consentedAt []byte
	if err := row.Scan(&contact.ID, &contact.Msisdn, &contact.Email, &contact.Country,
		&fields, &consent, &consentedAt, &contact.CreatedAt, &contact.UpdatedAt); err != nil {
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
	c.consented_at, c.created_at, c.updated_at`

// ListContacts pages contacts, optionally restricted to one list. Total is
// returned alongside because the audience screen shows "1–50 of 12,480".
func ListContacts(ctx context.Context, pool *pgxpool.Pool, id Identity,
	listID *uuid.UUID, cursor string, limit int) ([]Contact, int, string, error) {

	// An out-of-range limit falls back to 50 rather than being clamped to the
	// maximum, because a caller asking for something impossible has a bug and a
	// small page makes that obvious instead of silently serving a huge one.
	// The ceiling is high enough for the campaign fan-out to page in real
	// batches; user-supplied limits are bounded at the API layer.
	if limit <= 0 || limit > maxContactPage {
		limit = 50
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return nil, 0, "", err
	}

	var contacts []Contact
	var total int
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
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
			  AND ($2::timestamptz IS NULL OR (c.created_at, c.id) < ($2, $3))
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $4`, listID, cursorTime, cursorID, limit+1)
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
		return nil, 0, "", fmt.Errorf("store: list contacts: %w", err)
	}

	next := ""
	if len(contacts) > limit {
		next = encodeLedgerCursor(contacts[limit-1].CreatedAt, contacts[limit-1].ID)
		contacts = contacts[:limit]
	}
	return contacts, total, next, nil
}

// ImportRow is one CSV row after client-side normalisation.
type ImportRow struct {
	Msisdn    string
	Email     *string
	FirstName string
	LastName  string
	City      string
	Line      *int
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
			fields, err := json.Marshal(map[string]string{
				"firstName": row.FirstName, "lastName": row.LastName, "city": row.City,
			})
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
	cursor string, limit int) ([]Suppression, string, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	var out []Suppression
	var ids []uuid.UUID
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, identity, msisdn, email, reason, note, created_at
			FROM suppressions
			WHERE ($1::timestamptz IS NULL OR (created_at, id) < ($1, $2))
			ORDER BY created_at DESC, id DESC
			LIMIT $3`, cursorTime, cursorID, limit+1)
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
			ids = append(ids, rowID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("store: list suppressions: %w", err)
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].CreatedAt, ids[limit-1])
		out = out[:limit]
	}
	return out, next, nil
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
