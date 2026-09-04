package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// A send-only API is not a programmatic API. The first thing any integration
// does after sending is ask what happened — poll a status, reconcile a day's
// sends, pull delivery rates — and none of it was possible: every read answered
// 401 to the same key that had just been charged for a send a second earlier.

func TestAnApiKeyCanReadMessagesWithTheReadScope(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms", "read:messages"})

	sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876543210", "body": "Readable afterwards?",
	})
	if sent.Code != http.StatusAccepted {
		t.Fatalf("send = %d\n%s", sent.Code, sent.Body)
	}
	// A refusal is also a 202, so the status has to be read: without this the
	// test passes on a refused message, which is still recorded and would still
	// be found by the read below.
	var result struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
	}
	sent.decode(t, &result)
	if result.Status != "sent" && result.Status != "queued" {
		t.Fatalf("send status = %q (%v), want a real send", result.Status, result.ErrorCode)
	}

	read := h.do(http.MethodGet, "/v1/messages?limit=10", secret, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read with the same key = %d, want 200\n%s", read.Code, read.Body)
	}
	var page struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	read.decode(t, &page)
	if len(page.Messages) == 0 {
		t.Fatal("the key read its own tenant's log and found nothing in it")
	}
}

// Validation at creation is cosmetic on its own. This is the half that matters:
// the scope has to be refused at call time, and with 403 rather than 401 —
// the credential is real, the permission is not, and a client that retries a
// 401 by re-authenticating loops forever on a 403 wearing the wrong number.
func TestAKeyWithoutTheReadScopeIsForbiddenNotUnauthenticated(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	secret := h.apiKey(tenant, []string{"send:sms"})

	read := h.do(http.MethodGet, "/v1/messages?limit=1", secret, nil)
	if read.Code != http.StatusForbidden {
		t.Fatalf("read with a send-only key = %d, want 403\n%s", read.Code, read.Body)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	read.decode(t, &body)
	if body.Error.Code != "forbidden" {
		t.Fatalf("error code = %q, want forbidden", body.Error.Code)
	}
}

// The escalation the frontend asked us to check on our side: a customer issues
// a deliberately read-only key to a third party. That key must not be able to
// spend their wallet.
func TestAReadOnlyKeyCannotSpendTheWallet(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"read:messages"})

	sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876543210", "body": "Should never leave",
	})
	if sent.Code != http.StatusForbidden {
		t.Fatalf("send with a read-only key = %d, want 403 — a read-only key that "+
			"can send is a customer's wallet handed to whoever holds it\n%s",
			sent.Code, sent.Body)
	}
}

// send:sms and send:rcs are separate scopes for a reason.
func TestTheSmsScopeDoesNotAuthoriseAnRcsSend(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	rcsSender := h.approvedSenderOn(tenant, "RCS")
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})

	sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": rcsSender, "to": "9876543210", "body": "Over RCS",
	})
	if sent.Code != http.StatusForbidden {
		t.Fatalf("RCS send with an SMS-only key = %d, want 403\n%s", sent.Code, sent.Body)
	}
}

// Widening key auth must not have widened it to everything. The team roster,
// billing history and tenant settings are session-only and stay that way.
func TestAKeyIsStillNotACredentialOnSessionOnlyRoutes(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	secret := h.apiKey(tenant, []string{"send:sms", "read:messages",
		"read:logs", "read:analytics", "webhooks:manage"})

	// Every scope there is, and still no reach into any of these.
	for _, path := range []string{"/v1/team", "/v1/billing/invoices", "/v1/me",
		"/v1/wallet/ledger", "/v1/sender-ids", "/v1/suppressions"} {
		res := h.do(http.MethodGet, path, secret, nil)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a fully-scoped API key = %d, want 401\n%s",
				path, res.Code, res.Body)
		}
	}
}

// A key is tenant-scoped exactly as a session is.
func TestAKeyCannotReadAnotherTenantsMessages(t *testing.T) {
	h := newSendHarness(t)
	mine := h.newAccount("owner")
	theirs := h.newAccount("owner")

	sender := h.approvedSender(theirs)
	template := h.wildcardTemplate(theirs, sender)
	h.fundWallet(theirs)
	theirKey := h.apiKey(theirs, []string{"send:sms", "read:messages"})
	sent := h.do(http.MethodPost, "/v1/messages", theirKey, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876543210", "body": "Their message",
	})
	if sent.Code != http.StatusAccepted {
		t.Fatalf("their send = %d\n%s", sent.Code, sent.Body)
	}

	myKey := h.apiKey(mine, []string{"read:messages"})
	read := h.do(http.MethodGet, "/v1/messages?limit=50", myKey, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("my read = %d\n%s", read.Code, read.Body)
	}
	var page struct {
		Messages []map[string]any `json:"messages"`
		Total    int              `json:"total"`
	}
	read.decode(t, &page)
	if len(page.Messages) != 0 {
		t.Fatalf("a key read %d messages belonging to another tenant", len(page.Messages))
	}
}

// The catalogue and the validation must be one list. They were not, and the
// service accepted "messages:write" — a scope that appears nowhere in the six
// it publishes two paths away — stored it verbatim and echoed it back forever.
func TestAScopeOutsideTheCatalogueIsRefused(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	created := h.do(http.MethodPost, "/v1/developer/api-keys", acct.Token,
		map[string]any{"name": "scope probe", "environment": "test",
			"scopes": []string{"messages:write"}})
	if created.Code != http.StatusUnprocessableEntity {
		t.Fatalf("creating a key with an invented scope = %d, want 422\n%s",
			created.Code, created.Body)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	created.decode(t, &body)
	// The message must name the offender and list the vocabulary, the same
	// shape the channel enum uses.
	for _, want := range []string{"messages:write", "send:sms", "webhooks:manage"} {
		if !strings.Contains(body.Error.Message, want) {
			t.Fatalf("error message %q does not mention %q", body.Error.Message, want)
		}
	}
}

func TestEveryPublishedScopeIsAcceptedOnCreation(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	catalogue := h.do(http.MethodGet, "/v1/developer/scopes", acct.Token, nil)
	if catalogue.Code != http.StatusOK {
		t.Fatalf("scopes = %d\n%s", catalogue.Code, catalogue.Body)
	}
	var scopes []struct {
		Key string `json:"key"`
	}
	catalogue.decode(t, &scopes)
	if len(scopes) == 0 {
		t.Fatal("the catalogue is empty")
	}

	// Validation that refuses something the same service publishes would be
	// worse than no validation at all.
	for _, scope := range scopes {
		created := h.do(http.MethodPost, "/v1/developer/api-keys", acct.Token,
			map[string]any{"name": "scope " + scope.Key, "environment": "test",
				"scopes": []string{scope.Key}})
		if created.Code != http.StatusCreated {
			t.Fatalf("creating a key with the published scope %q = %d\n%s",
				scope.Key, created.Code, created.Body)
		}
	}
}
