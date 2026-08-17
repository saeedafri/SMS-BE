package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saeedafri/sms-be/internal/api"
)

func TestHealthzReportsOK(t *testing.T) {
	router := api.NewRouter(&api.Server{})
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want %q", body.Status, "ok")
	}
}

// Every route in the contract must be registered. A path the spec does not
// define is a 404 — and that 404 must still use the Error envelope, because
// the frontend parses failures the same way regardless of status.
func TestUnknownPathReturnsContractErrorEnvelope(t *testing.T) {
	router := api.NewRouter(&api.Server{})
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/not-a-real-endpoint", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("error.code = %q, want %q", body.Error.Code, "not_found")
	}
}

// A spot-check across domains that the generated routes are actually wired.
// If HandlerFromMux were never called these would 404 instead of 501.
func TestContractRoutesAreRegistered(t *testing.T) {
	router := api.NewRouter(&api.Server{})
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/auth/login"},
		{http.MethodGet, "/v1/wallet/balances"},
		{http.MethodGet, "/v1/campaigns"},
		{http.MethodGet, "/v1/operator/tenants"},
		{http.MethodGet, "/v1/verify/services"},
		{http.MethodGet, "/v1/developer/api-keys"},
	}
	for _, route := range routes {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s returned 404; the contract route is not registered",
				route.method, route.path)
		}
	}
}
