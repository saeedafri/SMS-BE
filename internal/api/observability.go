package api

import (
	"context"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// slowRequestThreshold is when a request stops being normal and becomes worth
// investigating. Measured p95 across every endpoint is under 12 ms, so 250 ms
// is far enough above the noise that a hit is genuinely interesting rather
// than routine.
const slowRequestThreshold = 250 * time.Millisecond

// Metrics is the live picture of what this process is doing.
//
// It is deliberately in-process and lock-light rather than a Prometheus
// dependency: the whole point is that it keeps working when other things are
// failing, and an observability system that needs a healthy network to tell
// you the network is unhealthy is not much use during an incident.
type Metrics struct {
	StartedAt time.Time

	requests  atomic.Int64
	errors4xx atomic.Int64
	errors5xx atomic.Int64
	slow      atomic.Int64

	// Per-endpoint detail. A map guarded by a mutex is correct here — it is
	// written once per request, which is nothing next to the database work
	// the same request already did.
	mu       sync.Mutex
	byRoute  map[string]*routeStat
	recent   []Incident
	maxEvent int
}

type routeStat struct {
	Count      int64
	TotalMs    float64
	MaxMs      float64
	Errors     int64
	LastCalled time.Time
}

// Incident is something that deserves a human's attention: a 5xx, or a request
// slow enough to be abnormal. Kept in a ring buffer so the endpoint always
// answers instantly and never grows without bound.
type Incident struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Duration float64   `json:"durationMs"`
	Request  string    `json:"requestId"`
	Detail   string    `json:"detail,omitempty"`
}

func NewMetrics() *Metrics {
	return &Metrics{
		StartedAt: time.Now(),
		byRoute:   map[string]*routeStat{},
		maxEvent:  100,
	}
}

// Record folds one completed request into the picture.
func (m *Metrics) Record(method, path string, status int, duration time.Duration, requestID, detail string) {
	if m == nil {
		return
	}
	m.requests.Add(1)
	switch {
	case status >= 500:
		m.errors5xx.Add(1)
	case status >= 400:
		m.errors4xx.Add(1)
	}

	ms := float64(duration.Microseconds()) / 1000
	slow := duration >= slowRequestThreshold
	if slow {
		m.slow.Add(1)
	}

	key := method + " " + path
	m.mu.Lock()
	stat, seen := m.byRoute[key]
	if !seen {
		stat = &routeStat{}
		m.byRoute[key] = stat
	}
	stat.Count++
	stat.TotalMs += ms
	stat.LastCalled = time.Now()
	if ms > stat.MaxMs {
		stat.MaxMs = ms
	}
	if status >= 400 {
		stat.Errors++
	}

	// Only 5xx and slow requests become incidents. A 404 or a 422 is the API
	// working correctly — recording those would bury the real problems.
	if status >= 500 || slow {
		kind := "slow"
		if status >= 500 {
			kind = "error"
		}
		m.recent = append(m.recent, Incident{
			At: time.Now().UTC(), Kind: kind, Method: method, Path: path,
			Status: status, Duration: ms, Request: requestID, Detail: detail,
		})
		if len(m.recent) > m.maxEvent {
			m.recent = m.recent[len(m.recent)-m.maxEvent:]
		}
	}
	m.mu.Unlock()
}

// RecordIncident logs something that is not tied to a request — a failed
// background sweep, a carrier submission error.
func (m *Metrics) RecordIncident(kind, detail string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.recent = append(m.recent, Incident{
		At: time.Now().UTC(), Kind: kind, Detail: detail,
	})
	if len(m.recent) > m.maxEvent {
		m.recent = m.recent[len(m.recent)-m.maxEvent:]
	}
	m.mu.Unlock()
}

type routeReport struct {
	Route     string  `json:"route"`
	Count     int64   `json:"count"`
	AvgMs     float64 `json:"avgMs"`
	MaxMs     float64 `json:"maxMs"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
}

// Snapshot renders the current picture. Routes are sorted by total time spent
// rather than by call count, because the endpoint costing the most wall-clock
// is the one worth looking at first.
func (m *Metrics) Snapshot() map[string]any {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	m.mu.Lock()
	routes := make([]routeReport, 0, len(m.byRoute))
	for key, stat := range m.byRoute {
		report := routeReport{
			Route: key, Count: stat.Count, MaxMs: stat.MaxMs, Errors: stat.Errors,
		}
		if stat.Count > 0 {
			report.AvgMs = stat.TotalMs / float64(stat.Count)
			report.ErrorRate = float64(stat.Errors) / float64(stat.Count)
		}
		routes = append(routes, report)
	}
	incidents := make([]Incident, len(m.recent))
	copy(incidents, m.recent)
	m.mu.Unlock()

	sort.Slice(routes, func(i, j int) bool {
		return routes[i].AvgMs*float64(routes[i].Count) > routes[j].AvgMs*float64(routes[j].Count)
	})
	// Newest incident first — during an incident nobody scrolls to the bottom.
	for i, j := 0, len(incidents)-1; i < j; i, j = i+1, j-1 {
		incidents[i], incidents[j] = incidents[j], incidents[i]
	}

	requests := m.requests.Load()
	errors5xx := m.errors5xx.Load()
	errorRate := 0.0
	if requests > 0 {
		errorRate = float64(errors5xx) / float64(requests)
	}

	return map[string]any{
		"uptimeSeconds": int(time.Since(m.StartedAt).Seconds()),
		"requests": map[string]any{
			"total":     requests,
			"4xx":       m.errors4xx.Load(),
			"5xx":       errors5xx,
			"slow":      m.slow.Load(),
			"errorRate": errorRate,
		},
		"runtime": map[string]any{
			"goroutines": runtime.NumGoroutine(),
			"heapMB":     float64(mem.HeapAlloc) / 1024 / 1024,
			"sysMB":      float64(mem.Sys) / 1024 / 1024,
			"gcRuns":     mem.NumGC,
			"cpuCount":   runtime.NumCPU(),
		},
		"routes":    routes,
		"incidents": incidents,
	}
}

// metrics serves the live picture. It requires no authentication because it
// exposes no tenant data — only counters, timings and route names — and an
// observability endpoint that needs a working login is useless exactly when
// login is what has broken.
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	if s.Metrics == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, s.Metrics.Snapshot())
}

// readiness reports whether this process should receive traffic, as opposed to
// whether it is alive. A process can be alive and not ready — still opening
// pools, or with a dependency it cannot serve without.
func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	ready := true
	checks := map[string]string{}

	if s.DB == nil {
		ready, checks["postgres"] = false, "absent"
	} else if err := s.DB.Ping(ctx); err != nil {
		ready, checks["postgres"] = false, "down"
	} else {
		checks["postgres"] = "up"
	}

	status := http.StatusOK
	state := "ready"
	if !ready {
		status, state = http.StatusServiceUnavailable, "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": checks})
}
