package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

var senderStatuses = []string{"approved", "blocked", "draft", "expired", "pending_review",
	"rejected", "submitted"}

// listOperatorTenantSenders returns every sender a tenant has registered, in
// the shape the tenant's own GET /v1/sender-ids uses, so the operator console's
// Sender filter can list senders that no campaign row mentions (registered but
// unused, journey-only, or older than the console's one page of campaigns).
//
// Every status is returned: an operator investigating a campaign may need a
// sender the tenant has since had rejected. Mounted directly like the other
// operator send routes; see docs/HANDOFF_TO_BACKEND_2026-09-30-operator-tenant-senders-and-exact-sender-filter.md.
func (s *Server) listOperatorTenantSenders(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	tenantID, valid := parsePathID(chi.URLParam(r, "id"))
	if !valid {
		writeError(w, http.StatusNotFound, codeNotFound, "No such tenant.")
		return
	}
	query := r.URL.Query()
	var filter store.CatalogueFilter
	var ok bool
	if filter.Status, ok = enumParam(w, query.Get("status"), "status", senderStatuses); !ok {
		return
	}
	if filter.Channel, ok = enumParam(w, strings.ToUpper(query.Get("channel")), "channel",
		channelIDs()); !ok {
		return
	}
	// A present but blank limit is refused, not defaulted: pageParams reads
	// "" as absent, which is right for the older routes and wrong for this one.
	if query.Has("limit") && strings.TrimSpace(query.Get("limit")) == "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, limitOutOfRange)
		return
	}
	if filter.Page, filter.Limit, ok = pageParams(w, query.Get("page"), query.Get("limit")); !ok {
		return
	}
	if filter.Limit == 0 {
		filter.Limit = 20
	}

	if _, err := store.GetTenant(r.Context(), s.operatorPool(), tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "No such tenant.")
			return
		}
		s.internalError(w, r, err)
		return
	}
	items, total, err := store.ListSenderIDs(r.Context(), s.DB,
		store.Identity{TenantID: tenantID}, filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]gen.SenderId, 0, len(items))
	for _, item := range items {
		out = append(out, senderResponse(item))
	}
	writeJSON(w, http.StatusOK, gen.SenderIdPage{SenderIds: out, Total: total})
}
