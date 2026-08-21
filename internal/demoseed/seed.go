// Package demoseed builds the demo tenant the frontend's browser suite expects.
//
// The suite's 43 specs are written against a fixed fixture — founder@acme.test,
// org "Acme Retail", specific senders, templates and campaigns — that lived
// only in MSW's memory. Identifiers and names here are copied from
// ../SMS-UI/src/mocks/*-state.ts because the specs assert on them directly.
package demoseed

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"
)

const (
	// TenantID is exported because the dev test hooks fall back to it when a
	// request arrives without a session — see internal/api/dev.go.
	TenantID = "88888888-8888-8888-8888-888888888888"
	tenantID = TenantID
	// UserID is exported for the same reason as TenantID: the role hook needs a
	// subject when a spec calls it without a session.
	UserID       = "99999999-9999-9999-9999-999999999999"
	userID       = UserID
	operatorID   = "0b0e7a10-0000-4000-8000-00000000000b"
	smsID        = "11111111-1111-1111-1111-111111111111"
	rcsID        = "22222222-2222-2222-2222-222222222222"
	pendingSMSID = "33333333-4444-4444-4444-444444444444"
	whatsappID   = "55555555-5555-5555-5555-555555555555"
	emailID      = "66666666-6666-6666-6666-666666666666"
	voiceID      = "77777777-7777-7777-7777-777777777777"
	// These ids are NOT arbitrary. The frontend's browser specs hardcode them —
	// they navigate straight to /campaigns/ca000001-… and post dev hooks against
	// a known contact — because they were written against MSW fixtures that use
	// exactly these values. A seed with freshly generated uuids leaves every one
	// of those specs looking at a 404, which reads as a broken feature rather
	// than a missing fixture. Keep them in step with src/mocks/*-state.ts.
	listID        = "11110000-0000-0000-0000-000000000001"
	smsTemplate   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	otpTemplate   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	rcsTemplate   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	campaignOne   = "ca000001-0000-0000-0000-000000000001"
	campaignTwo   = "ca000002-0000-0000-0000-000000000002"
	campaignThree = "ca000003-0000-0000-0000-000000000003"
	// NOT the frontend's ids. Their fixtures use "jo000001-…" and "cv000003-…",
	// which are not valid uuids — j, o and v are not hex digits. MSW never
	// noticed because it does not validate the format, but the contract declares
	// these fields as format: uuid and Postgres stores them in a uuid column,
	// so those literals cannot exist in any spec-compliant backend. These are
	// the closest valid equivalents; the specs that hardcode the originals need
	// the frontend fixtures corrected before they can pass.
	journeyID         = "10000001-0000-0000-0000-000000228853"
	conversationOne   = "cf000003-0000-0000-0000-000000000003"
	conversationTwo   = "cf000005-0000-0000-0000-000000000005"
	conversationThree = "cf000006-0000-0000-0000-000000000006"
	conversationFour  = "cf000007-0000-0000-0000-000000000007"
	verifyService     = "ffffffff-0000-0000-0000-000000000001"
	password          = "relay-dev"
)

// Apply rebuilds the demo tenant from scratch.
//
// It is used by the seed command AND by the /v1/dev/reset-mock-state test hook,
// which is the point of it living in a package rather than in main: the browser
// suite creates templates, campaigns and senders as it runs and never removes
// them, so without a real restore the second run of a spec hits a 409 on a name
// the first run already took. MSW got this for free by throwing away memory.
//
// The pool must be the migration role: this writes across tenants and briefly
// disables a trigger, neither of which the application role can do.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	return apply(ctx, pool, true)
}

// ApplyFixtureOnly rebuilds the Postgres fixture and leaves the warehouse alone.
//
// This is what the between-specs reset hook uses. Rewriting 30 days of ClickHouse
// history on every beforeEach AND afterEach costs seconds per test and pushes
// unrelated assertions past their timeout, and it buys nothing: no spec mutates
// message history, so the copy written once at seed time stays correct all run.
func ApplyFixtureOnly(ctx context.Context, pool *pgxpool.Pool) error {
	return apply(ctx, pool, false)
}

