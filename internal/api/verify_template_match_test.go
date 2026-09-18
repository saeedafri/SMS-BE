package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

type verifyChannelState struct {
	Channel          string `json:"channel"`
	SenderID         string `json:"senderId"`
	TemplateRequired *bool  `json:"templateRequired"`
	MatchedTemplate  *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"matchedTemplate"`
}

type verifyServiceRead struct {
	ID       string               `json:"id"`
	Status   string               `json:"status"`
	Channels []verifyChannelState `json:"channels"`
}

func (h *harness) otpServiceWith(tenant account, sender, copy string) verifyServiceRead {
	h.t.Helper()
	created := h.do(http.MethodPost, "/v1/verify/services", tenant.Token, map[string]any{
		"name": "Login " + uuid.NewString()[:6], "codeLength": 6, "codeTtlSeconds": 300, "maxAttempts": 3,
		"fallbackOrder": []string{}, "regionAllowlist": []string{},
		"channels":  []map[string]any{{"channel": "SMS", "senderId": sender, "body": copy}},
		"rateLimit": map[string]any{"maxPerPhone": 50, "windowSeconds": 600, "cooldownSeconds": 0},
	})
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create verify service = %d\n%s", created.Code, created.Body)
	}
	var service verifyServiceRead
	created.decode(h.t, &service)
	var read verifyServiceRead
	h.do(http.MethodGet, "/v1/verify/services/"+service.ID, tenant.Token, nil).decode(h.t, &read)
	return read
}

func (h *harness) usSender(tenant account) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, '+15550100', 'SMS', 'US', 'approved') RETURNING id`, tenant.TenantID).Scan(&id); err != nil {
		h.t.Fatalf("seed US sender: %v", err)
	}
	return id
}

func (h *harness) startVerification(tenant account, service string) response {
	h.t.Helper()
	return h.do(http.MethodPost, "/v1/verify/services/"+service+"/verifications",
		tenant.Token, map[string]any{"msisdn": "+919876543210"})
}

// lastMessageTemplate is the template the send path attached to the tenant's
// most recent message.
func (h *harness) lastMessageTemplate(tenant account) string {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	var template *uuid.UUID
	if err := conn.QueryRow(context.Background(), `
		SELECT template_id FROM messages FINAL WHERE tenant_id = ?
		ORDER BY created_at DESC LIMIT 1`, tenant.TenantID).Scan(&template); err != nil {
		return ""
	}
	if template == nil {
		return ""
	}
	return template.String()
}

func requireState(t *testing.T, service verifyServiceRead, required bool, matched string, status string) {
	t.Helper()
	if len(service.Channels) != 1 {
		t.Fatalf("%d channels, want 1", len(service.Channels))
	}
	ch := service.Channels[0]
	if ch.TemplateRequired == nil || *ch.TemplateRequired != required {
		t.Errorf("templateRequired = %v, want %t", ch.TemplateRequired, required)
	}
	got := ""
	if ch.MatchedTemplate != nil {
		got = ch.MatchedTemplate.ID
	}
	if got != matched {
		t.Errorf("matchedTemplate = %q, want %q", got, matched)
	}
	if service.Status != status {
		t.Errorf("status = %q, want %q", service.Status, status)
	}
}

const acmeCopy = "{{code}} is your Acme login code. Valid 5 min."

// Ask 40. India, and the sender's only approved template is unrelated.
func TestAServiceWhoseCopyMatchesNothingIsNotLive(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.registeredTemplate(tenant, sender, "Your order {{order}} has shipped.")
	requireState(t, h.otpServiceWith(tenant, sender, acmeCopy), true, "", "setup_needed")
}

func TestAServiceWhoseCopyMatchesIsLive(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender, acmeCopy)
	service := h.otpServiceWith(tenant, sender, acmeCopy)
	requireState(t, service, true, template, "live")
	if name := service.Channels[0].MatchedTemplate; name == nil || name.Name == "" {
		t.Error("matchedTemplate carries no name")
	}
}

// Ask 40. The match reads every approved template, not a first page of 200.
// The 249 fillers are newer than the match, so in newest-first order the match
// is the 250th template: past any first page.
func TestTheMatchScansEveryApprovedTemplate(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender, acmeCopy)
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status,
		    external_id, dlt_category, created_at)
		SELECT $1, $2, 'Filler ' || n, 'SMS', 'IN', 'Filler message number ' || n, 'approved',
		       '12070195011234567890', 'TRANSACTIONAL', now() + (n || ' seconds')::interval
		  FROM generate_series(1, 249) AS n`, tenant.TenantID, sender); err != nil {
		t.Fatalf("seed filler templates: %v", err)
	}
	h.fundWallet(tenant)
	service := h.otpServiceWith(tenant, sender, acmeCopy)
	requireState(t, service, true, template, "live")

	if started := h.startVerification(tenant, service.ID); started.Code != http.StatusCreated {
		t.Fatalf("verification with the match as the 250th template = %d, want 201\n%s", started.Code, started.Body)
	}
}

func TestAnotherSendersTemplateIsNotAMatch(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	var other string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'OTHERS', 'SMS', 'IN', 'approved') RETURNING id`, tenant.TenantID).Scan(&other); err != nil {
		t.Fatalf("seed other sender: %v", err)
	}
	h.registeredTemplate(tenant, other, acmeCopy)
	requireState(t, h.otpServiceWith(tenant, sender, acmeCopy), true, "", "setup_needed")
}

