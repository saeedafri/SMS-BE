// Package mailer sends transactional email through Resend's HTTP API.
//
// Transactional only — verification links, password resets, team invitations.
// Campaign traffic does NOT go through here: campaigns are metered, priced and
// held against a wallet by the sending pipeline, and routing them through a
// path with no ledger entry would let a tenant send for free.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const sendEndpoint = "https://api.resend.com/emails"

// Mailer posts to Resend. A zero APIKey makes every Send a logged no-op, which
// is what local development and the test suite want: neither should be able to
// put real mail in a real inbox by accident, and neither should fail because
// they have no credentials.
type Mailer struct {
	APIKey string
	From   string
	Logger *slog.Logger

	// Client is optional; a sane one is used when nil. Injectable so tests can
	// assert on the request without reaching the network.
	Client *http.Client
}

func (m *Mailer) enabled() bool { return m != nil && m.APIKey != "" }

func (m *Mailer) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	// A timeout, always. An HTTP client with no deadline can hang a request
	// goroutine indefinitely when the provider stalls, and these calls sit
	// inline in sign-up and password-reset handlers.
	return &http.Client{Timeout: 10 * time.Second}
}

// Send delivers one message. It reports failure so callers can decide, but the
// callers here deliberately do not fail the request over it — see deliverToken.
func (m *Mailer) Send(ctx context.Context, to, subject, html string) error {
	if !m.enabled() {
		if m != nil && m.Logger != nil {
			m.Logger.Info("email not sent: no RESEND_API_KEY configured",
				"to", to, "subject", subject)
		}
		return nil
	}

	return m.sendTo(ctx, sendEndpoint, to, subject, html)
}

// sendTo is Send with the endpoint as a parameter, so tests can point it at an
// httptest server and assert on the actual request rather than mocking the
// transport.
func (m *Mailer) sendTo(ctx context.Context, endpoint, to, subject, html string) error {
	body, err := json.Marshal(map[string]any{
		"from": m.From, "to": []string{to}, "subject": subject, "html": html,
	})
	if err != nil {
		return fmt.Errorf("mailer: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mailer: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := m.client().Do(req)
	if err != nil {
		return fmt.Errorf("mailer: send: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		// Resend's own message, capped. A bare status code here ("email failed:
		// 422") hides the one useful detail — usually an unverified From domain
		// or a malformed address — and makes this undiagnosable from logs.
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("mailer: resend returned %d: %s", res.StatusCode, detail)
	}
	return nil
}