func apply(ctx context.Context, pool *pgxpool.Pool, includeHistory bool) error {

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	// Deleting the tenant cascades to everything owned by it, which is what
	// makes a re-run restore the fixture instead of colliding with it.
	//
	// That cascade reaches wallet_ledger, which is append-only and refuses the
	// DELETE. The trigger is CORRECT: in production a tenant with financial
	// history must not be erasable, because those records are needed for audit
	// and tax long after the customer has gone. Deleting a real tenant should
	// fail exactly like this.
	//
	// So the exception is made explicitly, narrowly, and only here — a dev
	// seed rebuilding a known fixture. It runs as the table owner, drops the
	// guard for one statement, and restores it immediately even if the delete
	// fails. Nothing on the request path can reach this code.
	// The demo tenant's live sessions are carried across the rebuild.
	//
	// Deleting the tenant cascades to `sessions`, which signs out whoever is
	// using the fixture right now. That is invisible and awful in a browser
	// suite: a spec that resets state halfway through is silently logged out,
	// and every assertion after it fails against a login page rather than the
	// screen under test. It reads as the feature being broken.
	//
	// Only the demo tenant's own rows are kept, and only their hashes — the
	// tokens themselves were never stored. Nothing is created here that did not
	// already exist a moment ago.
	// The two fixture devices below are deliberately excluded: they are
	// re-created further down with fresh random token hashes, and carrying them
	// as well would add two more on every reset until the security screen was a
	// wall of identical rows. Their hashes must stay random — a deterministic
	// one would be a token anybody could compute and sign in with.
	rows, err := pool.Query(ctx, `
		SELECT token_hash, device, browser, location, ip_address,
		       last_active_at, expires_at, revoked_at
		FROM sessions
		WHERE tenant_id = $1
		  AND location NOT IN ('Bengaluru, India', 'Delhi, India')`, tenantID)
	if err != nil {
		return fmt.Errorf("read sessions before rebuild: %w", err)
	}
	type liveSession struct {
		tokenHash                     []byte
		device, browser, location, ip string
		lastActiveAt, expiresAt       time.Time
		revokedAt                     *time.Time
	}
	var carried []liveSession
	for rows.Next() {
		var session liveSession
		if err := rows.Scan(&session.tokenHash, &session.device, &session.browser,
			&session.location, &session.ip, &session.lastActiveAt,
			&session.expiresAt, &session.revokedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan session before rebuild: %w", err)
		}
		carried = append(carried, session)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read sessions before rebuild: %w", err)
	}

	if _, err := pool.Exec(ctx,
		`ALTER TABLE wallet_ledger DISABLE TRIGGER wallet_ledger_append_only`); err != nil {
		return fmt.Errorf("relax ledger guard for seed: %w", err)
	}

	// The operator audit log is emptied too, and for the same reason it is
	// append-only in the first place.
	//
	// Deleting audit rows in production must be impossible, and the trigger
	// makes it so. But the console's own spec asserts the log opens on "No
	// audit events yet" before the operator does anything — and audit entries
	// for the OTHER seeded tenants are not reached by the tenant cascade, so
	// one earlier spec suspending Bluewave Retail leaves a row that makes that
	// assertion fail for every run afterwards.
	//
	// Same narrow exception as the ledger above: table owner, one statement,
	// guard restored immediately whether or not the delete succeeded, and
	// unreachable from anything on the request path.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE operator_audit_log DISABLE TRIGGER operator_audit_log_append_only`); err != nil {
		return fmt.Errorf("relax audit guard for seed: %w", err)
	}
	_, auditErr := pool.Exec(ctx, `DELETE FROM operator_audit_log`)
	if _, err := pool.Exec(ctx,
		`ALTER TABLE operator_audit_log ENABLE TRIGGER operator_audit_log_append_only`); err != nil {
		return fmt.Errorf("restore audit guard: %w", err)
	}
	if auditErr != nil {
		return fmt.Errorf("clear operator audit log: %w", auditErr)
	}
	// What the fixture does not own goes — but ONLY if it is itself a test
	// account.
	//
	// The specs sign up accounts with FIXED addresses (grace@newco.test), so the
	// first run creates them and every later run gets a 409 and never leaves the
	// signup page. That reads as a broken signup flow when it is really leftover
	// state, so the rebuild has to clear more than its own tenant.
	//
	// It used to clear EVERYTHING it did not recognise, which on a deployment
	// that also serves real traffic is a data-loss bug rather than a cleanup: a
	// real customer signing up on the demo had their tenant deleted by the next
	// test run. That happened — a live account created minutes earlier was gone,
	// and the signup that raced the delete died on the sessions foreign key.
	//
	// So a tenant now survives if ANY of its users has a real email address. A
	// reserved TLD (.test/.example/.invalid/.localhost/.internal, RFC 2606 and
	// 6761) cannot be registered, so "every user is at a reserved TLD" is a
	// reliable statement that nobody real is in there. Same test the auth code
	// uses to decide whether a fixed dev token may be issued.
	_, deleteErr := pool.Exec(ctx, `
		DELETE FROM tenants t
		 WHERE (t.id NOT IN (
			$1,
			'aaaaaaaa-1111-1111-1111-111111111111','bbbbbbbb-2222-2222-2222-222222222222',
			'cccccccc-3333-3333-3333-333333333333','dddddddd-4444-4444-4444-444444444444',
			'eeeeeeee-5555-5555-5555-555555555555','ffffffff-6666-6666-6666-666666666666',
			'99999999-7777-7777-7777-777777777777')
		        AND NOT EXISTS (
		          SELECT 1 FROM tenant_users tu
		            JOIN users u ON u.id = tu.user_id
		           WHERE tu.tenant_id = t.id
		             AND u.email !~ '\.(test|example|invalid|localhost|internal)$'
		        ))
		    OR t.id = $1`, tenantID)
	if _, err := pool.Exec(ctx,
		`ALTER TABLE wallet_ledger ENABLE TRIGGER wallet_ledger_append_only`); err != nil {
		return fmt.Errorf("restore ledger guard: %w", err)
	}
	if deleteErr != nil {
		return fmt.Errorf("clear demo tenant: %w", deleteErr)
	}
	// Users are not owned by a tenant row, so the cascade above leaves them
	// behind. Anyone with no remaining membership is a leftover from a signup a
	// spec performed, and is exactly what makes the next run's signup collide.
	if _, err := pool.Exec(ctx, `
		DELETE FROM users
		WHERE email = 'founder@acme.test'
		   OR id NOT IN (SELECT user_id FROM tenant_users)`); err != nil {
		return fmt.Errorf("clear demo user: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tenants (id, name, country, status, capabilities)
		VALUES ($1, 'Acme Retail', 'IN', 'active',
		        ARRAY['sms.send','rcs.send','compliance.manage','billing.view'])`,
		tenantID); err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, email_verified)
		VALUES ($1, 'founder@acme.test', 'Alex Rao', $2, true)`,
		userID, hash); err != nil {
		return fmt.Errorf("seed user: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')`,
		tenantID, userID); err != nil {
		return fmt.Errorf("seed membership: %w", err)
	}

	// Put the sessions back, so a reset restores the fixture without signing
	// anyone out of it. Restored under the same fixed user id they were issued
	// to, which is why this works at all.
	for _, session := range carried {
		if _, err := pool.Exec(ctx, `
			INSERT INTO sessions (tenant_id, user_id, token_hash, device, browser,
			                      location, ip_address, last_active_at, expires_at, revoked_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (token_hash) DO NOTHING`,
			tenantID, userID, session.tokenHash, session.device, session.browser,
			session.location, session.ip, session.lastActiveAt,
			session.expiresAt, session.revokedAt); err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
	}

	// Two other signed-in devices, from ../SMS-UI/src/mocks/sessions-state.ts.
	//
	// The security screen's whole job is to let someone find a session they do
	// not recognise and end it. With only the session they are looking at, the
	// list has nothing to revoke, no way to show that the current device is
	// marked and protected, and no way to prove revoking one leaves the others
	// alone.
	//
	// The token hashes are random bytes, not the hash of any token: these
	// sessions must be VISIBLE but never usable. A real token would make the
	// fixture a permanent set of working credentials for the demo account.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (tenant_id, user_id, token_hash, device, browser,
		                      location, ip_address, last_active_at, expires_at)
		VALUES
		  ($1, $2, gen_random_bytes(32), 'macOS · Safari',    'Safari 17',
		   'Bengaluru, India', '49.207.12.44', now() - interval '1 day',  now() + interval '20 days'),
		  ($1, $2, gen_random_bytes(32), 'iOS · Relay app',   'Mobile Safari',
		   'Delhi, India',     '117.98.4.201', now() - interval '3 days', now() + interval '18 days')`,
		tenantID, userID); err != nil {
		return fmt.Errorf("seed sessions: %w", err)
	}

	// The rest of the roster, copied from ../SMS-UI/src/mocks/team-state.ts.
	//
	// A one-person team is not a team, and the settings screen shows nothing
	// worth looking at with a single owner row: no role to change, nobody to
	// remove, and no way to see that the sole owner's own row is deliberately
	// locked. The specs assert on these three by address, so the values are
	// part of the contract in the same way the sender headers below are.
	//
	// They share the founder's password hash. It is a fixture, none of them is
	// meant to be signed in as, and minting three more hashes would slow every
	// reset for nothing — the hash is deliberately expensive.
	//
	// new.hire@acme.test is 'invited', not 'active': the roster renders the two
	// states differently and nothing else in the fixture exercises the second.
	// Their name is '' rather than NULL — users.name is NOT NULL, and an
	// invited row is exactly what InviteTeamMember writes for someone who has
	// not accepted yet. ListTeamMembers turns that into the contract's null.
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, email_verified)
		VALUES
		  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'priya@acme.test',    'Priya Nair', $1, true),
		  ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'sam@acme.test',      'Sam Verma',  $1, true),
		  ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'new.hire@acme.test', '',           $1, false)`,
		hash); err != nil {
		return fmt.Errorf("seed team users: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant_users (tenant_id, user_id, role, status, invited_at)
		VALUES
		  ($1, 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'admin',  'active',  NULL),
		  ($1, 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'member', 'active',  NULL),
		  ($1, 'cccccccc-cccc-cccc-cccc-cccccccccccc', 'member', 'invited', '2026-07-20T10:00:00Z')`,
		tenantID); err != nil {
		return fmt.Errorf("seed team memberships: %w", err)
	}

	// The five senders the frontend's fixtures define, one per channel, plus a
	// pending SMS header. Copied from ../SMS-UI/src/mocks/senders-state.ts —
	// the specs assert on these headers by name, so the values are as much a
	// part of the contract as the shapes.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sender_ids (id, tenant_id, header, channel, country, status,
		                        waba_id, display_name, phone_number,
		                        email_domain, from_address, from_name,
		                        caller_id_number, voice_verified, external_id,
		                        quality_rating, messaging_tier, created_at)
		VALUES
		  ($1, $7, 'ACMERT', 'SMS', 'IN', 'approved',
		   NULL, NULL, NULL, NULL, NULL, NULL, NULL, false,
		   '1707161234567890123', NULL, NULL, '2026-05-02T09:30:00Z'),
		  ($2, $7, 'ACMERT', 'RCS', 'IN', 'approved',
		   NULL, NULL, NULL, NULL, NULL, NULL, NULL, false,
		   'rcs-agent-acmert-in', NULL, NULL, '2026-06-18T14:05:00Z'),
		  ($3, $7, 'NEWPND', 'SMS', 'IN', 'pending_review',
		   NULL, NULL, NULL, NULL, NULL, NULL, NULL, false,
		   NULL, NULL, NULL, '2026-07-01T10:00:00Z'),
		  ($4, $7, 'Acme Retail', 'WHATSAPP', 'IN', 'approved',
		   'waba-acme-retail-in', 'Acme Retail', '+919876543000',
		   NULL, NULL, NULL, NULL, false, NULL,
		   -- Meta's own rating for this account, mirrored from
		   -- ../SMS-UI/src/mocks/senders-state.ts. The senders list renders both
		   -- in a "Quality / tier" column and the spec reads them out of it.
		   'green', 10000, '2026-07-15T11:00:00Z'),
		  ($5, $7, 'Acme Notifications', 'EMAIL', 'IN', 'approved',
		   NULL, NULL, NULL, 'notifications.acmert.example',
		   'alerts@notifications.acmert.example', 'Acme Retail', NULL, false,
		   NULL, NULL, NULL, '2026-08-01T09:00:00Z'),
		  -- Verified, because this sender is seeded approved and an approved
		  -- voice sender whose caller id was never verified is a state the
		  -- product must not be able to reach.
		  ($6, $7, 'Acme Support Line', 'VOICE', 'IN', 'approved',
		   NULL, NULL, NULL, NULL, NULL, NULL, '+14155550199', true,
		   NULL, NULL, NULL, '2026-08-02T09:00:00Z')`,
		smsID, rcsID, pendingSMSID, whatsappID, emailID, voiceID, tenantID); err != nil {
		return fmt.Errorf("seed senders: %w", err)
	}

	// The wallet is funded through the ledger rather than by setting a balance
	// directly, because the balance is derived and a trigger forbids rewriting
	// history. Seeding the balance column alone would leave the two disagreeing.
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_balances (tenant_id, currency, balance_minor)
		VALUES ($1, 'INR', 0) ON CONFLICT DO NOTHING`, tenantID); err != nil {
		return fmt.Errorf("seed wallet: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
		                           balance_after_minor, description)
		VALUES ($1, 'INR', 'topup', 4250000, 4250000, 'Demo seed')`,
		tenantID); err != nil {
		return fmt.Errorf("seed ledger: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE wallet_balances SET balance_minor = 4250000
		 WHERE tenant_id = $1 AND currency = 'INR'`, tenantID); err != nil {
		return fmt.Errorf("set balance: %w", err)
	}

	// The audience spec asserts on an exact seeded list: 4 members, 2 consented
	// for SMS, 2 for RCS. Per-channel consent is what the audience screen shows,
	// and a list where everyone consented to everything would let a broken
	// consent filter pass unnoticed.
	if _, err := pool.Exec(ctx, `
		INSERT INTO contact_lists (id, tenant_id, name)
		VALUES ($1, $2, 'Diwali 2026')`, listID, tenantID); err != nil {
		return fmt.Errorf("seed contact list: %w", err)
	}

	// consentHoursAgo is when this contact last granted WhatsApp consent, as an
	// offset from now. WhatsApp only allows a business to open a conversation
	// within 24 hours of the customer's last interaction, so the campaign wizard
	// shows how many of a list are inside that window. Priya and Vikram are
	// inside it, Arjun is outside — a fixture where everyone was inside would
	// let a broken window calculation pass unnoticed, and one where nobody was
	// would hide the stat entirely.
	//
	// Stored as an offset rather than a fixed timestamp because the window is
	// measured against now: a literal date drifts out of the window overnight
	// and the spec starts failing for a reason unrelated to the code.
	// The numbers are chosen, not arbitrary.
	//
	// The sandbox connector reserves the LAST THREE DIGITS of a recipient to
	// drive its failure modes: …000 is rejected at submit, …001 comes back
	// ABSENT_SUBSCRIBER, …002 is blocked by DND, …003 never reports at all.
	// That is a deliberate QA feature — it gives anyone a way to exercise every
	// error path on demand — but the original fixture numbered these contacts
	// …001 through …004 without knowing it, so THREE OF THE FOUR demo contacts
	// could never be messaged successfully. Replying to Priya always failed,
	// and it looked like a broken inbox rather than a reserved number.
	//
	// So Priya, Arjun and Meera sit outside the reserved band. Vikram stays on
	// …004 because it delivers and because inbox.spec.ts names it directly.
	contacts := []struct {
		id, msisdn, name, city, email, consent string
		consentHoursAgo                        int
	}{
		{"c0000001-0000-0000-0000-000000000001", "+919876500011", "Priya", "Mumbai",
			"priya@example.com",
			`{"SMS":"opted_in","RCS":"unknown","WHATSAPP":"opted_in","EMAIL":"opted_in","VOICE":"opted_in"}`, 2},
		{"c0000002-0000-0000-0000-000000000002", "+919876500012", "Arjun", "Delhi", "",
			`{"SMS":"opted_in","RCS":"opted_in","WHATSAPP":"opted_in"}`, 30},
		{"c0000003-0000-0000-0000-000000000003", "+919876500013", "Meera", "Pune", "",
			`{"SMS":"opted_out","RCS":"unknown","WHATSAPP":"unknown"}`, 0},
		{"c0000004-0000-0000-0000-000000000004", "+919876500004", "Vikram", "Bengaluru", "",
			`{"SMS":"unknown","RCS":"opted_in","WHATSAPP":"opted_in"}`, 1},
	}
	for _, contact := range contacts {
		var email *string
		if contact.email != "" {
			email = &contact.email
		}
		// Meera never consented to WhatsApp, so she has no timestamp — a
		// consent date on someone who did not consent is a contradiction the
		// audience screen would have to invent a meaning for.
		var consentedAt *string
		if contact.consentHoursAgo > 0 {
			hours := fmt.Sprintf("%d", contact.consentHoursAgo)
			consentedAt = &hours
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO contacts (id, tenant_id, msisdn, email, country, fields, consent,
			                      consented_at)
			VALUES ($1, $2, $3, $4, 'IN',
			        jsonb_build_object('firstName', $5::text, 'city', $6::text),
			        $7::jsonb,
			        CASE WHEN $8::text IS NULL THEN NULL
			             ELSE jsonb_build_object('WHATSAPP',
			                      to_char(now() - ($8::text || ' hours')::interval,
			                              'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
			        END)`,
			contact.id, tenantID, contact.msisdn, email, contact.name, contact.city,
			contact.consent, consentedAt); err != nil {
			return fmt.Errorf("seed contact %s: %w", contact.name, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO contact_list_members (tenant_id, list_id, contact_id)
			VALUES ($1, $2, $3)`, tenantID, listID, contact.id); err != nil {
			return fmt.Errorf("seed list membership: %w", err)
		}
	}

	// The thirteen templates the frontend's fixtures define, across all five
	// channels. Names are asserted on directly by the specs ("Order shipped",
	// "Shipment notice (Email)"), so they are copied verbatim from
	// ../SMS-UI/src/mocks/templates-state.ts rather than invented.
	// SMS and Voice carry a plain body. RCS, WhatsApp and Email carry structured
	// content — a card with suggestion chips, a set of reply buttons, a subject
	// line and an HTML body. The `content` field below holds that structure
	// verbatim from the frontend's fixture, because the screens render its
	// individual parts (a button's label, an email's subject) and a paraphrase
	// would show the customer something the mock never said.
	templates := []struct {
		id, sender, name, channel, body, category, status string
		variables                                         string
		ctaURL                                            string
		content                                           string
	}{
		{smsTemplate, smsID, "Order shipped", "SMS",
			"Hi {{first_name}}, your order {{order_id}} has shipped. Track: https://acme.example.com/track",
			"", "approved", `{first_name,order_id}`, "https://acme.example.com/track", ""},
		{otpTemplate, smsID, "OTP", "SMS",
			"{{code}} is your Acme verification code.", "", "pending_review", `{code}`, "", ""},

		{rcsTemplate, rcsID, "Welcome (RCS)", "RCS", "", "", "approved", `{first_name}`, "", `{
			"kind": "text",
			"text": "Welcome to Acme, {{first_name}}.",
			"suggestions": [
				{"type": "reply", "text": "Get started"},
				{"type": "open_url", "text": "Open app", "url": "https://acme.example.com/app"}
			]
		}`},
		{"dddddddd-dddd-dddd-dddd-dddddddddddd", rcsID, "Product launch", "RCS",
			"", "", "pending_review", `{product}`, "", `{
			"kind": "card",
			"card": {
				"mediaUrl": "https://acme.example.com/launch.jpg",
				"title": "Meet {{product}}",
				"description": "Our newest release, available today."
			},
			"suggestions": [
				{"type": "open_url", "text": "Shop now", "url": "https://acme.example.com/shop"}
			]
		}`},

		{"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", whatsappID, "Order confirmed (WA text)",
			"WHATSAPP", "", "UTILITY", "approved", `{first_name,order_id}`, "", `{
			"kind": "text",
			"body": "Hi {{first_name}}, your order {{order_id}} is confirmed."
		}`},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", whatsappID, "Feedback request (WA buttons)",
			"WHATSAPP", "", "MARKETING", "approved", `{first_name}`, "", `{
			"kind": "buttons",
			"body": "Hi {{first_name}}, how was your order?",
			"buttons": [
				{"type": "quick_reply", "text": "Great"},
				{"type": "quick_reply", "text": "Not great"},
				{"type": "cta_url", "text": "Leave a review", "url": "https://acme.example.com/review"}
			]
		}`},
		{"10101010-1010-1010-1010-101010101010", whatsappID, "Support menu (WA list)",
			"WHATSAPP", "", "UTILITY", "approved", `{}`, "", `{
			"kind": "list",
			"body": "How can we help today?",
			"buttonLabel": "View options",
			"sections": [
				{"title": "Support", "rows": [
					{"id": "track",  "title": "Track my order"},
					{"id": "return", "title": "Start a return"},
					{"id": "human",  "title": "Talk to a person"}
				]}
			]
		}`},

		{"20202020-2020-2020-2020-202020202020", emailID, "Shipment notice (Email)",
			"EMAIL", "", "TRANSACTIONAL", "approved", `{first_name,order_id}`, "", `{
			"subject": "Your order {{order_id}} has shipped",
			"bodyHtml": "<p>Hi {{first_name}}, your order {{order_id}} is on its way.</p>",
			"preheader": "Track your delivery"
		}`},
		{"30303030-3030-3030-3030-303030303030", emailID, "Diwali sale (Email)",
			"EMAIL", "", "MARKETING", "approved", `{first_name}`, "", `{
			"subject": "{{first_name}}, our Diwali sale starts now",
			"bodyHtml": "<p>Hi {{first_name}}, save 20% this week only.</p><a href=\"{{unsubscribe_url}}\">Unsubscribe</a>",
			"preheader": "20% off everything"
		}`},
		{"40404040-4040-4040-4040-404040404040", emailID, "Verification code (Email)",
			"EMAIL", "", "AUTHENTICATION", "approved", `{code}`, "", `{
			"subject": "Your verification code",
			"bodyHtml": "<p>{{code}} is your Acme verification code. It expires in 10 minutes.</p>"
		}`},

		{"77777777-0000-0000-0000-000000000001", voiceID, "Delivery update (Voice)", "VOICE",
			"Hi {{first_name}}, this is Acme calling about order {{order_id}}. It's out for delivery today.",
			"TRANSACTIONAL", "approved", `{first_name,order_id}`, "", ""},
		{"77777777-0000-0000-0000-000000000002", voiceID, "Diwali sale (Voice)", "VOICE",
			"Hi {{first_name}}, Acme's Diwali sale is on now. Say 'yes' to hear today's top offer.",
			"MARKETING", "approved", `{first_name}`, "", ""},
		{"77777777-0000-0000-0000-000000000003", voiceID, "Verification code (Voice)", "VOICE",
			"Your Acme verification code is {{code}}. Repeating: {{code}}.",
			"AUTHENTICATION", "approved", `{code}`, "", ""},
	}
	for _, template := range templates {
		var body, category, ctaURL *string
		if template.body != "" {
			body = &template.body
		}
		if template.category != "" {
			category = &template.category
		}
		if template.ctaURL != "" {
			ctaURL = &template.ctaURL
		}
		// The content column is chosen by channel, so a payload can never land
		// on the wrong one — the database enforces the same rule, and hitting
		// that constraint here would mean this table and the schema disagree.
		var rcsContent, waContent, emailContent *string
		if template.content != "" {
			switch template.channel {
			case "RCS":
				rcsContent = &template.content
			case "WHATSAPP":
				waContent = &template.content
			case "EMAIL":
				emailContent = &template.content
			}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
			                       body, category, variables, status, cta_url,
			                       rcs_content, wa_content, email_content)
			VALUES ($1,$2,$3,$4,$5,'IN',$6,$7,$8::text[],$9,$10,
			        $11::jsonb, $12::jsonb, $13::jsonb)`,
			template.id, tenantID, template.sender, template.name, template.channel,
			body, category, template.variables, template.status, ctaURL,
			rcsContent, waContent, emailContent); err != nil {
			return fmt.Errorf("seed template %s: %w", template.name, err)
		}
	}

	// The DNS records the "Acme Notifications" email sender publishes. They hang
	// off the EMAIL sender, not the SMS one — an SMS header has no domain to
	// authenticate — and they are seeded verified, because that sender is seeded
	// approved and an approved email sender with unverified DNS is a state the
	// product must never reach. Hosts and values are copied from
	// ../SMS-UI/src/mocks/senders-state.ts; the specs read the host out of the
	// rendered row, so the string is part of the contract.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sender_dns_records (tenant_id, sender_id, record_type, host, value, status)
		VALUES ($1, $2, 'SPF', 'notifications.acmert.example',
		        'v=spf1 include:mail.relay-platform.example ~all', 'verified'),
		       ($1, $2, 'DKIM', 'relay1._domainkey.notifications.acmert.example',
		        'v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDRacme000000000000000000000000', 'verified'),
		       ($1, $2, 'DMARC', '_dmarc.notifications.acmert.example',
		        'v=DMARC1; p=quarantine; rua=mailto:dmarc@notifications.acmert.example', 'verified')`,
		tenantID, emailID); err != nil {
		return fmt.Errorf("seed dns records: %w", err)
	}

	// Two campaigns in different states: one finished, one mid-flight. The
	// campaign specs assert on both, and a fixture with only completed rows
	// would let a broken in-progress view pass unnoticed.
	if _, err := pool.Exec(ctx, `
		INSERT INTO campaigns (id, tenant_id, name, channel, country, list_id, sender_id,
		                       template_id, status, send_started_at, scheduled_at, recipients,
		                       segments_per_message, cost_minor_min, cost_minor_max, currency)
		VALUES ($1, $3, 'Festive flash sale', 'SMS', 'IN', $4, $5, $6,
		        'sent', '2026-06-20T10:05:00Z', NULL, 1840, 1, 22080, 22080, 'INR'),
		       ($2, $3, 'Weekend RCS promo',  'RCS', 'IN', $4, $7, $8,
		        'sending', now() - interval '20 seconds', NULL, 1200, 1, 25200, 54000, 'INR'),
		       ($9, $3, 'Payment reminders',  'SMS', 'IN', $4, $5, $6,
		        -- Scheduled in the future so it stays scheduled: a past date would
		        -- describe a campaign that should already have sent.
		        'scheduled', NULL, now() + interval '3 days', 900, 1, 21600, 21600, 'INR')`,
		campaignOne, campaignTwo, tenantID, listID, smsID, smsTemplate,
		rcsID, rcsTemplate, campaignThree); err != nil {
		return fmt.Errorf("seed campaigns: %w", err)
	}

	// A live journey, two open threads and a verify service. Each is addressed
	// by a hardcoded id in the frontend's specs, so the ids matter as much as
	// the rows.
	if _, err := pool.Exec(ctx, `
		-- activated_at is set explicitly because this row is inserted straight
		-- into the 'active' state rather than transitioned into it, and it is
		-- the transition that normally stamps the column. Without it an active
		-- journey reports no activation time, and the screen that shows "running
		-- since" has nothing to show.
		--
		-- Relative to now, not a fixed date: the journey is described as
		-- currently running, and a literal timestamp would have it activated
		-- further in the past every day until the claim stopped being plausible.
		INSERT INTO journeys (id, tenant_id, name, status, trigger_type,
		                      trigger_list_id, steps, recipients, activated_at)
		VALUES ($1, $2, 'Diwali welcome flow', 'active', 'list_entry', $3, $4::jsonb, 1200,
		        now() - interval '6 days')`,
		journeyID, tenantID, listID, `[
			{"type":"send","id":"step-1","channel":"SMS","senderId":"`+smsID+`","templateId":"`+smsTemplate+`"},
			{"type":"wait","id":"step-2","durationMinutes":1440},
			{"type":"send","id":"step-3","channel":"SMS","senderId":"`+smsID+`","templateId":"`+smsTemplate+`"}
		]`); err != nil {
		return fmt.Errorf("seed journey: %w", err)
	}

	// Four threads, chosen so the inbox can demonstrate what it is for:
	//
	//   * Priya appears TWICE, on SMS and Email. One person reaching a business
	//     on two channels is the normal case, and a fixture with one thread per
	//     contact cannot show that the list groups by thread rather than person.
	//   * Arjun's SMS thread is already suppressed — he texted STOP. The inbox
	//     must refuse a reply there, and that guard needs a thread to guard.
	//   * Vikram's RCS thread is closed, so the list has something in every
	//     status rather than only open ones.
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversations (id, tenant_id, contact_id, channel, status, unread)
		VALUES ($1, $3, 'c0000004-0000-0000-0000-000000000004', 'RCS',   'closed', false),
		       ($2, $3, 'c0000001-0000-0000-0000-000000000001', 'EMAIL', 'open',   false),
		       ($4, $3, 'c0000001-0000-0000-0000-000000000001', 'SMS',   'open',   true),
		       ($5, $3, 'c0000002-0000-0000-0000-000000000002', 'SMS',   'open',   false)`,
		conversationOne, conversationTwo, tenantID,
		conversationThree, conversationFour); err != nil {
		return fmt.Errorf("seed conversations: %w", err)
	}

	// Arjun's opt-out. Seeded as a real suppression keyed on his number, not a
	// flag on the thread, because that is how the send path checks it — a
	// thread-level flag would let a campaign still reach him.
	if _, err := pool.Exec(ctx, `
		INSERT INTO suppressions (tenant_id, identity, msisdn, reason, note)
		VALUES ($1, '+919876500012', '+919876500012', 'opted_out_keyword', 'Replied STOP')
		ON CONFLICT DO NOTHING`, tenantID); err != nil {
		return fmt.Errorf("seed arjun suppression: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation_messages (tenant_id, conversation_id, direction, body)
		VALUES ($1, $2, 'inbound',  'Is the festive offer still on?'),
		       ($1, $2, 'outbound', 'Yes — it runs until Sunday.'),
		       -- NOT "One more question!". e2e/inbox.spec.ts posts exactly that
		       -- string as its probe for reopen-on-reply and then asserts the
		       -- text is on screen; seeding the identical body gave it two
		       -- matches and a strict-mode violation. The spec is right — a
		       -- fixture must not occupy the string a test needs to be unique.
		       -- The MSW mock always used distinct wording here, which is why
		       -- this only ever surfaced against a real backend.
		       ($1, $3, 'inbound',  'One more question — do you ship internationally?'),
		       ($1, $4, 'inbound',  'Where is my order?'),
		       ($1, $4, 'outbound', 'It is out for delivery today.')`,
		tenantID, conversationOne, conversationTwo, conversationThree); err != nil {
		return fmt.Errorf("seed conversation messages: %w", err)
	}
	// The seeded outbound replies were real sends, so they carry real charges.
	//
	// The billing spec baselines the ledger BEFORE sending its own reply and
	// then proves exactly one new "Inbox reply" line appeared. With no seeded
	// charges the baseline is zero and the test cannot tell a working charge
	// path from one that bills every reply twice.
	//
	// Two lines: Vikram's RCS reply at the RCS rate, Priya's SMS reply at the
	// SMS rate. Different amounts on purpose — a ledger where every line costs
	// the same cannot show that pricing follows the channel.
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
		                           balance_after_minor, description)
		VALUES ($1, 'INR', 'charge', 21, 4249979, 'Inbox reply (RCS)'),
		       ($1, 'INR', 'charge', 12, 4249967, 'Inbox reply (SMS)')`,
		tenantID); err != nil {
		return fmt.Errorf("seed inbox reply charges: %w", err)
	}
	// The balance follows the two charges above. Set rather than derived here
	// because the ledger is append-only and the balance column is the running
	// total the screens read; leaving it at the pre-charge figure would have the
	// two disagreeing, which is the one thing a ledger must never do.
	if _, err := pool.Exec(ctx,
		`UPDATE wallet_balances SET balance_minor = 4249967
		 WHERE tenant_id = $1 AND currency = 'INR'`, tenantID); err != nil {
		return fmt.Errorf("set balance after reply charges: %w", err)
	}

	// The STOP itself, recorded with the keyword that matched. The compliance
	// screen quotes this message as the evidence for the suppression, so it has
	// to be the real inbound rather than a note describing one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversation_messages (tenant_id, conversation_id, direction, body,
		                                   keyword_matched)
		VALUES ($1, $2, 'inbound', 'STOP', 'STOP')`,
		tenantID, conversationFour); err != nil {
		return fmt.Errorf("seed stop message: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO verify_services (id, tenant_id, name, channels, fallback_order,
		                             code_length, code_ttl_seconds, max_attempts,
		                             max_per_phone, window_seconds, cooldown_seconds,
		                             region_allowlist)
		VALUES ($1, $2, 'Login OTP', $3::jsonb, ARRAY['RCS','SMS'],
		        6, 300, 3, 5, 3600, 30, ARRAY[]::text[])`,
		verifyService, tenantID, `[
			{"channel":"RCS","senderId":"`+rcsID+`","body":"{{code}} is your Acme login code."},
			{"channel":"SMS","senderId":"`+smsID+`","body":"{{code}} is your Acme login code. Valid 5 min."}
		]`); err != nil {
		return fmt.Errorf("seed verify service: %w", err)
	}

	if err := seedVerifications(ctx, pool, tenantID); err != nil {
		return err
	}

	// One issued invoice for a closed month plus its line items. The billing
	// screens distinguish an issued invoice from the open current period, and a
	// fixture with only the current month cannot exercise that difference.
	var invoiceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO invoices (tenant_id, currency, period_start, period_end, status,
		                      subtotal_minor, tax_rate_percent, tax_minor, total_minor)
		VALUES ($1, 'INR', date_trunc('month', now() - interval '1 month')::date,
		        (date_trunc('month', now()) - interval '1 day')::date, 'issued',
		        118000, 18, 21240, 139240)
		RETURNING id`, tenantID).Scan(&invoiceID); err != nil {
		return fmt.Errorf("seed invoice: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invoice_line_items (invoice_id, tenant_id, description, quantity,
		                                unit_minor, amount_minor)
		VALUES ($1, $2, 'SMS messages (IN)', 8000, 12,  96000),
		       ($1, $2, 'RCS messages (IN)', 1000, 21,  21000),
		       ($1, $2, 'Number lookup',      100, 10,   1000)`,
		invoiceID, tenantID); err != nil {
		return fmt.Errorf("seed invoice lines: %w", err)
	}

	// Analytics reads ClickHouse, so without this the dashboards are empty even
	// though Postgres has campaigns. Not fatal: a missing ClickHouse should cost
	// the charts, not the whole fixture.
	if url := os.Getenv("CLICKHOUSE_URL"); includeHistory && url != "" {
		if err := seedMessageHistory(ctx, url, tenantID); err != nil {
			fmt.Fprintln(os.Stderr, "warning: message history not seeded:", err)
		}
	}

	// Platform staff and the other tenants the operator console lists. The demo
	// tenant alone would make every operator screen a single-row table, which
	// cannot show sorting, filtering, or the difference between an active and a
	// suspended customer.
	operatorHash, err := auth.HashPassword("relay-ops-dev")
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operator_users (id, email, name, password_hash, role)
		VALUES ($1, 'ops@relay.internal', 'Ops Team', $2, 'admin')
		ON CONFLICT (email) DO UPDATE
		  SET password_hash = EXCLUDED.password_hash, role = EXCLUDED.role`,
		operatorID, operatorHash); err != nil {
		return fmt.Errorf("seed operator: %w", err)
	}

	// Upserted rather than deleted and recreated: these tenants are referenced
	// by the approval and rate-override fixtures, and a cascade would take those
	// with them on every reset.
	// All seven tenants the operator console's fixtures define, including the two
	// carrying abuse flags — the abuse queue is empty without them, so the specs
	// that work that queue have nothing to act on. Hours-ago flag times rather
	// than fixed dates so "flagged 30 hours ago" stays true whenever this runs.
	otherTenants := []struct {
		id, name, country, status string
		flagHoursAgo              int
		flagReason                string
	}{
		{"aaaaaaaa-1111-1111-1111-111111111111", "Northwind Logistics", "US", "active", 0, ""},
		// Flagged most recently of the three, so the abuse queue has a clear
		// newest entry and the SLA badge has a range to sort across rather than
		// three rows all aged the same.
		{"bbbbbbbb-2222-2222-2222-222222222222", "Bluewave Retail", "GB", "active", 6,
			"Sudden 40x spike in outbound volume overnight"},
		{"cccccccc-3333-3333-3333-333333333333", "Falcon Freight", "AE", "suspended", 0, ""},
		{"dddddddd-4444-4444-4444-444444444444", "Nimbus Foods", "IN", "pending", 0, ""},
		{"eeeeeeee-5555-5555-5555-555555555555", "Orbit Media", "US", "active", 30,
			"Multiple recipient complaints of unsolicited messaging"},
		{"ffffffff-6666-6666-6666-666666666666", "Sahara Traders", "AE", "active", 60,
			"Suppressed-list contacts still receiving messages"},
		{"99999999-7777-7777-7777-777777777777", "Kestrel Analytics", "GB", "suspended", 0, ""},
	}
	for _, tenant := range otherTenants {
		if _, err := pool.Exec(ctx, `
			INSERT INTO tenants (id, name, country, status, capabilities,
			                     flagged_at, flag_reason)
			VALUES ($1,$2,$3,$4, ARRAY['sms.send'],
			        CASE WHEN $5::int > 0 THEN now() - make_interval(hours => $5::int) END,
			        NULLIF($6,''))
			ON CONFLICT (id) DO UPDATE
			  SET name = EXCLUDED.name, country = EXCLUDED.country,
			      status = EXCLUDED.status, flagged_at = EXCLUDED.flagged_at,
			      flag_reason = EXCLUDED.flag_reason, throttled_at = NULL`,
			tenant.id, tenant.name, tenant.country, tenant.status,
			tenant.flagHoursAgo, tenant.flagReason); err != nil {
			return fmt.Errorf("seed tenant %s: %w", tenant.name, err)
		}
	}

	activityTenants := make([]string, 0, len(otherTenants)+1)
	activityTenants = append(activityTenants, tenantID)
	for _, tenant := range otherTenants {
		activityTenants = append(activityTenants, tenant.id)
	}
	if err := seedUserActivity(ctx, pool, activityTenants); err != nil {
		return err
	}

	// Approval items belonging to OTHER tenants.
	//
	// The approvals queue is a cross-tenant screen — its whole purpose is that
	// one operator works through every customer's submissions in one place. A
	// fixture where every pending item belonged to Acme Retail could not show
	// that, and could not exercise the country or status filters at all, because
	// every row would answer identically.
	//
	// So: one pending US sender, one pending GB template, and one already-
	// rejected GB sender. The rejected row is what makes "status: rejected"
	// return anything, and it carries its rejection reason so the review dialog
	// has something real to display.
	otherSenders := []struct {
		id, tenant, header, channel, country, status, reason string
	}{
		{"a1a1a1a1-0000-4000-8000-000000000001", "aaaaaaaa-1111-1111-1111-111111111111",
			"NWLOGI", "SMS", "US", "pending_review", ""},
		{"b1b1b1b1-0000-4000-8000-000000000002", "bbbbbbbb-2222-2222-2222-222222222222",
			"BLUWAV", "SMS", "GB", "approved", ""},
		{"c1c1c1c1-0000-4000-8000-000000000003", "99999999-7777-7777-7777-777777777777",
			"KESTRL", "SMS", "GB", "rejected", "Header does not match the registered entity name."},
	}
	for _, sender := range otherSenders {
		if _, err := pool.Exec(ctx, `
			INSERT INTO sender_ids (id, tenant_id, header, channel, country, status,
			                        rejection_reason)
			VALUES ($1,$2,$3,$4,$5,$6, NULLIF($7,''))
			ON CONFLICT (id) DO UPDATE
			  SET status = EXCLUDED.status, rejection_reason = EXCLUDED.rejection_reason`,
			sender.id, sender.tenant, sender.header, sender.channel, sender.country,
			sender.status, sender.reason); err != nil {
			return fmt.Errorf("seed approval sender %s: %w", sender.header, err)
		}
	}
	// A pending GB template for Bluewave Retail, hung off their approved sender
	// — a template cannot exist without one, and the reject flow needs a target.
	if _, err := pool.Exec(ctx, `
		INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
		                       body, category, variables, status)
		VALUES ('b2b2b2b2-0000-4000-8000-000000000004',
		        'bbbbbbbb-2222-2222-2222-222222222222',
		        'b1b1b1b1-0000-4000-8000-000000000002',
		        'Winter clearance', 'SMS', 'GB',
		        'Hi {{first_name}}, our winter clearance starts today.',
		        'MARKETING', ARRAY['first_name'], 'pending_review')
		ON CONFLICT (id) DO UPDATE
		  SET status = EXCLUDED.status, rejection_reason = NULL`); err != nil {
		return fmt.Errorf("seed approval template: %w", err)
	}

	// A saved card, so the top-up flow has something to charge. Without one the
	// billing screen offers no way to add funds and the spec has nothing to click.
	if _, err := pool.Exec(ctx, `
		INSERT INTO payment_methods (tenant_id, brand, last4, is_default)
		VALUES ($1, 'visa', '4242', true)`, tenantID); err != nil {
		return fmt.Errorf("seed payment method: %w", err)
	}

	// A charge against the finished campaign, on top of the top-up.
	//
	// amount_minor is always POSITIVE — the constraint enforces it, and the
	// entry_type carries the direction. Signing the amount instead would make
	// "charge" and "refund" indistinguishable from their sign alone and let a
	// negative topup through as a charge.
	//
	// The balance moves with it: the ledger is append-only and the balance is
	// derived from it, so seeding a charge without moving the balance would leave
	// the wallet screen and the ledger disagreeing — exactly the discrepancy the
	// append-only rule exists to make impossible.
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
		                           balance_after_minor, description,
		                           campaign_id, campaign_name)
		VALUES ($1,'INR','charge', 22080, 4227920, 'Festive flash sale',
		        $2, 'Festive flash sale')`,
		tenantID, campaignOne); err != nil {
		return fmt.Errorf("seed campaign charge: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE wallet_balances SET balance_minor = 4227920
		 WHERE tenant_id = $1 AND currency = 'INR'`, tenantID); err != nil {
		return fmt.Errorf("apply campaign charge: %w", err)
	}

	// Support tickets in all three states. A queue with only open tickets cannot
	// exercise the resolve or reopen flows, and the operator console's filters
	// have nothing to filter.
	// Every ticket belongs to one of the OTHER tenants, never the demo tenant.
	//
	// The operator queue needs a cross-tenant spread to filter across, and the
	// customer's own support screen needs to open on its empty state — the
	// first thing a new customer sees, and the only place the "Start a ticket"
	// call to action lives. Those two are only compatible if the demo tenant
	// starts with none, so its tickets are the ones it creates during the run.
	//
	// The tenant, status and category on each row are load-bearing: the queue
	// specs filter by all three and assert which tenants drop out, so a ticket
	// moved to a different company or a different status breaks a test that has
	// nothing to do with support.
	tickets := []struct {
		id, tenantID, subject, category, status, body string
	}{
		{"70000001-0000-4000-8000-000000000001", "bbbbbbbb-2222-2222-2222-222222222222",
			"Need help with my API key", "technical", "open",
			"My key stopped working today."},
		{"70000002-0000-4000-8000-000000000002", "dddddddd-4444-4444-4444-444444444444",
			"Invoice amount doesn't match my usage", "billing", "pending",
			"The invoice total looks higher than what I'd expect from our sent volume this month."},
		{"70000003-0000-4000-8000-000000000003", "aaaaaaaa-1111-1111-1111-111111111111",
			"DLT registration stuck in review", "compliance", "resolved",
			"Our sender registration has been pending for several days."},
	}
	// Cleared by id first. These belong to the other tenants, which are upserted
	// rather than deleted on a reset, so without this the second seed collides
	// on the primary key and the whole rebuild fails.
	if _, err := pool.Exec(ctx, `
		DELETE FROM support_tickets
		WHERE id IN ('70000001-0000-4000-8000-000000000001',
		             '70000002-0000-4000-8000-000000000002',
		             '70000003-0000-4000-8000-000000000003')`); err != nil {
		return fmt.Errorf("clear seeded tickets: %w", err)
	}
	for _, ticket := range tickets {
		if _, err := pool.Exec(ctx, `
			INSERT INTO support_tickets (id, tenant_id, subject, category, status)
			VALUES ($1,$2,$3,$4,$5)`,
			ticket.id, ticket.tenantID, ticket.subject, ticket.category, ticket.status); err != nil {
			return fmt.Errorf("seed ticket %s: %w", ticket.subject, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO support_messages (tenant_id, ticket_id, author, author_name, body)
			VALUES ($1,$2,'customer','Alex Rao',$3)`,
			ticket.tenantID, ticket.id, ticket.body); err != nil {
			return fmt.Errorf("seed ticket message: %w", err)
		}
	}
	// A staff reply on the open ticket, so the thread has both sides.
	if _, err := pool.Exec(ctx, `
		INSERT INTO support_messages (tenant_id, ticket_id, author, author_name, body)
		VALUES ($1,$2,'operator','Ops Team','Can you share a sample payload?')`,
		tickets[0].tenantID, tickets[0].id); err != nil {
		return fmt.Errorf("seed operator reply: %w", err)
	}

	// Two suppressions with DIFFERENT reasons: the screen groups by reason, and
	// a list where every row says the same thing cannot show that grouping works.
	if _, err := pool.Exec(ctx, `
		INSERT INTO suppressions (tenant_id, identity, msisdn, reason, note)
		VALUES ($1,'+919876590001','+919876590001','opted_out_keyword','Replied STOP'),
		       ($1,'+919876590002','+919876590002','manual','Added by support'),
		       -- A second suppressed CONTACT, not just a suppressed number.
		       --
		       -- The journey funnel's "exited (suppressed)" step counts list
		       -- members who are suppressed, and only one of the four was — so
		       -- the funnel showed 1 while every other fixture number implied
		       -- two people had opted out. This contact already carries
		       -- SMS: opted_out in its consent map; the suppression row is the
		       -- enforcement side of that same fact, and without it the two
		       -- halves of the fixture disagreed with each other.
		       ($1,'+919876500013','+919876500013','opted_out_keyword','Replied STOP')
		ON CONFLICT DO NOTHING`, tenantID); err != nil {
		return fmt.Errorf("seed suppressions: %w", err)
	}

	// Developer surface: one key, one webhook, one allowlist entry — each in the
	// state the screens render by default. Secrets are stored hashed, exactly as
	// the real create path stores them, so nothing here is a shortcut around the
	// hashing the product depends on.
	keyHash, err := auth.HashPassword("relay_sk_live_seedkey")
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, name, environment, scopes, key_prefix, key_hash, status)
		VALUES ($1,'Default key','live', ARRAY['send:sms','read:messages'],
		        'sk_live_seed', $2, 'active')`, tenantID, keyHash); err != nil {
		return fmt.Errorf("seed api key: %w", err)
	}
	secretHash, err := auth.HashPassword("whsec_seed")
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_endpoints (tenant_id, environment, url, subscribed_events,
		                               signing_secret_prefix, signing_secret_hash, status)
		VALUES ($1,'live','https://example-tenant.test/webhooks/relay',
		        ARRAY['message.delivered','message.failed'], 'whsec_seed', $2, 'enabled')`,
		tenantID, secretHash); err != nil {
		return fmt.Errorf("seed webhook: %w", err)
	}
	// Two delivery attempts, one of each outcome.
	//
	// The event log is where someone goes when a webhook is not arriving, so a
	// fixture with only successes hides the column they came to read. The
	// failure carries a real 500 and a response snippet, because "failed" with
	// nothing next to it does not tell anyone what to fix.
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_events (tenant_id, endpoint_id, event_type, payload,
		                            attempt, outcome, http_status, response_snippet,
		                            occurred_at)
		SELECT $1, e.id, v.event_type, v.payload::jsonb, v.attempt, v.outcome,
		       v.http_status, nullif(v.snippet, ''), now() - make_interval(mins => v.mins_ago)
		FROM webhook_endpoints e
		CROSS JOIN (VALUES
		    ('message.delivered',
		     '{"messageId":"11111111-1111-1111-1111-111111111111","status":"delivered"}',
		     1, 'succeeded', 200, '', 12),
		    ('message.failed',
		     '{"messageId":"22222222-2222-2222-2222-222222222222","status":"failed"}',
		     2, 'failed', 500, 'upstream timeout after 10s', 47)
		) AS v(event_type, payload, attempt, outcome, http_status, snippet, mins_ago)
		WHERE e.tenant_id = $1 AND e.environment = 'live'`,
		tenantID); err != nil {
		return fmt.Errorf("seed webhook events: %w", err)
	}
	// No IP allowlist entry on purpose: the screen's empty state ("no IP
	// restrictions") is what the spec asserts before adding one, so seeding a row
	// here would hide the state the feature starts in.

	// A test-environment key as well as the live one. The Live/Test toggle is
	// only meaningful if the two environments hold different keys, and a toggle
	// that shows the same list either way proves nothing.
	testKeyHash, err := auth.HashPassword("relay_sk_test_seedkey")
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, name, environment, scopes, key_prefix, key_hash, status)
		VALUES ($1,'Sandbox key','test', ARRAY['send:sms'],
		        'sk_test_seed', $2, 'active')`, tenantID, testKeyHash); err != nil {
		return fmt.Errorf("seed test api key: %w", err)
	}

	// Per-category rates for the channels that price by category. The operator
	// rate card renders one row per (country, channel, category), so a rate card
	// with only channel-level rows shows nothing for Email or Voice — and the
	// categories are what a customer is actually billed against.
	if _, err := pool.Exec(ctx, `
		INSERT INTO pricing_rates (country, channel, category, per_segment_minor, currency)
		-- MARKETING, not PROMOTIONAL. The contract types this field as
		-- TemplateCategory and PROMOTIONAL is not one of its four members; the
		-- frontend threw on it and blanked the whole rate card, taking the valid
		-- rows down with it. db/migrations/00026 now makes that unstorable.
		-- The CHANNEL-LEVEL rows are restored here too, not just the category
		-- ones, and they have to be.
		--
		-- pricing_rates is global — it has no tenant_id — so the tenant delete
		-- that rebuilds everything else does not touch it. These rows came from
		-- migration 00009 and were never re-seeded, so the first spec that
		-- edited a rate in the operator console changed the rate card FOREVER:
		-- reset restored nothing, and every later run of every spec saw the
		-- edited number as its "default". IN/SMS drifted from 12 to 99 that way
		-- and stayed there, which then broke an assertion three specs later
		-- with no visible connection to the spec that had moved it.
		--
		-- Values kept identical to 00009 so the reset restores the real
		-- baseline rather than inventing a second one.
		VALUES ('IN','SMS','',      12,'INR'),
		       ('IN','RCS','',      35,'INR'),
		       ('IN','WHATSAPP','', 80,'INR'),
		       ('US','SMS','',      75,'USD'),
		       ('US','RCS','',     200,'USD'),
		       ('GB','SMS','',      35,'GBP'),
		       ('AE','SMS','',      12,'AED'),
		       ('IN','EMAIL','TRANSACTIONAL',  8,'INR'),
		       ('IN','EMAIL','MARKETING',     12,'INR'),
		       ('IN','VOICE','TRANSACTIONAL', 45,'INR'),
		       ('IN','VOICE','MARKETING',     60,'INR'),
		       ('IN','WHATSAPP','AUTHENTICATION', 70,'INR'),
		       ('IN','WHATSAPP','UTILITY',        55,'INR'),
		       ('IN','WHATSAPP','MARKETING',      90,'INR')
		ON CONFLICT (country, channel, category) DO UPDATE
		  SET per_segment_minor = EXCLUDED.per_segment_minor`); err != nil {
		return fmt.Errorf("seed category rates: %w", err)
	}

	// Carrier routes, ordered. The console's move-up/move-down controls need at
	// least two routes in the same country and channel to have anything to swap.
	if _, err := pool.Exec(ctx, `DELETE FROM routes`); err != nil {
		return fmt.Errorf("clear routes: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO routes (country, channel, carrier, label, priority,
		                    compliance_standing, cost_per_segment_minor, currency, status)
		-- Carrier holds a CarrierId from the contract's enum, not a vendor name.
		-- The console resolves it against a fixed registry to get the label, so
		-- a lowercase or invented value makes the whole routes page throw.
		--
		-- Ported wholesale from ../SMS-UI/src/mocks/routes-state.ts. The console's
		-- specs assert on these labels, and the filter tests assert on which rows
		-- DROP OUT — so a corridor missing here reads as a broken filter.
		--
		-- Priority repeats within a corridor because it ranks the ways of reaching
		-- ONE carrier: JIO has a direct route and an aggregator route, so those are
		-- 1 and 2, while AIRTEL's own direct route is separately priority 1.
		VALUES ('IN','SMS','JIO',        'Jio Direct',                  1,'registered',12,'INR','active'),
		       ('IN','SMS','JIO',        'Jio via Aggregator A',        2,'grey',       9,'INR','active'),
		       ('IN','SMS','AIRTEL',     'Airtel Direct',               1,'registered',14,'INR','active'),
		       ('IN','SMS','VI',         'Vi Direct',                   1,'grey',       8,'INR','disabled'),
		       ('IN','RCS','JIO',        'Jio RCS Direct',              1,'registered',45,'INR','active'),
		       ('IN','RCS','AIRTEL',     'Airtel RCS Direct',           1,'registered',48,'INR','active'),
		       ('IN','VOICE','JIO',      'Jio Voice Direct',            1,'registered',40,'INR','active'),
		       ('US','SMS','VERIZON',    'Verizon Direct',              1,'registered', 1,'USD','active'),
		       ('US','SMS','ATT',        'AT&T Direct',                 1,'registered', 1,'USD','active'),
		       ('US','SMS','TMOBILE',    'T-Mobile via Aggregator B',   1,'grey',       1,'USD','active'),
		       ('US','RCS','VERIZON',    'Verizon RCS Direct',          1,'registered', 5,'USD','active'),
		       ('US','VOICE','VERIZON',  'Verizon Voice Direct',        1,'registered', 5,'USD','active'),
		       ('GB','SMS','EE',         'EE Direct',                   1,'registered', 4,'GBP','active'),
		       ('GB','SMS','O2',         'O2 Direct',                   1,'registered', 4,'GBP','active'),
		       ('GB','SMS','VODAFONE_UK','Vodafone UK via Aggregator C',1,'grey',       3,'GBP','disabled'),
		       ('GB','RCS','EE',         'EE RCS Direct',               1,'registered', 8,'GBP','active'),
		       ('GB','VOICE','EE',       'EE Voice Direct',             1,'registered', 6,'GBP','active'),
		       ('AE','SMS','ETISALAT',   'Etisalat Direct',             1,'registered', 3,'AED','active'),
		       ('AE','SMS','DU',         'du Direct',                   1,'registered', 3,'AED','active'),
		       ('AE','RCS','ETISALAT',   'Etisalat RCS Direct',         1,'registered', 9,'AED','active'),
		       ('AE','VOICE','ETISALAT', 'Etisalat Voice Direct',       1,'registered', 8,'AED','active')`,
	); err != nil {
		return fmt.Errorf("seed routes: %w", err)
	}

	return nil
}

// seedMessageHistory writes 30 days of message history to ClickHouse.
//
// Without it the demo tenant has campaigns and contacts but no traffic, so
// every analytics screen renders an empty state — the dashboards that are
// supposed to be the product look broken. The rollup is what the analytics
// queries actually read, so both it and the raw messages are written: the
// message rows back the logs explorer, the rollup backs the charts.
//
// The mix is deliberately imperfect. A fixture where everything delivered
// would let a broken failure path, a wrong delivery rate, or a missing error
// breakdown all pass unnoticed.
// seedVerifications gives the "Login OTP" service 30 days of one-time-code
// history, because the verify analytics screen is computed from real rows and
// a service with none renders an empty funnel, a zero success rate and a fraud
// panel reading all clear — which is exactly what a broken implementation
// looks like.
//
// The distribution is chosen to make each part of that screen provably working:
//
//   - Mostly `verified`, because most people do type the code correctly.
//   - A steady minority of `incorrect` and `expired`, so the funnel narrows and
//     the success rate is a number strictly between 0 and 100. A fixture at
//     100% cannot tell a correct rate from a hardcoded one.
//   - A handful of `locked`, the state after too many wrong attempts.
//   - A few of each fraud flag. Without these the fraud cards show three zeros,
//     the drill-down has nothing to filter to, and a panel that never reports
//     anything is indistinguishable from one that cannot.
//
// Deterministic rather than random: a fixture that differs between runs turns
// an assertion on a count into a flaky test, and the first thing anyone does
// with a flaky test is stop believing it.
func seedVerifications(ctx context.Context, pool *pgxpool.Pool, tenantID string) error {
	// Each entry is a repeating pattern walked once per verification. The
	// counts below are what the analytics screen adds up, so they are stated
	// here rather than emerging from a random draw nobody can predict.
	outcomes := []struct {
		status    string
		fraudFlag string
		attempts  int
	}{
		{"verified", "none", 1},
		{"verified", "none", 1},
		{"verified", "none", 2},
		{"verified", "none", 1},
		{"incorrect", "none", 3},
		{"verified", "none", 1},
		{"expired", "none", 0},
		{"verified", "none", 1},
		{"verified", "velocity", 1},
		{"locked", "none", 3},
		{"verified", "none", 1},
		{"incorrect", "geo_anomaly", 2},
		{"verified", "none", 1},
		{"rate_limited", "blocked", 0},
		{"verified", "none", 1},
	}

	const days = 30
	const perDay = 4
	for day := 0; day < days; day++ {
		for slot := 0; slot < perDay; slot++ {
			outcome := outcomes[(day*perDay+slot)%len(outcomes)]
			// Spread across the day rather than all at one instant, so the
			// 24h range has something to show and is not a single spike.
			// Passed as a string because it is interpolated into an interval
			// literal below; pgx has no encode plan from int to a text column.
			hoursAgo := fmt.Sprintf("%d", day*24+slot*5)
			// A stable pseudo-number per row: real traffic is many different
			// phones, and a fixture where every code went to one number would
			// let a broken per-phone rate limit look correct.
			msisdn := fmt.Sprintf("+9198765%05d", 10000+day*perDay+slot)
			var verifiedAt any
			if outcome.status == "verified" {
				verifiedAt = hoursAgo
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO verifications (tenant_id, service_id, msisdn, country,
				    channel, code_hash, status, attempts_used, max_attempts,
				    cost_minor, currency, fraud_flag, expires_at, verified_at,
				    created_at)
				VALUES ($1, $2, $3, 'IN', 'SMS',
				        -- Never a real code. These are historical rows nobody
				        -- can verify against, and storing something that looked
				        -- like a usable hash would be worse than storing noise.
				        decode(md5($3 || $4::text), 'hex'),
				        $5, $6, 3, 25, 'INR', $7,
				        now() - ($4::text || ' hours')::interval + interval '5 minutes',
				        CASE WHEN $8::text IS NULL THEN NULL
				             ELSE now() - ($8::text || ' hours')::interval
				                       + interval '40 seconds' END,
				        now() - ($4::text || ' hours')::interval)`,
				tenantID, verifyService, msisdn, hoursAgo,
				outcome.status, outcome.attempts, outcome.fraudFlag,
				verifiedAt); err != nil {
				return fmt.Errorf("seed verification: %w", err)
			}
		}
	}
	return nil
}

