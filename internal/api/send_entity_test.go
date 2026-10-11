package api_test

import (
	"context"
	"net/http"
	"testing"
)

// The dashboard refuses to start a campaign until the tenant's India entity is
// fully registered — principal entity AND telemarketer mapping. The API sent
// anyway: a tenant with its TM mapping still in review was blocked on screen
// and sent RCS over HTTP the same afternoon. One rule, both doors.
func TestASendFromAnUnregisteredEntityIsRefusedUntilItIsApproved(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	var sender string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'NOENTY', 'SMS', 'IN', 'approved') RETURNING id`,
		acct.TenantID).Scan(&sender); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	template := h.wildcardTemplate(acct, sender)
	h.fundWallet(acct)
	// Half registered: the principal entity is approved, the TM mapping is not.
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO registrations (tenant_id, country, object_key, status) VALUES
		  ($1, 'IN', 'pe_rtm_entity', 'approved'),
		  ($1, 'IN', 'tm_mapping', 'pending_review')`, acct.TenantID); err != nil {
		t.Fatalf("seed registrations: %v", err)
	}

	send := func() (string, string, int) {
		res := h.do(http.MethodPost, "/v1/messages", acct.Token, map[string]any{
			"senderId": sender, "templateId": template,
			"to": "+919820000003", "body": "Your order has shipped.",
		})
		if res.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 with a verdict\n%s", res.Code, res.Body)
		}
		var verdict struct {
			Status    string  `json:"status"`
			ErrorCode *string `json:"errorCode"`
			CostMinor int     `json:"costMinor"`
		}
		res.decode(t, &verdict)
		code := ""
		if verdict.ErrorCode != nil {
			code = *verdict.ErrorCode
		}
		return verdict.Status, code, verdict.CostMinor
	}

	status, code, cost := send()
	if status != "rejected" || code != "entity_not_registered" {
		t.Fatalf("half-registered entity: status %q code %q, want rejected/entity_not_registered",
			status, code)
	}
	if cost != 0 {
		t.Errorf("a refused send was charged %d minor units", cost)
	}

	if _, err := h.admin.Exec(context.Background(), `
		UPDATE registrations SET status = 'approved'
		 WHERE tenant_id = $1 AND object_key = 'tm_mapping'`, acct.TenantID); err != nil {
		t.Fatalf("approve tm mapping: %v", err)
	}

	if status, code, _ := send(); status == "rejected" {
		t.Fatalf("a fully registered entity was refused: code %q", code)
	}
}
