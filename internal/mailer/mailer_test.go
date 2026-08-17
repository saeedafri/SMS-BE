package mailer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A mailer with no API key must not reach the network. This is the property
// that keeps the test suite and local development from putting real mail in
// real inboxes, so it is worth asserting rather than assuming.
func TestSendWithoutAPIKeyDoesNotCallResend(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	m := &Mailer{Client: server.Client()}
	if err := m.Send(context.Background(), "a@b.test", "s", "<p>x</p>"); err != nil {
		t.Fatalf("unconfigured Send should be a no-op, got %v", err)
	}
	if called {
		t.Fatal("unconfigured mailer sent an HTTP request")
	}
	// A nil receiver must be safe too: Server.Mail is optional.
	var nilMailer *Mailer
	if err := nilMailer.Send(context.Background(), "a@b.test", "s", "x"); err != nil {
		t.Fatalf("nil mailer should be a no-op, got %v", err)
	}
}

func TestSendPostsAuthorisedJSON(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer server.Close()

	m := &Mailer{APIKey: "re_test", From: "Relay <n@e.test>", Client: server.Client()}
	if err := m.sendTo(context.Background(), server.URL,
		"to@e.test", "Subject", "<p>hi</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "Bearer re_test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	var payload struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body was not JSON: %v", err)
	}
	if payload.From != "Relay <n@e.test>" || len(payload.To) != 1 || payload.To[0] != "to@e.test" {
		t.Errorf("unexpected envelope: %+v", payload)
	}
	if payload.Subject != "Subject" || payload.HTML != "<p>hi</p>" {
		t.Errorf("unexpected content: %+v", payload)
	}
}

// A rejection must surface Resend's own message. A bare status code hides the
// usual cause — an unverified From domain — and makes this undiagnosable.
func TestSendSurfacesProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"The from address domain is not verified"}`))
	}))
	defer server.Close()

	m := &Mailer{APIKey: "re_test", From: "x@unverified.test", Client: server.Client()}
	err := m.sendTo(context.Background(), server.URL, "to@e.test", "s", "x")
	if err == nil {
		t.Fatal("expected an error for a 422")
	}
	if !strings.Contains(err.Error(), "not verified") {
		t.Errorf("provider detail was dropped: %v", err)
	}
}
