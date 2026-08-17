package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/demoseed"
	"github.com/saeedafri/sms-be/internal/store"
)

// Test hooks under /v1/dev/*.
//
// The browser suite needs states a real system produces slowly or not at all:
// a sender a regulator approved, a campaign that finished, a wallet at zero, a
// caller demoted to member. Waiting for a carrier is not an option in a test,
// and reaching into the database from the spec would couple the frontend's
// suite to our schema. These endpoints are the seam.
//
// They are NOT part of the 151-operation contract. They exist only because the
// frontend's specs already call them — the UI ships /api/dev/* proxies that
// forward here — and they are registered only when ENABLE_DEV_ENDPOINTS is
// exactly "true".
//
// Every one of them acts on the CALLER'S OWN tenant, taken from the session
// rather than from the request body. A test hook that accepted a tenant id
// would be a cross-tenant write primitive, which is the one thing this system
// must never have — dev build or not.
func (s *Server) mountDevRoutes(r chi.Router) {
	r.Route("/v1/dev", func(dev chi.Router) {
		dev.Post("/advance-campaign", s.devAdvanceCampaign)
		dev.Post("/advance-sender", s.devAdvanceSender)
		dev.Post("/advance-template", s.devAdvanceTemplate)
		dev.Post("/advance-registration", s.devAdvanceRegistration)
		dev.Post("/advance-email-dns", s.devAdvanceEmailDNS)
		dev.Post("/drain-wallet", s.devDrainWallet)
		dev.Post("/set-my-role", s.devSetMyRole)
		dev.Post("/receive-inbound-message", s.devReceiveInbound)
		dev.Post("/reset-mock-state", s.devResetState)
	})
}

// devRequest decodes the body and resolves the caller. Every hook needs both,
// and each one returning its own 401 shape would drift.
func devRequest(w http.ResponseWriter, r *http.Request, body any) (store.Identity, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		// Fall back to the demo tenant rather than refusing.
		//
		// Playwright's bare `request` fixture is a separate context from `page`
		// and carries none of the browser's cookies, so a spec written as
		// `request.post("/api/dev/…")` arrives with no session at all. Half the
		// suite is written that way and the other half uses `page.request`,
		// which does share cookies. Refusing the first kind made those hooks
		// no-ops that failed silently three assertions later.
		//
		// This is safe only because these routes exist solely when
		// ENABLE_DEV_ENDPOINTS is set, and the fallback is a single hardcoded
		// fixture tenant — never a tenant id taken from the request.
		tenant, err := uuid.Parse(demoseed.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return store.Identity{}, false
		}
		user, err := uuid.Parse(demoseed.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return store.Identity{}, false
		}
		identity = store.Identity{TenantID: tenant, UserID: user}
	}
	if body != nil {
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
			return store.Identity{}, false
		}
	}
	return identity, true
}

// devExec runs one statement against the caller's tenant and reports whether it
// matched anything. A hook that silently matches zero rows is worse than an
// error: the spec proceeds against unchanged state and fails somewhere later,
// far from the cause.
func (s *Server) devExec(w http.ResponseWriter, r *http.Request, identity store.Identity,
	sql string, args ...any) bool {
	var affected int64
	err := store.WithTenant(r.Context(), s.DB, identity.TenantID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(r.Context(), sql, args...)
		affected = tag.RowsAffected()
		return execErr
	})
	if err != nil {
		s.Logger.Error("dev hook failed", "error", err, "sql", sql)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return false
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "not_found",
			"no row matched — the fixture this hook targets does not exist in this tenant")
		return false
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (s *Server) devAdvanceCampaign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	id, err := uuid.Parse(body.ID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid")
		return
	}
	// Ageing a campaign to sent is what the specs are after: they assert on
	// the finished state (final counts, no further sending) that a live campaign
	// only reaches after every recipient is done.
	//
	// Reaching that state also SETTLES the campaign's cost. A campaign that
	// finishes without charging anyone is not a finished campaign — the whole
	// point of the terminal state is that the money is now real. Skipping it
	// left the wallet untouched, so nothing downstream of a charge could ever
	// be observed: no ledger line, no balance movement, and no auto-recharge.
	//
	// Charged once. Advancing an already-sent campaign is a no-op rather than a
	// second debit, because the specs call this hook more than once and a
	// customer must never pay twice for one send.
	if err := s.devSettleCampaign(r, identity, id); err != nil {
		s.Logger.Warn("dev advance-campaign: settle failed", "campaign", id, "error", err)
	}
	s.devExec(w, r, identity,
		`UPDATE campaigns SET status = 'sent', updated_at = now()
		  WHERE id = $1 AND tenant_id = $2`, id, identity.TenantID)
}

// devSettleCampaign debits the campaign's cost, once.
func (s *Server) devSettleCampaign(r *http.Request, identity store.Identity, id uuid.UUID) error {
	ctx := r.Context()
	var name string
	var cost int64
	var currency, status string
	if err := s.AdminDB.QueryRow(ctx, `
		SELECT name, cost_minor_max, currency, status FROM campaigns
		WHERE id = $1 AND tenant_id = $2`, id, identity.TenantID,
	).Scan(&name, &cost, &currency, &status); err != nil {
		return err
	}
	if status == "sent" || cost <= 0 {
		return nil
	}
	campaignID := id
	_, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
		Currency: currency, Type: "charge", AmountMinor: cost,
		Description: name, CampaignID: &campaignID, CampaignName: &name,
	})
	return err
}

