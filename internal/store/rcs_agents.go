package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RcsAgent is a customer's own RCS brand identity.
//
// The customer owns it and may hold several — a retailer separating order
// notifications from marketing is the ordinary case, and the two have different
// use cases, which carriers price and review differently. That is why this is a
// collection under the tenant rather than a column on it.
type RcsAgent struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	DisplayName       string
	Description       *string
	LogoAssetID       *uuid.UUID
	HeroAssetID       *uuid.UUID
	PrimaryColor      *string
	PhoneNumber       *string
	Email             *string
	Website           *string
	PrivacyPolicyURL  *string
	TermsOfServiceURL *string
	UseCase           string
	Country           string
	RegistrationID    *string
	Status            string
	RejectionReason   *string

	Verification RcsAgentVerification
	Launches     []RcsCarrierLaunch

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RcsAgentVerification is the brand-verification packet and its review.
//
// A separate axis from carrier launch, not a stage before it: an agent can be
// verified and still reach nobody because every carrier refused it.
type RcsAgentVerification struct {
	Status          string
	ContactName     *string
	ContactEmail    *string
	ContactPhone    *string
	DocumentAssetID *uuid.UUID
	RejectionReason *string
	SubmittedAt     *time.Time
	ReviewedAt      *time.Time
}

// RcsCarrierLaunch is one carrier's admission of one agent.
type RcsCarrierLaunch struct {
	Carrier         string
	Status          string
	CarrierAgentID  *string
	RejectionReason *string
	SubmittedAt     *time.Time
	UpdatedAt       time.Time
}

// RcsAgentFilter narrows the customer's list.
type RcsAgentFilter struct {
	Status  *string
	Country *string
	Search  *string
	Page    int
	Limit   int
}

const rcsAgentColumns = `a.id, a.tenant_id, a.display_name, a.description,
	a.logo_asset_id, a.hero_asset_id, a.primary_color, a.phone_number, a.email,
	a.website, a.privacy_policy_url, a.terms_of_service_url, a.use_case,
	a.country, a.registration_id, a.status, a.rejection_reason,
	a.verification_status, a.verification_contact_name, a.verification_contact_email,
	a.verification_contact_phone, a.verification_document_id,
	a.verification_rejection_reason, a.verification_submitted_at,
	a.verification_reviewed_at, a.created_at, a.updated_at`

