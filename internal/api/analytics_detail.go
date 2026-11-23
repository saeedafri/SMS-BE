package api

import (
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/domain/failures"
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
		Layer   *string `json:"layer"`
		Title   string  `json:"title"`
	}
	type layerRow struct {
		Layer *string `json:"layer"`
		Count int     `json:"count"`
		Share float64 `json:"shareOfFailures"`
	}
	share := func(count int) float64 {
		if totals.Failed == 0 {
			return 0
		}
		return float64(count) / float64(totals.Failed)
	}
	// Each group is asked of the failure dictionary with its whole question
	// (how it failed, on which route and carrier), then the groups are folded
	// back to one row per channel and code, the most frequent answer naming it.
	rules := s.failureRules(r.Context())
	type key struct{ channel, code, class string }
	merged := map[key]*row{}
	var order []key
	layers := map[string]int{}
	for _, e := range rows {
		code := e.Code
		if code == "UNKNOWN" {
			code = ""
		}
		question, _ := failures.ContextFor(e.Channel, e.Status, code, e.RouteID, e.Carrier)
		answer := failures.Resolve(rules, question)
		layers[answer.Layer] += e.Count
		k := key{e.Channel, e.Code, e.Class}
		if existing, seen := merged[k]; seen {
			existing.Count += e.Count
			continue
		}
		entry := &row{Channel: e.Channel, Code: e.Code, Class: e.Class, Count: e.Count,
			Title: answer.Title}
		if !answer.Unmapped() {
			layer := answer.Layer
			entry.Layer = &layer
		}
		merged[k] = entry
		order = append(order, k)
	}
	out := make([]row, 0, len(order))
	for _, k := range order {
		entry := *merged[k]
		entry.Share = share(entry.Count)
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 100 {
		out = out[:100]
	}
	byLayer := make([]layerRow, 0, len(layers))
	for layer, count := range layers {
		group := layerRow{Count: count, Share: share(count)}
		if layer != "" {
			name := layer
			group.Layer = &name
		}
		byLayer = append(byLayer, group)
	}
	sort.Slice(byLayer, func(i, j int) bool { return byLayer[i].Count > byLayer[j].Count })
	failureRate := 0.0
	if totals.Messages > 0 {
		failureRate = float64(totals.Failed) / float64(totals.Messages)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": totals.Messages,
		"failed": totals.Failed, "failureRate": failureRate, "errors": out, "byLayer": byLayer})
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