func seedMessageHistory(ctx context.Context, url, tenant string) error {
	conn, err := store.OpenClickHouse(ctx, url)
	if err != nil {
		return fmt.Errorf("open clickhouse: %w", err)
	}
	defer conn.Close()

	// Re-running the seed must not double the history, and ReplacingMergeTree
	// would only deduplicate rows with identical sort keys — these have new
	// timestamps each run, so they would accumulate.
	for _, table := range []string{"messages", "message_events", "message_rollup_hourly"} {
		if err := conn.Exec(ctx,
			fmt.Sprintf("ALTER TABLE %s DELETE WHERE tenant_id = toUUID('%s')", table, tenant),
		); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}

	messages, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, campaign_id, campaign_name, journey_id, journey_name,
		conversation_id, channel, country, sender_header, template_id, msisdn,
		email, status, delivered_channel, error_code, error_class, fraud_flag,
		segments, cost_minor, currency, route_id, carrier_ref, carrier,
		created_at, sent_at, delivered_at, updated_at, version)`)
	if err != nil {
		return fmt.Errorf("prepare messages: %w", err)
	}
	rollup, err := conn.PrepareBatch(ctx, `INSERT INTO message_rollup_hourly`)
	if err != nil {
		return fmt.Errorf("prepare rollup: %w", err)
	}

	tenantUUID := uuid.MustParse(tenant)
	// Carrier is part of the fixture, not decoration: the deliverability screen
	// compares carriers against each other, so history attributed to a single
	// carrier would render a comparison with nothing to compare.
	campaigns := []struct {
		id, name, channel, sender, carrier string
	}{
		// CarrierId is a contract ENUM, not a display name. Seeding "Reliance Jio"
		// here made the dashboard throw "Unknown carrier" and render nothing —
		// the frontend resolves the id against a fixed registry to get the label.
		{campaignOne, "Festive flash sale", "SMS", "ACMERT", "JIO"},
		{campaignTwo, "Weekend RCS promo", "RCS", "ACMERT", "AIRTEL"},
		// Email, WhatsApp and Voice traffic with an EMPTY campaign id — these are
		// direct API sends, which is how most transactional traffic on those
		// channels actually arrives. Seeding campaigns for them instead would add
		// rows to the campaigns list that the campaign specs count, breaking
		// assertions that have nothing to do with analytics.
		//
		// Without this the analytics screen had no Email, WhatsApp or Voice
		// traffic at all, so every channel-specific tile — bounces, conversations,
		// answered rate — had nothing to compute from and rendered empty on a
		// product that supports five channels.
		{"", "Shipment notices", "EMAIL", "Acme Notifications", "JIO"},
		{"", "Order updates", "WHATSAPP", "Acme Retail", "JIO"},
		{"", "Delivery calls", "VOICE", "Acme Support Line", "AIRTEL"},
	}
	// Weighted so the dashboard shows a believable delivery rate rather than a
	// suspiciously perfect one, and so every status the UI can render exists.
	// These are the real lifecycle states from internal/domain/messaging, not
	// invented labels. The analytics rollup counts "attempted" as accepted +
	// submitted + rejected, so a seed using its own vocabulary would sum to zero
	// and every chart would render empty while looking correctly wired.
	outcomes := []struct {
		status, errorCode, errorClass, fraud string
		weight                               int
	}{
		{"delivered", "", "", "none", 74},
		{"delivered", "", "", "none", 12},
		// errorClass values come from the contract's enum, not from words that
		// merely describe the failure. "policy" and "handset" read fine in a log
		// line and make the logs page throw, because the frontend resolves the
		// class against a fixed set.
		{"undelivered", "DND", "blocked", "none", 5},
		{"undelivered", "ABSENT_SUBSCRIBER", "unreachable", "none", 3},
		{"rejected", "SPAM_FILTERED", "rejected", "velocity", 2},
		{"accepted", "", "", "none", 4},
	}
	// Email, WhatsApp and Voice fail in ways SMS does not, and the tiles on the
	// analytics screen exist to show exactly those differences. An email that
	// bounces is not "undelivered by a carrier", it is a mailbox that rejected
	// it; a voice call that is not answered is not a failure at all, it is the
	// normal outcome for a third of calls. Reusing the SMS profile for all five
	// channels would make every tile report the same shape and prove nothing.
	channelOutcomes := map[string][]struct {
		status, errorCode, errorClass, fraud string
		weight                               int
	}{
		"EMAIL": {
			{"delivered", "", "", "none", 82},
			// A bounce is the mailbox being unreachable — the closest honest
			// member of the contract's MessageErrorClass enum.
			{"undelivered", "BOUNCED_HARD", "unreachable", "none", 9},
			{"undelivered", "BOUNCED_SOFT", "unreachable", "none", 5},
			{"rejected", "SPAM_FILTERED", "rejected", "velocity", 2},
			{"accepted", "", "", "none", 2},
		},
		"WHATSAPP": {
			{"delivered", "", "", "none", 88},
			{"undelivered", "NOT_ON_WHATSAPP", "unreachable", "none", 6},
			{"rejected", "TEMPLATE_PAUSED", "rejected", "none", 3},
			{"accepted", "", "", "none", 3},
		},
		"VOICE": {
			// Answered. Roughly two thirds, which is what a real outbound
			// campaign sees — a fixture at 95% would let a broken answered-rate
			// calculation look plausible.
			{"delivered", "", "", "none", 64},
			{"undelivered", "NO_ANSWER", "unreachable", "none", 24},
			{"undelivered", "BUSY", "unreachable", "none", 6},
			{"rejected", "INVALID_NUMBER", "rejected", "none", 3},
			{"accepted", "", "", "none", 3},
		},
	}

	// weightedFor expands a profile into an index list so a draw is a single
	// array lookup rather than a running total on every message.
	weightedFor := func(profile []struct {
		status, errorCode, errorClass, fraud string
		weight                               int
	}) []int {
		var out []int
		for index, outcome := range profile {
			for count := 0; count < outcome.weight; count++ {
				out = append(out, index)
			}
		}
		return out
	}

	var weighted []int
	for index, outcome := range outcomes {
		for count := 0; count < outcome.weight; count++ {
			weighted = append(weighted, index)
		}
	}
	weightedByChannel := map[string][]int{}
	for channel, profile := range channelOutcomes {
		weightedByChannel[channel] = weightedFor(profile)
	}

	now := time.Now().UTC()
	counts := map[[4]string]struct{ messages, segments, cost int64 }{}
	step := 0
	for day := 29; day >= 0; day-- {
		// More traffic on recent days so the trend line slopes rather than
		// sitting flat, which is what makes a chart worth looking at.
		perDay := 40 + (29-day)*2
		for index := 0; index < perDay; index++ {
			campaign := campaigns[step%len(campaigns)]
			outcome := outcomes[weighted[(step*7)%len(weighted)]]
			if profile, ok := channelOutcomes[campaign.channel]; ok {
				picks := weightedByChannel[campaign.channel]
				outcome = profile[picks[(step*7)%len(picks)]]
			}
			country := "IN"
			if step%11 == 0 {
				country = "US"
			}
			createdAt := now.AddDate(0, 0, -day).
				Add(time.Duration(9+index%10) * time.Hour).
				Add(time.Duration(index%60) * time.Minute)
			// Per-channel pricing, matching the seeded rate card rather than
			// pretending every channel costs the same. A cost chart where email
			// and voice price identically cannot show the thing it exists for.
			cost := int64(12)
			switch campaign.channel {
			case "RCS":
				cost = 21
			case "EMAIL":
				cost = 8
			case "WHATSAPP":
				cost = 55
			case "VOICE":
				cost = 45
			}
			messageID := uuid.New()
			var deliveredAt *time.Time
			if outcome.status == "delivered" {
				// Spread rather than a constant: a fixture where every message
				// takes exactly 4s makes p50 and p90 identical, which is the one
				// thing real latency never looks like. The long tail is what the
				// p90 on the dashboard exists to show.
				delay := 900*time.Millisecond + time.Duration((step*37)%5200)*time.Millisecond
				if step%17 == 0 {
					delay += 9 * time.Second // the slow tail every network has
				}
				moment := createdAt.Add(delay)
				deliveredAt = &moment
			}
			sentAt := createdAt.Add(time.Second)

			// Direct API sends carry no campaign, so both id and name are null
			// rather than an empty string that would render as a nameless
			// campaign row in the logs explorer.
			var campaignID *uuid.UUID
			var campaignName *string
			if campaign.id != "" {
				parsed := uuid.MustParse(campaign.id)
				campaignID, campaignName = &parsed, &campaign.name
			}
			// Email addresses belong on email rows and nowhere else; the logs
			// explorer shows whichever identity the channel actually used.
			var email *string
			if campaign.channel == "EMAIL" {
				address := fmt.Sprintf("customer%03d@example.com", step%250)
				email = &address
			}

			if err := messages.Append(tenantUUID, messageID,
				campaignID, campaignName,
				nil, nil, nil, campaign.channel, country, campaign.sender, nil,
				fmt.Sprintf("+9198765%05d", step%100000), email,
				outcome.status, nil,
				nullableString(outcome.errorCode), nullableString(outcome.errorClass),
				outcome.fraud, uint8(1), cost, "INR", nil, nil, campaign.carrier,
				createdAt, &sentAt, deliveredAt, createdAt, uint64(1),
			); err != nil {
				return fmt.Errorf("append message: %w", err)
			}

			// The rollup counts a message once, under its final status. Counting
			// every transition would inflate "sent" — a bug this backend has
			// already had once.
			// One row per TRANSITION, matching what the ingest path writes:
			// an attempt row plus the outcome row. The analytics summary counts
			// attempts from the first and outcomes from the second, so writing
			// only the outcome would report a delivery rate over a zero base.
			hour := createdAt.Truncate(time.Hour).Format(time.RFC3339)
			attempt := "accepted"
			if outcome.status == "rejected" {
				// A rejected message was never accepted by anyone.
				attempt = "rejected"
			}
			for _, status := range []string{attempt, outcome.status} {
				key := [4]string{hour, campaign.channel, country, status}
				entry := counts[key]
				entry.messages++
				entry.segments++
				if status == "delivered" {
					entry.cost += cost
				}
				counts[key] = entry
				if attempt == outcome.status {
					break // rejected is both the attempt and the outcome
				}
			}
			step++
		}
	}
	if err := messages.Send(); err != nil {
		return fmt.Errorf("send messages: %w", err)
	}
	for key, entry := range counts {
		hour, _ := time.Parse(time.RFC3339, key[0])
		if err := rollup.Append(tenantUUID, hour, key[1], key[2], key[3],
			uint64(entry.messages), uint64(entry.segments), entry.cost, "INR"); err != nil {
			return fmt.Errorf("append rollup: %w", err)
		}
	}
	if err := rollup.Send(); err != nil {
		return fmt.Errorf("send rollup: %w", err)
	}
	return nil
}

// nullableString keeps empty strings out of Nullable columns, where "" and NULL
// mean different things: no error versus an error with a blank code.
func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// seedUserActivity backfills 90 days of account-security events.
//
// The operator console's activity table is filtered by tenant, by event type
// and by a 7/30/90-day range, and every one of those controls needs rows on
// BOTH sides of the filter to prove it works: a table that is empty whatever
// you pick looks identical to a filter that silently matches nothing. So every
// tenant gets events, and the events span all ten types across the full window.
//
// These are synthetic history, written directly rather than through
// RecordUserActivity, because they describe things that happened before this
// database existed. Everything from now on is recorded by the code that
// performs the action.
func seedUserActivity(ctx context.Context, pool *pgxpool.Pool, tenantIDs []string) error {
	// A repeating pattern rather than a random draw, so the counts on screen
	// are the same on every machine and a spec can assert on them. login comes
	// first and recurs most often because it genuinely is the common event —
	// people sign in far more than they rotate keys.
	events := []struct{ eventType, detail string }{
		{"login", "Signed in"},
		{"login", "Signed in"},
		{"api_key.create", "Created a live key"},
		{"login", "Signed in"},
		{"mfa.enroll", "Enabled two-factor authentication"},
		{"login", "Signed in"},
		{"session.revoke", "Signed out another device"},
		{"login", "Signed in"},
		{"team.invite", "Invited a teammate"},
		{"api_key.rotate", "Rotated a live key"},
		{"login", "Signed in"},
		{"team.role_change", "Changed a teammate's role"},
		{"login", "Signed in"},
		{"api_key.revoke", "Revoked a key"},
		{"sso.config_change", "Enabled SSO"},
		{"login", "Signed in"},
		{"mfa.disable", "Disabled two-factor authentication"},
	}

	// Each tenant's events are attributed to a real member where one exists,
	// and to a plausible stand-in where the tenant has no users seeded. The
	// name and email are stored on the row itself, which is what makes the
	// second case work at all — and what keeps the first readable after that
	// person leaves the team.
	// Cleared first, for the tenants that survive a reset.
	//
	// The demo tenant is deleted and rebuilt, so its rows cascade away on their
	// own. The other seven are upserted rather than deleted — so without this,
	// every reset between specs would append another 17 events each, and a
	// suite that resets twice per test would end the run with tens of thousands
	// of rows and an activity table that had quietly become a load test.
	if _, err := pool.Exec(ctx,
		`DELETE FROM user_activity WHERE tenant_id = ANY($1::uuid[])`, tenantIDs); err != nil {
		return fmt.Errorf("clear user activity: %w", err)
	}

	for tenantIndex, tenantID := range tenantIDs {
		for eventIndex, event := range events {
			// Spread back across the full 90 days, offset per tenant so the
			// unfiltered list interleaves customers instead of showing one
			// tenant's whole history before the next one starts.
			hoursAgo := eventIndex*127 + tenantIndex*13
			if _, err := pool.Exec(ctx, `
				INSERT INTO user_activity (tenant_id, user_id, user_name, user_email,
				                           event_type, detail, occurred_at)
				SELECT $1,
				       u.id,
				       coalesce(nullif(u.name, ''), 'Account owner'),
				       coalesce(u.email::text, 'owner@' || lower(replace(t.name, ' ', '')) || '.example'),
				       $2, $3,
				       now() - make_interval(hours => $4::int)
				FROM tenants t
				LEFT JOIN LATERAL (
				    SELECT u.id, u.name, u.email
				    FROM tenant_users tu
				    JOIN users u ON u.id = tu.user_id
				    WHERE tu.tenant_id = t.id
				    ORDER BY tu.created_at
				    LIMIT 1
				) u ON true
				WHERE t.id = $1`,
				tenantID, event.eventType, event.detail, hoursAgo); err != nil {
				return fmt.Errorf("seed user activity: %w", err)
			}
		}
	}
	return nil
}