func scanRcsAgent(row pgx.Row) (RcsAgent, error) {
	var a RcsAgent
	err := row.Scan(&a.ID, &a.TenantID, &a.DisplayName, &a.Description,
		&a.LogoAssetID, &a.HeroAssetID, &a.PrimaryColor, &a.PhoneNumber, &a.Email,
		&a.Website, &a.PrivacyPolicyURL, &a.TermsOfServiceURL, &a.UseCase,
		&a.Country, &a.RegistrationID, &a.Status, &a.RejectionReason,
		&a.Verification.Status, &a.Verification.ContactName, &a.Verification.ContactEmail,
		&a.Verification.ContactPhone, &a.Verification.DocumentAssetID,
		&a.Verification.RejectionReason, &a.Verification.SubmittedAt,
		&a.Verification.ReviewedAt, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// ListRcsAgents answers one page of a tenant's agents, with the total for the
// filtered set — filters in the WHERE of both queries, so the total cannot
// describe a different set from the rows beside it.
func ListRcsAgents(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter RcsAgentFilter) ([]RcsAgent, int, error) {

	limit, offset := pageWindow(filter.Page, filter.Limit)
	where := `
		FROM rcs_agents a
		WHERE ($1::text IS NULL OR a.status  = $1)
		  AND ($2::text IS NULL OR a.country = $2)
		  AND ($3::text IS NULL OR a.display_name ILIKE '%' || $3 || '%')`
	args := []any{filter.Status, filter.Country, filter.Search}

	var out []RcsAgent
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+rcsAgentColumns+where+`
			ORDER BY a.created_at DESC, a.id DESC
			LIMIT $4 OFFSET $5`, append(args, limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			agent, err := scanRcsAgent(rows)
			if err != nil {
				return err
			}
			out = append(out, agent)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list rcs agents: %w", err)
	}
	if err := attachLaunches(ctx, pool, id, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// attachLaunches fills in every agent's carrier rows in ONE query, rather than
// one per agent. A page of twenty agents must not cost twenty round trips —
// the same defect the campaigns list carried against the message log.
func attachLaunches(ctx context.Context, pool *pgxpool.Pool, id Identity, agents []RcsAgent) error {
	if len(agents) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	byAgent := map[uuid.UUID][]RcsCarrierLaunch{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT agent_id, carrier, status, carrier_agent_id, rejection_reason,
			       submitted_at, updated_at
			FROM rcs_agent_carrier_launches
			WHERE agent_id = ANY($1)
			ORDER BY carrier`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var agentID uuid.UUID
			var l RcsCarrierLaunch
			if err := rows.Scan(&agentID, &l.Carrier, &l.Status, &l.CarrierAgentID,
				&l.RejectionReason, &l.SubmittedAt, &l.UpdatedAt); err != nil {
				return err
			}
			byAgent[agentID] = append(byAgent[agentID], l)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("store: attach carrier launches: %w", err)
	}
	for i := range agents {
		agents[i].Launches = byAgent[agents[i].ID]
	}
	return nil
}

// GetRcsAgent reads one agent. Another tenant's agent is ErrNotFound and not a
// refusal, so the id space cannot be used to enumerate who the customers are.
func GetRcsAgent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agentID uuid.UUID) (RcsAgent, error) {

	var agent RcsAgent
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		agent, err = scanRcsAgent(tx.QueryRow(ctx,
			`SELECT `+rcsAgentColumns+` FROM rcs_agents a WHERE a.id = $1`, agentID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RcsAgent{}, ErrNotFound
	}
	if err != nil {
		return RcsAgent{}, fmt.Errorf("store: get rcs agent: %w", err)
	}
	one := []RcsAgent{agent}
	if err := attachLaunches(ctx, pool, id, one); err != nil {
		return RcsAgent{}, err
	}
	return one[0], nil
}

// CreateRcsAgent writes a draft.
func CreateRcsAgent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agent RcsAgent) (RcsAgent, error) {

	var created uuid.UUID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO rcs_agents (tenant_id, display_name, description, use_case,
			                        country, registration_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			id.TenantID, agent.DisplayName, agent.Description, agent.UseCase,
			agent.Country, agent.RegistrationID).Scan(&created)
	})
	if err != nil {
		return RcsAgent{}, fmt.Errorf("store: create rcs agent: %w", err)
	}
	return GetRcsAgent(ctx, pool, id, created)
}

// RcsAgentUpdate carries only the fields a PATCH named. A nil pointer means the
// caller did not mention the field, and the column is left alone.
//
// NOTE, because it is a real limit rather than an omission: the contract's
// optional fields are plain nullable values, so `{"website": null}` and a body
// that never mentions website arrive here identically. There is therefore no
// way to CLEAR a field once set. Flagged to the frontend rather than worked
// around, since inventing a sentinel would put a second meaning on a value the
// contract says is simply absent.
type RcsAgentUpdate struct {
	DisplayName       *string
	Description       *string
	LogoAssetID       *uuid.UUID
	HeroAssetID       *uuid.UUID
	PrimaryColor      *string
	PhoneNumber       *string
	Email             *string
	Website           *string
	PrivacyPolicyURL  *string
	TermsOfServiceURL *string
	UseCase           *string
	RegistrationID    *string

	// Cleared names the fields the request sent as an explicit null, keyed by
	// the contract's own field names.
	//
	// It exists because a nil pointer above means two different things — "not
	// mentioned" and "set to null" — and the generated struct cannot tell them
	// apart. That is JSON Merge Patch (RFC 7386), and the alternative was
	// inventing a sentinel value, which gives some ordinary string a second
	// meaning forever.
	Cleared map[string]bool
}

// UpdateRcsAgent applies a PATCH with JSON Merge Patch semantics.
//
// Three cases per field, and coalesce alone carries only two of them: a NULL
// argument means "keep", so it can never also mean "clear". The flag beside
// each value is the third — it is the difference between a key the request
// omitted and a key it sent as null, which the generated struct collapses into
// one nil pointer.
//
// The SQL text is fixed rather than assembled per request. Building a SET
// clause from field names would work and would put caller-influenced strings
// into a statement, which is a thing to avoid on principle rather than after an
// argument about whether this particular map is safe. A query whose text does
// not vary is also one the planner caches and a reader can check.
//
// display_name and use_case take no flag: neither is nullable, and an agent
// with no name is not an agent.
func UpdateRcsAgent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agentID uuid.UUID, update RcsAgentUpdate) (RcsAgent, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE rcs_agents SET
			    display_name         = coalesce($2,  display_name),
			    use_case             = coalesce($3,  use_case),
			    description          = CASE WHEN $14 THEN NULL ELSE coalesce($4,  description)          END,
			    logo_asset_id        = CASE WHEN $15 THEN NULL ELSE coalesce($5,  logo_asset_id)        END,
			    hero_asset_id        = CASE WHEN $16 THEN NULL ELSE coalesce($6,  hero_asset_id)        END,
			    primary_color        = CASE WHEN $17 THEN NULL ELSE coalesce($7,  primary_color)        END,
			    phone_number         = CASE WHEN $18 THEN NULL ELSE coalesce($8,  phone_number)         END,
			    email                = CASE WHEN $19 THEN NULL ELSE coalesce($9,  email)                END,
			    website              = CASE WHEN $20 THEN NULL ELSE coalesce($10, website)              END,
			    privacy_policy_url   = CASE WHEN $21 THEN NULL ELSE coalesce($11, privacy_policy_url)   END,
			    terms_of_service_url = CASE WHEN $22 THEN NULL ELSE coalesce($12, terms_of_service_url) END,
			    registration_id      = CASE WHEN $23 THEN NULL ELSE coalesce($13, registration_id)      END,
			    updated_at           = now()
			WHERE id = $1`,
			agentID, update.DisplayName, update.UseCase, update.Description,
			update.LogoAssetID, update.HeroAssetID, update.PrimaryColor,
			update.PhoneNumber, update.Email, update.Website,
			update.PrivacyPolicyURL, update.TermsOfServiceURL, update.RegistrationID,
			update.Cleared["description"], update.Cleared["logoAssetId"],
			update.Cleared["heroImageAssetId"], update.Cleared["primaryColor"],
			update.Cleared["phoneNumber"], update.Cleared["email"],
			update.Cleared["website"], update.Cleared["privacyPolicyUrl"],
			update.Cleared["termsOfServiceUrl"], update.Cleared["registrationId"])
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return RcsAgent{}, ErrNotFound
		}
		return RcsAgent{}, fmt.Errorf("store: update rcs agent: %w", err)
	}
	return GetRcsAgent(ctx, pool, id, agentID)
}

// ErrIllegalTransition is a lifecycle refusal: the agent exists and is this
// tenant's, and the thing being asked of it is not legal from where it is.
// Distinct from ErrNotFound so the API can answer 409 rather than 404.
var ErrIllegalTransition = errors.New("store: illegal agent transition")

// SubmitRcsAgentVerification moves a draft or a rejected agent into review.
//
// The state test is IN THE UPDATE, not a read followed by a write. Two
// submissions racing would otherwise both read 'draft' and both write, and the
// second would overwrite the first reviewer's queue row.
func SubmitRcsAgentVerification(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agentID uuid.UUID, contactName, contactEmail, contactPhone string,
	documentID uuid.UUID) (RcsAgent, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE rcs_agents SET
			    status                        = 'verification_submitted',
			    verification_status           = 'pending',
			    verification_contact_name     = $2,
			    verification_contact_email    = $3,
			    verification_contact_phone    = $4,
			    verification_document_id      = $5,
			    verification_rejection_reason = NULL,
			    verification_submitted_at     = now(),
			    verification_reviewed_at      = NULL,
			    rejection_reason              = NULL,
			    updated_at                    = now()
			WHERE id = $1 AND status IN ('draft', 'verification_rejected')`,
			agentID, contactName, contactEmail, contactPhone, documentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return transitionOrNotFound(ctx, tx, agentID)
		}
		return nil
	})
	if err != nil {
		return RcsAgent{}, wrapAgentError("submit rcs agent verification", err)
	}
	return GetRcsAgent(ctx, pool, id, agentID)
}

// ReviewRcsAgent is the operator's approve or reject. It runs on the operator
// pool, which is not subject to the tenant policy — reviewing every tenant's
// queue is the entire point of the console.
//
// reason is required on a rejection and refused on an approval: a rejection the
// customer cannot read is a dead end, and the column constraint enforces the
// same pairing so neither path can write a half-state.
func ReviewRcsAgent(ctx context.Context, pool *pgxpool.Pool, agentID uuid.UUID,
	approve bool, reason string) error {

	status, verification := "verification_approved", "approved"
	var storedReason *string
	if !approve {
		status, verification = "verification_rejected", "rejected"
		storedReason = &reason
	}
	tag, err := pool.Exec(ctx, `
		UPDATE rcs_agents SET
		    status                        = $2,
		    verification_status           = $3,
		    verification_rejection_reason = $4,
		    rejection_reason              = $4,
		    verification_reviewed_at      = now(),
		    updated_at                    = now()
		WHERE id = $1 AND status = 'verification_submitted'`,
		agentID, status, verification, storedReason)
	if err != nil {
		return fmt.Errorf("store: review rcs agent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT true FROM rcs_agents WHERE id = $1`, agentID).Scan(&exists); err != nil {
			return ErrNotFound
		}
		return ErrIllegalTransition
	}
	return nil
}

