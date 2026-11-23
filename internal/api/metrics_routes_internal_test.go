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