// Ask 40. The match is anchored at both ends: copy that adds text after the
// template's last fixed segment is a different message.
//
// The fixture has a fixed tail. With a template ENDING in its variable, the
// variable absorbs any trailing text — MatchesTemplate and the frontend's port
// of it both say so (template-match.test.ts: "a trailing variable swallows the
// rest") — so "Your code is {{code}}" against "... click here" is a match on the
// send path, and reporting otherwise would disagree with it.
func TestAMatchIsAnchored(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.registeredTemplate(tenant, sender, "Your code is {{code}}. Valid 5 min.")
	requireState(t, h.otpServiceWith(tenant, sender, "Your code is {{code}}. Valid 5 min. click here"),
		true, "", "setup_needed")
}

// Ask 40. A fixed digit beside the variable matches one code in ten. Two
// renderings that share no digit must both match.
func TestADigitBesideTheCodeIsNotAMatch(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.registeredTemplate(tenant, sender, "Your code: 1{{code}}")
	requireState(t, h.otpServiceWith(tenant, sender, "Your code: {{code}}"), true, "", "setup_needed")
}

func TestAUSChannelRequiresNoTemplate(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	requireState(t, h.otpServiceWith(tenant, h.usSender(tenant), acmeCopy), false, "", "live")
}

// Ask 40, a regression guard: a US sender needs no template, so a verification
// without one still sends once the send path decides through matchOTPTemplate.
func TestAUSVerificationWithNoTemplateStillSends(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.appendTopup(tenant, "USD", 1_000_000)
	service := h.otpServiceWith(tenant, h.usSender(tenant), acmeCopy)
	started := h.startVerification(tenant, service.ID)
	if started.Code != http.StatusCreated {
		t.Fatalf("US verification with no template = %d, want 201\n%s", started.Code, started.Body)
	}
}

// Ask 40. The 422 says why as data, and the verification is dead.
func TestAnUnsendableVerificationSaysWhyAsData(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	service := h.otpServiceWith(tenant, sender, acmeCopy)
	started := h.startVerification(tenant, service.ID)
	var body struct {
		Error struct {
			Code   string  `json:"code"`
			Reason *string `json:"reason"`
		} `json:"error"`
	}
	started.decode(t, &body)
	if started.Code != http.StatusUnprocessableEntity || body.Error.Code != "verification_not_sent" ||
		body.Error.Reason == nil || *body.Error.Reason != "registered_template_required" {
		t.Fatalf("= %d %s, want 422 verification_not_sent with reason registered_template_required",
			started.Code, started.Body)
	}
	var status string
	if err := h.admin.QueryRow(context.Background(), `
		SELECT status FROM verifications WHERE service_id = $1 ORDER BY created_at DESC LIMIT 1`,
		service.ID).Scan(&status); err != nil || status != "expired" {
		t.Errorf("verification status = %q (%v), want expired", status, err)
	}
}

// Ask 40. The service's matchedTemplate is the template the send path uses.
func TestTheServiceAndTheSendAgree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		template string // registered on the sender; empty for none
		copy     string
	}{
		{"match", acmeCopy, acmeCopy},
		{"unrelated", "Your order {{order}} has shipped.", acmeCopy},
		{"anchored", "Your code is {{code}}. Valid 5 min.", "Your code is {{code}}. Valid 5 min. click here"},
		{"digit beside", "Your code: 1{{code}}", "Your code: {{code}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSendHarness(t)
			tenant := h.newAccount("owner")
			sender := h.approvedSender(tenant)
			h.registeredTemplate(tenant, sender, tc.template)
			h.fundWallet(tenant)
			service := h.otpServiceWith(tenant, sender, tc.copy)
			matched := ""
			if len(service.Channels) == 1 && service.Channels[0].MatchedTemplate != nil {
				matched = service.Channels[0].MatchedTemplate.ID
			}
			h.startVerification(tenant, service.ID)
			if used := h.lastMessageTemplate(tenant); used != matched {
				t.Errorf("service says %q, the send used %q", matched, used)
			}
			if tc.name == "match" && matched == "" {
				t.Error(fmt.Sprintf("%s: no match reported", tc.name))
			}
		})
	}
}