// LaunchRcsAgentOnCarrier records that an agent has been put to one carrier.
//
// One carrier per call, and the top-level status only ever moves
// verification_approved -> launch_pending. A LIVE agent stays live when another
// carrier opens a review: one carrier beginning to look does not withdraw the
// reach the agent already has on another network.
func LaunchRcsAgentOnCarrier(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agentID uuid.UUID, carrier string) (RcsAgent, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM rcs_agents WHERE id = $1 FOR UPDATE`, agentID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "verification_approved" && status != "launch_pending" && status != "live" {
			return ErrIllegalTransition
		}

		// ON CONFLICT DO NOTHING with the state test in the WHERE, so a second
		// launch on a carrier already pending or approved is refused rather
		// than resetting its review.
		tag, err := tx.Exec(ctx, `
			INSERT INTO rcs_agent_carrier_launches
			    (agent_id, tenant_id, carrier, status, submitted_at, updated_at)
			VALUES ($1, $2, $3, 'pending', now(), now())
			ON CONFLICT (agent_id, carrier) DO UPDATE
			    SET status = 'pending', submitted_at = now(), updated_at = now(),
			        rejection_reason = NULL
			    WHERE rcs_agent_carrier_launches.status IN ('not_submitted', 'rejected')`,
			agentID, id.TenantID, carrier)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrIllegalTransition
		}

		if status == "verification_approved" {
			if _, err := tx.Exec(ctx,
				`UPDATE rcs_agents SET status = 'launch_pending', updated_at = now()
				 WHERE id = $1`, agentID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RcsAgent{}, wrapAgentError("launch rcs agent", err)
	}
	return GetRcsAgent(ctx, pool, id, agentID)
}

// transitionOrNotFound tells a refused transition apart from a missing agent,
// so the API can answer 409 or 404 rather than guessing.
func transitionOrNotFound(ctx context.Context, tx pgx.Tx, agentID uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT true FROM rcs_agents WHERE id = $1`, agentID).Scan(&exists); err != nil {
		return ErrNotFound
	}
	return ErrIllegalTransition
}

