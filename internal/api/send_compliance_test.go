package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// The country's content rules apply on the send path, not only in the template
// editor.
//
// India bans public URL shorteners under DLT. The rule lived in the regime and
// was enforced when a template was created, so a bit.ly link was refused there
// and accepted here — a one-off send carrying the same link went out, was
// charged, and came back "sent". The browser checks too, but a client-side rule
// is a hint; anyone reading our own API docs could step around it.
func TestASendCarryingABannedShortenerIsRefused(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	sender := h.approvedSender(acct)
	// India also requires a registered template on every send now, so this
	// carries one. Without it BOTH sends below are refused for the missing
	// template and the shortener rule is never reached — which is what this
	// test was silently doing until submit refusals stopped being spelled the
	// same as delivery failures.
	template := h.wildcardTemplate(acct, sender)
	h.fundWallet(acct)

	res := h.do(http.MethodPost, "/v1/messages", acct.Token, map[string]any{
		"senderId":   sender,
		"templateId": template,
		"to":         "+919820000002",
		"body":       "WIN FREE CASH NOW!!! Click http://bit.ly/xyz claim 10 lakh prize",
	})
	// A refusal is an answer, not an error: this endpoint returns 202 with the
	// verdict in the body so an integrator gets the message id and the reason.
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 with a verdict\n%s", res.Code, res.Body)
	}
	var verdict struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
		CostMinor int     `json:"costMinor"`
	}
	res.decode(t, &verdict)
	if verdict.Status != "rejected" {
		t.Fatalf("a bit.ly link was accepted: status %q\n%s", verdict.Status, res.Body)
	}
	if verdict.ErrorCode == nil || *verdict.ErrorCode != "content_not_allowed" {
		t.Errorf("errorCode = %v, want content_not_allowed", verdict.ErrorCode)
	}
	if verdict.CostMinor != 0 {
		t.Errorf("a refused send was charged %d minor units", verdict.CostMinor)
	}

	// The same send without the shortener must still work — the rule is about
	// the link, not about links.
	ok := h.do(http.MethodPost, "/v1/messages", acct.Token, map[string]any{
		"senderId":   sender,
		"templateId": template,
		"to":         "+919820000002",
		"body":       "Your order has shipped. Track it at https://acme.example/orders/42",
	})
	if ok.Code != http.StatusAccepted {
		t.Fatalf("a legitimate full URL was refused: %d\n%s", ok.Code, ok.Body)
	}
	var allowed struct {
		Status string `json:"status"`
	}
	ok.decode(t, &allowed)
	if allowed.Status == "rejected" {
		t.Fatalf("a legitimate full URL was rejected: %s", ok.Body)
	}
}

// An India DLT header is exactly six letters or digits. The rule was written
// into the regime's own remediation text and enforced nowhere, so this string
// was accepted and sat in review looking like a real submission.
func TestAnIndiaHeaderMustLookLikeADltHeader(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, header := range []string{
		`a b!@#$%^&*()_+1234567890`,
		"TOOLONGHEADER",
		"SHORT",
		"AC ME1",
	} {
		res := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
			"header": header, "channel": "SMS", "country": "IN",
		})
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("header %q = %d, want 422\n%s", header, res.Code, res.Body)
		}
	}

	// Six alphanumerics is what DLT issues, and must still be accepted.
	ok := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "ACME01", "channel": "SMS", "country": "IN",
	})
	if ok.Code != http.StatusCreated {
		t.Fatalf("a valid six-character header was refused: %d\n%s", ok.Code, ok.Body)
	}

	// A Voice sender is identified by a number, not a DLT header, so the rule
	// must not reach it.
	voice := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "Acme Support Line", "channel": "VOICE", "country": "IN",
		"callerIdNumber": "+919820000001",
	})
	if voice.Code != http.StatusCreated {
		t.Fatalf("the DLT header rule leaked onto a Voice sender: %d\n%s", voice.Code, voice.Body)
	}
}

// A retried send must not become a second message and a second charge.
//
// This is the endpoint where idempotency matters most and the one place the
// contract did not offer it: Idempotency-Key was on campaign create and contact
// import, both of which are far less likely to be retried than a single OTP
// whose response timed out.
func TestARetriedSendWithTheSameKeyIsNotChargedTwice(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	sender := h.approvedSender(acct)
	h.fundWallet(acct)

	send := func(key string) (string, int) {
		t.Helper()
		res := h.doWithHeaders(http.MethodPost, "/v1/messages", acct.Token,
			map[string]any{
				"senderId": sender, "to": "+919820000006", "body": "Your code is 4821",
			},
			map[string]string{"Idempotency-Key": key})
		if res.Code != http.StatusAccepted {
			t.Fatalf("send = %d\n%s", res.Code, res.Body)
		}
		var out struct {
			Id        *string `json:"id"`
			CostMinor int     `json:"costMinor"`
		}
		res.decode(t, &out)
		if out.Id == nil {
			t.Fatalf("no message id: %s", res.Body)
		}
		return *out.Id, out.CostMinor
	}

	key := "otp-retry-" + uuid.NewString()
	firstID, firstCost := send(key)
	replayID, replayCost := send(key)

	if replayID != firstID {
		t.Fatalf("the retry sent a second message: %s then %s", firstID, replayID)
	}
	if replayCost != firstCost {
		t.Errorf("replay cost %d, original %d", replayCost, firstCost)
	}

	// A different key is a different message — the guard must not collapse
	// genuinely separate sends.
	otherID, _ := send("otp-retry-" + uuid.NewString())
	if otherID == firstID {
		t.Fatal("two different keys produced the same message")
	}
}
