package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/store"
)

// Error and latency stats: the two pages Sigmo has under Reports that the
// analytics summary only hints at. Mounted directly, outside the contract, until
// the frontend adds them.
func (s *Server) mountAnalyticsDetailRoutes(r chi.Router) {
	r.Get("/v1/analytics/errors", s.errorStats)
	r.Get("/v1/analytics/latency", s.latencyStats)
}

var detailRanges = map[string]time.Duration{
	"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
}

func (s *Server) detailFilter(w http.ResponseWriter, r *http.Request) (store.DetailFilter, bool) {
	query := r.URL.Query()
	window := query.Get("range")
	if window == "" {
		window = "7d"
	}
	span, ok := detailRanges[window]
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"range must be one of: 24h, 7d, 30d, 90d.")
		return store.DetailFilter{}, false
	}
	filter := store.DetailFilter{Since: time.Now().UTC().Add(-span).Truncate(time.Minute)}
	if channel := query.Get("channel"); channel != "" {
		if !slices.Contains(validChannels, channel) {
			writeError(w, http.StatusUnprocessableEntity, codeValidation,
				"channel must be one of: SMS, RCS, WHATSAPP, EMAIL, VOICE.")
			return filter, false
		}
		filter.Channel = channel
	}
	if country := query.Get("country"); country != "" {
		if !slices.Contains(validCountries, country) {
			writeError(w, http.StatusUnprocessableEntity, codeValidation,
				"country must be one of: IN, US, GB, AE.")
			return filter, false
		}
		filter.Country = country
	}
	return filter, true
}

func (s *Server) errorStats(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	filter, ok := s.detailFilter(w, r)
	if !ok {
		return
	}
	conn, err := s.clickhouse(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "message data is unavailable")
		return
	}
	totals, rows, err := store.QueryErrorBreakdown(r.Context(), conn, identity.TenantID, filter)
	if err != nil {
		s.Logger.Error("error stats", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	type row struct {
		Channel string  `json:"channel"`
		Code    string  `json:"code"`
		Class   string  `json:"class"`
		Count   int     `json:"count"`
		Share   float64 `json:"shareOfFailures"`
	}
	out := make([]row, len(rows))
	for i, e := range rows {
		share := 0.0
		if totals.Failed > 0 {
			share = float64(e.Count) / float64(totals.Failed)
		}
		out[i] = row{e.Channel, e.Code, e.Class, e.Count, share}
	}
	failureRate := 0.0
	if totals.Messages > 0 {
		failureRate = float64(totals.Failed) / float64(totals.Messages)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": totals.Messages,
		"failed": totals.Failed, "failureRate": failureRate, "errors": out})
}

func (s *Server) latencyStats(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	filter, ok := s.detailFilter(w, r)
	if !ok {
		return
	}
	conn, err := s.clickhouse(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "message data is unavailable")
		return
	}
	rows, buckets, err := store.QueryLatency(r.Context(), conn, identity.TenantID, filter)
	if err != nil {
		s.Logger.Error("latency stats", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	type row struct {
		Channel   string `json:"channel"`
		Carrier   string `json:"carrier"`
		Delivered int    `json:"delivered"`
		P50Ms     int    `json:"p50Ms"`
		P90Ms     int    `json:"p90Ms"`
		P99Ms     int    `json:"p99Ms"`
		AvgMs     int    `json:"avgMs"`
	}
	out := make([]row, len(rows))
	delivered := 0
	for i, l := range rows {
		out[i] = row{l.Channel, l.Carrier, l.Delivered, l.P50Ms, l.P90Ms, l.P99Ms, l.AvgMs}
		delivered += l.Delivered
	}
	hist := make([]map[string]any, len(buckets))
	for i, b := range buckets {
		hist[i] = map[string]any{"label": b.Label, "count": b.Count}
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": delivered,
		"byCarrier": out, "histogram": hist})
}