func wrapAgentError(what string, err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrIllegalTransition) {
		return err
	}
	return fmt.Errorf("store: %s: %w", what, err)
}

// CarriersForCountry is the set of carriers a country has a configured RCS
// route for, plus every carrier that operates there without one.
//
// DERIVED from routes rather than a hardcoded table, so a corridor configured
// tomorrow appears on every agent's launch screen with no second list to keep
// in step. A copied table is one deploy away from telling a customer a network
// does not exist.
func CarriersForCountry(ctx context.Context, pool *pgxpool.Pool, country string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT carrier FROM routes
		WHERE country = $1 AND channel = 'RCS'
		ORDER BY carrier`, country)
	if err != nil {
		return nil, fmt.Errorf("store: carriers for country: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var carrier string
		if err := rows.Scan(&carrier); err != nil {
			return nil, err
		}
		out = append(out, carrier)
	}
	return out, rows.Err()
}

// HasApprovedRegistration reports whether the tenant holds an approved business
// entity for a country.
//
// An RCS agent carries the customer's registered brand identity, and that
// identity is the one the compliance spine already approved — there is no
// second brand record, deliberately, because two records for one legal fact is
// two sources of truth. So an agent cannot exist before the entity behind it
// does.
func HasApprovedRegistration(ctx context.Context, pool *pgxpool.Pool, id Identity,
	country string) (bool, error) {

	var approved bool
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM registrations
			    WHERE country = $1 AND status = 'approved')`, country).Scan(&approved)
	})
	if err != nil {
		return false, fmt.Errorf("store: check approved registration: %w", err)
	}
	return approved, nil
}

