package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/mailer"
)

type acceptAll struct{}

func (acceptAll) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"x"}`)),
		Header: http.Header{}}, nil
}

// With real mail configured, a verification or reset token never reaches the
// log: logs are shipped off the box, and the token is an account takeover.
func TestAnIssuedTokenIsNotLoggedWhenMailIsReallySent(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	s := &Server{Logger: logger, Mail: &mailer.Mailer{APIKey: "re_test", From: "Relay <r@x.test>",
		Logger: logger, Client: &http.Client{Transport: acceptAll{}}}}

	s.deliverToken("password_reset", "user@x.test", "SECRET-RESET-TOKEN")
	if strings.Contains(logs.String(), "SECRET-RESET-TOKEN") {
		t.Fatalf("reset token written to the log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "account token issued") {
		t.Errorf("issuing a token is no longer logged at all:\n%s", logs.String())
	}
}
