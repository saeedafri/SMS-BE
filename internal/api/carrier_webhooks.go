package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// Carrier callbacks: delivery outcomes, template decisions and inbound
// messages, posted to us by Airtel or Vi.
//
// Deliberately NOT part of the generated contract. That contract describes what
// the frontend may call; these are inbound from a third party, their shapes
// belong to the vendors, and putting them in openapi.json would publish two
// carrier-shaped endpoints to a UI team that must never call either.
//
// Authentication is the weakest of the usual options because it is the only one
// available. Neither vendor signs its callbacks, and neither lets us attach a
// header to them — a webhook is configured as a URL and nothing else. So the
// shared secret travels in the path, which means it lands in access logs, and
// it is paired with an optional IP allowlist (Airtel documents IP whitelisting
// in both directions) and rotated by changing one environment variable.

// maxCarrierWebhookBody caps what we will read from a caller who is
// unauthenticated until the token in their path has been checked.
const maxCarrierWebhookBody = 1 << 20 // 1 MiB

func (s *Server) mountCarrierWebhookRoutes(r chi.Router) {
	r.Post("/v1/carrier-webhooks/rcs/{vendor}/{token}", s.receiveRCSWebhook)
}

func (s *Server) receiveRCSWebhook(w http.ResponseWriter, r *http.Request) {
	// 404, not 401 or 403. A wrong token should not confirm that the endpoint
	// exists, and neither should an address outside the allowlist.
	if !s.CarrierWebhookAllowlist.permits(clientIP(r)) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
		return
	}
	token := chi.URLParam(r, "token")
	if s.CarrierWebhookToken == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.CarrierWebhookToken)) != 1 {
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
		return
	}

	vendor := strings.ToLower(chi.URLParam(r, "vendor"))
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxCarrierWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidation, "unreadable body")
		return
	}

	var event connector.RCSEvent
	switch vendor {
	case "airtel":
		event, err = connector.ParseAirtelWebhook(payload)
	case "vi":
		event, err = connector.ParseViWebhook(payload)
	default:
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
		return
	}
	if err != nil {
		// A body we cannot parse is the one case worth refusing. Everything
		// else answers 200 so the carrier stops retrying: both vendors retry
		// on any non-200, and a payload we understand but choose not to act on
		// is not a failure they can fix by sending it again.
		s.Logger.Warn("carrier webhook could not be parsed",
			"vendor", vendor, "error", err)
		writeError(w, http.StatusBadRequest, codeValidation, "unparseable payload")
		return
	}

	s.applyRCSEvent(r, event)
	w.WriteHeader(http.StatusOK)
}