// CarrierAgentID is the id one carrier issued for one agent — Airtel's agentId,
// Vi's botId — and it is what the send path puts on the wire.
//
// Three conditions, and every one of them can change after the sender was
// registered, which is why this is asked at SEND time rather than trusted from
// registration:
//
//   - the launch is approved and carries an id. A pending launch has no id to
//     send under, and a rejected one never will.
//   - the agent is not suspended or archived. An agent approved in June is not
//     evidence about today; suspension is the whole reason to re-check.
//   - the agent belongs to this tenant. RLS enforces that rather than a WHERE
//     clause, so a stolen agent id reads as "not found" and not as a leak.
//
// ErrNotFound means "this agent cannot send on this carrier", and the caller
// must NOT substitute the deployment's shared agent for it: a tenant that owns
// an agent and sends under someone else's identity is the failure this whole
// feature exists to remove.
func CarrierAgentID(ctx context.Context, pool *pgxpool.Pool, id Identity,
	agentID uuid.UUID, carrier string) (string, error) {

	var carrierAgentID string
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT l.carrier_agent_id
			  FROM rcs_agent_carrier_launches l
			  JOIN rcs_agents a ON a.id = l.agent_id
			 WHERE l.agent_id = $1
			   AND l.carrier = $2
			   AND l.status = 'approved'
			   AND l.carrier_agent_id IS NOT NULL
			   AND a.status NOT IN ('suspended', 'archived')`,
			agentID, carrier).Scan(&carrierAgentID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: carrier agent id: %w", err)
	}
	return carrierAgentID, nil
}

// CachedCarrierAgentID is CarrierAgentID with the send path's cache in front,
// for the same reason CachedSenderID exists: this is one more round trip on
// every single RCS message, asking a question whose answer changes when an
// operator or a carrier changes it, not between two messages in a burst.
//
// The TTL is the blast radius, exactly as it is for a suspended tenant: an
// agent suspended at t keeps sending until t+TTL. That window is the same one
// the send path already accepts for tenant status, and it is shorter than the
// time it takes anyone to notice the button was clicked.
//
// A miss is cached too. Without that, the failing case — an agent with no
// approved launch — pays the full round trip on every message of a campaign
// that is refusing all of them anyway.
func CachedCarrierAgentID(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	id Identity, agentID uuid.UUID, carrier string) (string, error) {

	key := "rcsagent:" + id.TenantID.String() + ":" + agentID.String() + ":" + carrier
	if value, found := cache.Get(key); found {
		carrierAgentID := value.(string)
		if carrierAgentID == "" {
			return "", ErrNotFound
		}
		return carrierAgentID, nil
	}
	carrierAgentID, err := CarrierAgentID(ctx, pool, id, agentID, carrier)
	if errors.Is(err, ErrNotFound) {
		cache.Put(key, "")
		return "", err
	}
	if err != nil {
		return "", err
	}
	cache.Put(key, carrierAgentID)
	return carrierAgentID, nil
}

// TenantForCarrierAgent resolves an inbound carrier event to the tenant that
// owns the agent it names.
//
// On an inbound RCS event the carrier's agent id is the ONLY tenant
// discriminator there is: the payload carries no tenant, and unlike a delivery
// report there is no message of ours to look the sender up from. The unique
// index rcs_launch_carrier_identity on (carrier, carrier_agent_id) is what
// makes this answer single-valued — without it two tenants could collide on one
// carrier id and each would receive the other's inbound messages.
//
// Read on the OPERATOR pool, and it has to be: the caller is a carrier, there
// is no session, and a tenant-scoped read would have to already know the answer
// it is asking for.
func TenantForCarrierAgent(ctx context.Context, pool *pgxpool.Pool,
	carrier, carrierAgentID string) (uuid.UUID, uuid.UUID, error) {

	var tenantID, agentID uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT tenant_id, agent_id
		  FROM rcs_agent_carrier_launches
		 WHERE carrier = $1 AND carrier_agent_id = $2`,
		carrier, carrierAgentID).Scan(&tenantID, &agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("store: tenant for carrier agent: %w", err)
	}
	return tenantID, agentID, nil
}

