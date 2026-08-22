package api_test

import (
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func createSender(t *testing.T, h *harness, token, header, channel, country string) gen.SenderId {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/sender-ids", token, map[string]string{
		"header": header, "channel": channel, "country": country,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("create sender %s: status = %d, want 201; body = %s", header, res.Code, res.Body)
	}
	var sender gen.SenderId
	res.decode(t, &sender)
	return sender
}

func TestCreateSenderStartsPendingReview(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	sender := createSender(t, h, acct.Token, "ACMERT", "SMS", "IN")

	if sender.Header != "ACMERT" {
		t.Errorf("header = %q, want ACMERT", sender.Header)
	}
	// The compliance board keys its states off this; nothing is approved on
	// arrival.
	if sender.Status != gen.ApprovalStatus("pending_review") {
		t.Errorf("status = %q, want pending_review", sender.Status)
	}
	if sender.Channel != gen.ChannelId("SMS") || sender.Country != gen.CountryCode("IN") {
		t.Errorf("channel/country = %q/%q, want SMS/IN", sender.Channel, sender.Country)
	}
	if sender.CreatedAt.IsZero() {
		t.Error("createdAt is zero")
	}
	// An SMS sender has no voice verification state at all.
	if sender.VoiceVerification != nil {
		t.Error("an SMS sender carries voiceVerification, want absent")
	}
}

func TestCreateSenderRejectsDuplicatesButAllowsOtherChannels(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	createSender(t, h, acct.Token, "DUPHDR", "SMS", "IN")

	same := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]string{
		"header": "DUPHDR", "channel": "SMS", "country": "IN",
	})
	if same.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want 409; body = %s", same.Code, same.Body)
	}
	if code := same.errorCode(t); code != "conflict" {
		t.Fatalf("error.code = %q, want conflict", code)
	}

	// The same header on a different channel is a different registration.
	other := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]string{
		"header": "DUPHDR", "channel": "RCS", "country": "IN",
	})
	if other.Code != http.StatusCreated {
		t.Fatalf("same header on another channel: status = %d, want 201; body = %s",
			other.Code, other.Body)
	}
}

func TestCreateSenderValidatesCountryAndHeader(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	blank := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]string{
		"header": "   ", "channel": "SMS", "country": "IN",
	})
	if blank.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank header: status = %d, want 422; body = %s", blank.Code, blank.Body)
	}
}

func TestSenderEndpointsRespectRoleAndTenant(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	member := h.newAccount("member")
	other := h.newAccount("owner")

	sender := createSender(t, h, owner.Token, "MINEHD", "SMS", "IN")

	t.Run("member cannot create", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/sender-ids", member.Token, map[string]string{
			"header": "MEMHDR", "channel": "SMS", "country": "IN",
		})
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
		}
	})

	t.Run("another tenant cannot read it", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/sender-ids/"+sender.Id.String(), other.Token, nil)
		if res.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
		}
	})

	t.Run("another tenant's list does not contain it", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/sender-ids", other.Token, nil)
		var senders []gen.SenderId
		res.decode(t, &senders)
		for _, s := range senders {
			if s.Id == sender.Id {
				t.Fatal("another tenant's sender appeared in the list")
			}
		}
	})

	t.Run("owner sees it in their own list", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/sender-ids", owner.Token, nil)
		var senders []gen.SenderId
		res.decode(t, &senders)
		found := false
		for _, s := range senders {
			if s.Id == sender.Id {
				found = true
			}
		}
		if !found {
			t.Fatal("the owner's own sender is missing from their list")
		}
	})
}

func TestGetSenderReturns404ForAnUnknownId(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet,
		"/v1/sender-ids/00000000-0000-0000-0000-000000000000", acct.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
	}
}

func TestVoiceVerificationRoundTrip(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "+14155550100", "VOICE", "US")

	// Before any call, a Voice sender is present but unverified.
	if sender.VoiceVerification == nil {
		t.Fatal("a Voice sender has no voiceVerification, want one")
	}
	before, err := sender.VoiceVerification.AsVoiceVerification()
	if err != nil {
		t.Fatalf("decode voiceVerification: %v", err)
	}
	if before.Status != gen.VoiceVerificationStatusUnverified {
		t.Fatalf("status = %q, want unverified", before.Status)
	}

	call := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-call",
		acct.Token, nil)
	if call.Code != http.StatusOK {
		t.Fatalf("voice-call: status = %d, want 200; body = %s", call.Code, call.Body)
	}
	var result gen.VoiceCallResult
	call.decode(t, &result)
	if len(result.Code) != 6 {
		t.Fatalf("code = %q, want six digits", result.Code)
	}

	wrong := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-code",
		acct.Token, map[string]string{"code": "000000"})
	if wrong.Code == http.StatusNoContent {
		t.Fatal("a wrong code verified the sender")
	}

	// A wrong attempt DISCARDS the code — deliberately, so a session holder
	// cannot brute-force six digits — so the original code is dead now and a new
	// call is required. This test used to reuse it and fail with "Request a
	// verification call before entering a code", which is the anti-brute-force
	// measure working, not a bug.
	//
	// Not testing the discarded code here on purpose: submitting it would be
	// another wrong attempt and would discard the REISSUED code too, which is
	// the same trap one line further down.
	recall := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-call",
		acct.Token, nil)
	if recall.Code != http.StatusOK {
		t.Fatalf("second voice-call: status = %d; body = %s", recall.Code, recall.Body)
	}
	var reissued gen.VoiceCallResult
	recall.decode(t, &reissued)

	right := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-code",
		acct.Token, map[string]string{"code": reissued.Code})
	if right.Code != http.StatusNoContent {
		t.Fatalf("voice-code: status = %d, want 204; body = %s", right.Code, right.Body)
	}

	after := h.do(http.MethodGet, "/v1/sender-ids/"+sender.Id.String(), acct.Token, nil)
	var verified gen.SenderId
	after.decode(t, &verified)
	state, err := verified.VoiceVerification.AsVoiceVerification()
	if err != nil {
		t.Fatalf("decode voiceVerification: %v", err)
	}
	if state.Status != gen.VoiceVerificationStatusVerified {
		t.Fatalf("status = %q after verifying, want verified", state.Status)
	}
}

// A code must not be replayable once spent — otherwise anyone who observed it
// once could re-verify the sender later.
func TestVoiceCodeCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "+14155550111", "VOICE", "US")

	call := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-call",
		acct.Token, nil)
	var result gen.VoiceCallResult
	call.decode(t, &result)

	if first := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-code",
		acct.Token, map[string]string{"code": result.Code}); first.Code != http.StatusNoContent {
		t.Fatalf("first use: status = %d", first.Code)
	}
	second := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-code",
		acct.Token, map[string]string{"code": result.Code})
	if second.Code == http.StatusNoContent {
		t.Fatal("a spent voice code was accepted again")
	}
}

func TestVoiceCodeBeforeAnyCallIsRejected(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "+14155550122", "VOICE", "US")

	res := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/voice-code",
		acct.Token, map[string]string{"code": "123456"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
}

func TestSenderEndpointsRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	if res := h.do(http.MethodGet, "/v1/sender-ids", "", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", res.Code, res.Body)
	}
}
