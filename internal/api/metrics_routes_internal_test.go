package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// /metrics is public and its route table is never pruned. A path that matched
// no route must not become a key of its own, or anyone can grow server memory
// without limit and have their own text echoed on that page.
func TestUnmatchedPathsShareOneMetricsKey(t *testing.T) {
	metrics := NewMetrics()
	router := chi.NewRouter()
	router.Use(requestLogger(slog.New(slog.NewTextHandler(io.Discard, nil)), metrics))
	router.Get("/v1/campaigns/{id}", func(http.ResponseWriter, *http.Request) {})

	for _, path := range []string{"/v1/campaigns/1", "/probe-a1", "/probe-b2", "/v1/nope/x"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	routes := metrics.Snapshot()["routes"].([]routeReport)
	counts := map[string]int64{}
	for _, route := range routes {
		counts[route.Route] = route.Count
	}
	want := map[string]int64{"GET unmatched": 3, "GET /v1/campaigns/{id}": 1}
	if len(counts) != len(want) || counts["GET unmatched"] != 3 || counts["GET /v1/campaigns/{id}"] != 1 {
		t.Fatalf("metrics routes = %v, want %v", counts, want)
	}
}

// With METRICS_TOKEN set, /metrics answers only its bearer. Behind nginx every
// request arrives from the proxy, so if the edge rule is ever lost this token is
// what still stands. A token of another length must be refused, not panic.
func TestMetricsTokenGuardsMetrics(t *testing.T) {
	guarded := &Server{Metrics: NewMetrics(), MetricsToken: "metrics-token-0123456789"}
	cases := []struct {
		name, header string
		want         int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer metrics-token-9876543210", http.StatusUnauthorized},
		{"shorter token", "Bearer metrics", http.StatusUnauthorized},
		{"longer token", "Bearer metrics-token-0123456789-and-more", http.StatusUnauthorized},
		{"right token", "Bearer metrics-token-0123456789", http.StatusOK},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		res := httptest.NewRecorder()
		guarded.metrics(res, req)
		if res.Code != c.want {
			t.Errorf("%s: /metrics = %d, want %d", c.name, res.Code, c.want)
		}
	}

	open := &Server{Metrics: NewMetrics()}
	res := httptest.NewRecorder()
	open.metrics(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if res.Code != http.StatusOK {
		t.Errorf("METRICS_TOKEN unset: /metrics = %d, want 200 as before", res.Code)
	}
}