// PendingRcsAgent is one operator queue row.
type PendingRcsAgent struct {
	RcsAgent
	TenantName string
}

// ListPendingRcsAgents reads agents awaiting a brand-verification decision,
// across every tenant.
//
// Operator pool, which is not subject to the tenant policy: working across
// tenants is the entire point of the console. The status filter is applied here
// rather than in memory for the same reason the other three queue sources apply
// it in SQL — a queue defaulting to pending must not read every approved agent
// ever just to discard them.
func ListPendingRcsAgents(ctx context.Context, pool *pgxpool.Pool,
	status *string) ([]PendingRcsAgent, error) {

	// The queue's vocabulary is approval status, not agent lifecycle: an
	// operator asks for "pending" and means "awaiting my decision", which for
	// an agent is verification_submitted.
	wanted := map[string]string{
		"pending":  "verification_submitted",
		"approved": "verification_approved",
		"rejected": "verification_rejected",
	}
	var agentStatus *string
	if status != nil {
		mapped, ok := wanted[*status]
		if !ok {
			// A status this queue cannot express matches nothing, rather than
			// everything. The same rule the message log's status filter now
			// follows, and for the same reason.
			return nil, nil
		}
		agentStatus = &mapped
	}

	rows, err := pool.Query(ctx, `
		SELECT `+rcsAgentColumns+`, t.name
		FROM rcs_agents a
		JOIN tenants t ON t.id = a.tenant_id
		WHERE ($1::text IS NULL OR a.status = $1)
		  AND a.status IN ('verification_submitted', 'verification_approved',
		                   'verification_rejected')
		ORDER BY a.created_at DESC, a.id DESC`, agentStatus)
	if err != nil {
		return nil, fmt.Errorf("store: list pending rcs agents: %w", err)
	}
	defer rows.Close()

	var out []PendingRcsAgent
	for rows.Next() {
		var p PendingRcsAgent
		if err := rows.Scan(&p.ID, &p.TenantID, &p.DisplayName, &p.Description,
			&p.LogoAssetID, &p.HeroAssetID, &p.PrimaryColor, &p.PhoneNumber, &p.Email,
			&p.Website, &p.PrivacyPolicyURL, &p.TermsOfServiceURL, &p.UseCase,
			&p.Country, &p.RegistrationID, &p.Status, &p.RejectionReason,
			&p.Verification.Status, &p.Verification.ContactName, &p.Verification.ContactEmail,
			&p.Verification.ContactPhone, &p.Verification.DocumentAssetID,
			&p.Verification.RejectionReason, &p.Verification.SubmittedAt,
			&p.Verification.ReviewedAt, &p.CreatedAt, &p.UpdatedAt, &p.TenantName); err != nil {
			return nil, fmt.Errorf("store: scan pending rcs agent: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetRcsAgentAsOperator reads one agent without a tenant scope.
//
// The operator console is the only caller: a reviewer is not the tenant, so
// there is no tenant policy to satisfy and none to lean on. Kept separate from
// GetRcsAgent rather than adding a flag, so that every unscoped read of this
// table is greppable by name.
func GetRcsAgentAsOperator(ctx context.Context, pool *pgxpool.Pool,
	agentID uuid.UUID) (RcsAgent, error) {

	agent, err := scanRcsAgent(pool.QueryRow(ctx,
		`SELECT `+rcsAgentColumns+` FROM rcs_agents a WHERE a.id = $1`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return RcsAgent{}, ErrNotFound
	}
	if err != nil {
		return RcsAgent{}, fmt.Errorf("store: get rcs agent as operator: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT carrier, status, carrier_agent_id, rejection_reason, submitted_at, updated_at
		FROM rcs_agent_carrier_launches WHERE agent_id = $1 ORDER BY carrier`, agentID)
	if err != nil {
		return RcsAgent{}, fmt.Errorf("store: get rcs agent launches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l RcsCarrierLaunch
		if err := rows.Scan(&l.Carrier, &l.Status, &l.CarrierAgentID,
			&l.RejectionReason, &l.SubmittedAt, &l.UpdatedAt); err != nil {
			return RcsAgent{}, err
		}
		agent.Launches = append(agent.Launches, l)
	}
	return agent, rows.Err()
}