// applyRCSEvent does whatever the event means, and never fails the request.
//
// A carrier that gets a 500 retries, and retrying will not fix a message we do
// not hold or a template registered by some other system. Everything that goes
// wrong here is logged and answered 200; the alternative is a retry loop that
// runs until the vendor disables our endpoint.
func (s *Server) applyRCSEvent(r *http.Request, event connector.RCSEvent) {
	ctx := r.Context()
	log := s.Logger.With("vendor", event.Vendor, "carrier_event", event.Raw)

	switch event.Kind {
	case connector.RCSEventTemplate:
		if s.OperatorDB == nil || event.CarrierTemplateID == "" {
			log.Warn("template decision could not be applied",
				"template", event.CarrierTemplateID)
			return
		}
		// Applied on the operator pool because the payload carries the
		// carrier's template id and no tenant at all. The unique index on
		// (carrier_vendor, carrier_template_id) is what makes that safe.
		templateID, err := store.ApplyCarrierTemplateStatus(ctx, s.OperatorDB,
			event.Vendor, event.CarrierTemplateID, event.TemplateStatus, event.RejectionReason)
		if errors.Is(err, store.ErrNotFound) {
			log.Info("template decision for a template we do not hold",
				"carrier_template", event.CarrierTemplateID)
			return
		}
		if err != nil {
			log.Error("applying template decision", "error", err)
			return
		}
		log.Info("carrier template decision applied",
			"template", templateID, "status", event.TemplateStatus)

	case connector.RCSEventDelivery:
		service := s.sendingService(ctx)
		if service == nil {
			log.Error("delivery report dropped: no data plane")
			return
		}
		clickhouse, err := s.clickhouse(ctx)
		if err != nil {
			log.Error("delivery report dropped: message log unavailable")
			return
		}
		// Airtel's payload contains nothing we control, so the only way back to
		// a Relay message — and to the tenant whose wallet is holding money
		// against it — is the reference we stored at submit.
		tenantID, messageID, err := store.FindMessageByCarrierRef(ctx, clickhouse, event.CarrierRef)
		if err != nil {
			log.Info("delivery report for a message we did not send",
				"carrier_ref", event.CarrierRef)
			return
		}
		report := connector.DeliveryReport{
			CarrierRef: event.CarrierRef,
			MessageID:  messageID.String(),
			Delivered:  event.Delivered,
			ErrorCode:  event.ErrorCode,
			OccurredAt: event.OccurredAt,
		}
		// Replays are normal — carriers retry — and settle refuses to move a
		// terminal message, so this is idempotent without a dedupe table.
		if err := service.ApplyDeliveryReport(ctx,
			store.Identity{TenantID: tenantID}, report); err != nil {
			log.Error("applying delivery report", "message", messageID, "error", err)
			return
		}
		log.Info("carrier delivery report applied",
			"message", messageID, "delivered", event.Delivered)

	case connector.RCSEventInbound:
		// Attribution first, because on an inbound event the carrier's agent id
		// is the ONLY thing that names a tenant. A delivery report resolves
		// through our own carrier reference above — the message is ours and the
		// tenant comes with it — but an inbound message is a stranger writing
		// to a brand, and the brand is all we get.
		//
		// Unattributed is logged, not dropped. Every inbound event on this
		// deployment is unattributed today, because the shared agent has no
		// launch row at all: that is a true statement about the migration, and
		// silence would make it look like the carrier had stopped calling.
		tenantID, agentID, err := s.attributeToAgent(ctx, event)
		switch {
		case err != nil:
			log.Info("inbound RCS could not be attributed to a tenant",
				"carrier_agent", event.AgentID, "msisdn", event.Msisdn)
		default:
			log = log.With("tenant", tenantID, "agent", agentID)
		}

		// Not wired. Inbound RCS belongs in the inbox alongside SMS replies,
		// and threading a suggestion tap back to the campaign that offered it
		// needs the conversation model this send path does not touch. Logged
		// rather than dropped so the traffic is visible while that is built —
		// and so nobody concludes from silence that the carrier is not sending
		// them.
		log.Info("inbound RCS received but not yet threaded into the inbox",
			"msisdn", event.Msisdn, "postback", event.PostbackData,
			"context_ref", event.ContextRef)

	default:
		log.Debug("carrier event with no consequence")
	}
}

// attributeToAgent resolves a carrier event to the tenant that owns the agent
// it names.
//
// The carrier column is upper case because that is the vocabulary the routes
// table and the launch rows use — AIRTEL, VI — while a connector's vendor is
// lower case. The two meet here and nowhere else, which is why the conversion
// is at this seam rather than in the store.
//
// The unique index rcs_launch_carrier_identity on (carrier, carrier_agent_id)
// is what makes the answer single-valued. Without it two tenants could hold the
// same carrier agent id and each would receive the other's inbound messages —
// the worst failure available in this feature, and a silent one.
func (s *Server) attributeToAgent(ctx context.Context, event connector.RCSEvent) (
	uuid.UUID, uuid.UUID, error) {

	if s.OperatorDB == nil || event.AgentID == "" {
		return uuid.Nil, uuid.Nil, store.ErrNotFound
	}
	return store.TenantForCarrierAgent(ctx, s.OperatorDB,
		strings.ToUpper(event.Vendor), event.AgentID)
}