func (s *Server) devAdvanceSender(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country string `json:"country"`
		Header  string `json:"header"`
		To      string `json:"to"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	// Addressed by (country, header) rather than id: the specs create the sender
	// through the real API and never learn its generated id.
	s.devExec(w, r, identity,
		`UPDATE sender_ids SET status = $1,
		        rejection_reason = CASE WHEN $1 = 'rejected'
		                                THEN 'Header did not match the DLT registry.'
		                                ELSE NULL END
		  WHERE tenant_id = $2 AND country = $3 AND header = $4`,
		body.To, identity.TenantID, body.Country, body.Header)
}

func (s *Server) devAdvanceTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country string `json:"country"`
		Name    string `json:"name"`
		To      string `json:"to"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	s.devExec(w, r, identity,
		`UPDATE templates SET status = $1,
		        rejection_reason = CASE WHEN $1 = 'rejected'
		                                THEN 'Rejected by the regulator.' ELSE NULL END
		  WHERE tenant_id = $2 AND country = $3 AND name = $4`,
		body.To, identity.TenantID, body.Country, body.Name)
}

func (s *Server) devAdvanceRegistration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country   string `json:"country"`
		ObjectKey string `json:"objectKey"`
		To        string `json:"to"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	s.devExec(w, r, identity,
		`UPDATE registrations SET status = $1, updated_at = now(),
		        -- The exact wording a rejected registration shows the customer.
		        -- Copied from the frontend fixture rather than invented: the
		        -- remediation screen quotes it verbatim.
		        rejection_reason = CASE WHEN $1 = 'rejected'
		                                THEN 'Submitted details did not match the registry.'
		                                ELSE NULL END
		  WHERE tenant_id = $2 AND country = $3 AND object_key = $4`,
		body.To, identity.TenantID, body.Country, body.ObjectKey)
}

func (s *Server) devAdvanceEmailDNS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country    string `json:"country"`
		Header     string `json:"header"`
		RecordType string `json:"recordType"`
		To         string `json:"to"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	// Per record, not per sender: the screen shows SPF, DKIM and DMARC with
	// independent states, and the spec verifies exactly one of them at a time.
	s.devExec(w, r, identity,
		`UPDATE sender_dns_records AS d SET status = $1
		   FROM sender_ids AS s
		  WHERE d.sender_id = s.id AND d.tenant_id = $2
		    AND s.country = $3 AND s.header = $4 AND d.record_type = $5`,
		body.To, identity.TenantID, body.Country, body.Header, body.RecordType)
}

func (s *Server) devDrainWallet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Currency string `json:"currency"`
		ToMinor  int64  `json:"toMinor"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	// Only the balance moves. The ledger is append-only and a test hook has no
	// business writing history — an adjustment row here would be indistinguishable
	// from a real one in an audit, which is exactly what the append-only rule
	// exists to prevent.
	s.devExec(w, r, identity,
		`UPDATE wallet_balances SET balance_minor = $1
		  WHERE tenant_id = $2 AND currency = $3`,
		body.ToMinor, identity.TenantID, body.Currency)
}

func (s *Server) devSetMyRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	switch body.Role {
	case "owner", "admin", "member":
	default:
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"role must be owner, admin or member")
		return
	}
	// The RBAC specs sign in once and need to see the app as a lesser role. The
	// caller demotes only themselves — the user id comes from the session.
	s.devExec(w, r, identity,
		`UPDATE tenant_users SET role = $1 WHERE tenant_id = $2 AND user_id = $3`,
		body.Role, identity.TenantID, identity.UserID)
}

func (s *Server) devReceiveInbound(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ContactID string `json:"contactId"`
		Channel   string `json:"channel"`
		Body      string `json:"body"`
	}
	identity, ok := devRequest(w, r, &body)
	if !ok {
		return
	}
	contactID, err := uuid.Parse(body.ContactID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "contactId must be a uuid")
		return
	}
	message, err := store.ReceiveInboundMessage(r.Context(), s.DB, identity,
		contactID, body.Channel, body.Body)
	if err != nil {
		s.Logger.Error("dev inbound failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(message)
}

// devResetState restores the shared fixture account between specs.
//
// This is the hook the frontend calls as /api/dev/verify-reset, and against MSW
// it cleared in-memory singletons. Against a real database the equivalent
// problem is worse, because the damage persists: the auth specs CHANGE the
// fixture's password and the RBAC specs demote it to member, so a later spec
// signing in as the founder gets a 401 or a stripped-down UI and fails for a
// reason that has nothing to do with what it tests. A whole 45-minute run was
// lost to exactly that.
//
// It restores only the mutations the suite is known to make — password, role
// and email verification — rather than re-seeding wholesale, so data a spec
// legitimately created earlier in the run survives.
func (s *Server) devResetState(w http.ResponseWriter, r *http.Request) {
	// A full rebuild, not a targeted repair. The suite creates templates,
	// senders and campaigns as it runs and removes none of them, so the second
	// run of a spec collides with the first — "A template with that name
	// already exists" on a name the previous run took. Restoring only the
	// password and role fixed sign-in but left that collision in place.
	if s.AdminDB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"reset needs the admin database role; set DATABASE_ADMIN_URL")
		return
	}
	if err := demoseed.ApplyFixtureOnly(r.Context(), s.AdminDB); err != nil {
		s.Logger.Error("dev reset failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
