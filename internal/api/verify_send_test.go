package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/verify"
)

func (h *harness) otpService(tenant account, sender string) string {
	h.t.Helper()
	created := h.do(http.MethodPost, "/v1/verify/services", tenant.Token, map[string]any{
		"name": "Login", "codeLength": 6, "codeTtlSeconds": 300, "maxAttempts": 3,
		"fallbackOrder": []string{}, "regionAllowlist": []string{},
		"channels": []map[string]any{{"channel": "SMS", "senderId": sender,
			"body": "{{code}} is your login code. Do not share it."}},
		"rateLimit": map[string]any{"maxPerPhone": 5, "windowSeconds": 600, "cooldownSeconds": 0},
	})
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create verify service = %d\n%s", created.Code, created.Body)
	}
	var service struct {
		ID string `json:"id"`
	}
	created.decode(h.t, &service)
	return service.ID
}

// A verification sends its code: charged, delivered through the ordinary send
// path under the customer's registered OTP template, checkable with the code
// that went to the handset, and written to no log.
func TestAVerificationSendsItsCodeAndNeverLogsIt(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.registeredTemplate(tenant, sender, "{{code}} is your login code. Do not share it.")
	h.fundWallet(tenant)
	service := h.otpService(tenant, sender)
	before := h.walletBalance(tenant)

	started := h.do(http.MethodPost, "/v1/verify/services/"+service+"/verifications",
		tenant.Token, map[string]any{"msisdn": "+919876543210"})
	if started.Code != http.StatusCreated {
		t.Fatalf("start verification = %d, want 201\n%s", started.Code, started.Body)
	}
	var verification struct {
		ID string `json:"id"`
	}
	started.decode(t, &verification)

	if h.walletBalance(tenant) >= before {
		t.Error("wallet unchanged — the code was never sent through the billed path")
	}
	if strings.Contains(h.logs.String(), verify.DevCode) {
		t.Error("the OTP code appears in the server log")
	}

	checked := h.do(http.MethodPost, "/v1/verify/services/"+service+"/verifications/"+
		verification.ID+"/check", tenant.Token, map[string]any{"code": verify.DevCode})
	var result struct {
		Status string `json:"status"`
	}
	checked.decode(t, &result)
	if result.Status != "verified" {
		t.Errorf("check with the sent code = %q, want verified\n%s", result.Status, checked.Body)
	}
}

// With no registered template matching the OTP copy, India refuses the send.
// The caller must hear that, and the challenge must not stay open for a code
// nobody received.
func TestAVerificationThatCannotBeSentIsRefusedAndDead(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	service := h.otpService(tenant, sender)

	started := h.do(http.MethodPost, "/v1/verify/services/"+service+"/verifications",
		tenant.Token, map[string]any{"msisdn": "+919876543210"})
	if started.Code != http.StatusUnprocessableEntity {
		t.Fatalf("start verification with no OTP template = %d, want 422\n%s",
			started.Code, started.Body)
	}
	if !strings.Contains(string(started.Body), "template") {
		t.Errorf("refusal does not name the missing template: %s", started.Body)
	}
}
