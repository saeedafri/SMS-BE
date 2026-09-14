package api_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"
)

// P1-2: a handset's reply reaches the inbox, and STOP stops.

func reply(t *testing.T, from, to, text string) pdu.PDU {
	t.Helper()
	d := pdu.NewDeliverSM().(*pdu.DeliverSM)
	_ = d.SourceAddr.SetAddress(from)
	_ = d.DestAddr.SetAddress(to)
	message, err := pdu.NewShortMessageWithEncoding(text, data.GSM7BIT)
	if err != nil {
		t.Fatal(err)
	}
	d.Message = message
	return d
}

// inboundHarness binds a test-environment connection to a simulated operator
// and gives a tenant an approved SMS header nobody else holds.
func inboundHarness(t *testing.T) (*harness, *operatorSMSC, account, string) {
	t.Helper()
	h := newSendHarness(t)
	h.withSMPP("test")
	operator := h.operatorToken()
	smsc := startOperatorSMSC(t)
	h.seedConnection(operator, "test", smsc.port(), true)
	if err := h.server.ReloadSMPPBinds(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant := h.newAccount("owner")
	header := fmt.Sprintf("R%05d", rand.Intn(100000))
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved')`, tenant.TenantID, header); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	return h, smsc, tenant, header
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAnSMSReplyAppearsInTheInbox(t *testing.T) {
	h, smsc, tenant, header := inboundHarness(t)
	from := fmt.Sprintf("9198765%05d", rand.Intn(100000))

	smsc.push(reply(t, from, header, "When does my order arrive?"))

	eventually(t, "the reply in GET /v1/conversations", func() bool {
		var page struct {
			Conversations []struct {
				Identity           string `json:"identity"`
				Channel            string `json:"channel"`
				LastMessagePreview string `json:"lastMessagePreview"`
			} `json:"conversations"`
		}
		h.do(http.MethodGet, "/v1/conversations", tenant.Token, nil).decode(t, &page)
		for _, c := range page.Conversations {
			if c.Identity == "+"+from && c.Channel == "SMS" &&
				strings.Contains(c.LastMessagePreview, "When does my order arrive?") {
				return true
			}
		}
		return false
	})
}

func TestStopSuppressesTheNumberAndTheNextSendIsRefused(t *testing.T) {
	h, smsc, tenant, header := inboundHarness(t)
	suffix := rand.Intn(100000)
	from := fmt.Sprintf("9198765%05d", suffix)

	smsc.push(reply(t, from, header, "  stop "))

	eventually(t, "the number on the suppression list", func() bool {
		res := h.do(http.MethodGet, "/v1/suppressions", tenant.Token, nil)
		return strings.Contains(string(res.Body), "+"+from)
	})

	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "to": fmt.Sprintf("98765%05d", suffix),
		"body": "Big sale today.",
	})
	var result struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
		CostMinor int64   `json:"costMinor"`
	}
	res.decode(t, &result)
	if result.ErrorCode == nil || *result.ErrorCode != "recipient_suppressed" || result.CostMinor != 0 {
		t.Fatalf("send after STOP = %s, want refused recipient_suppressed at cost 0", res.Body)
	}
}

// An ordinary reply is not an opt-out.
func TestAReplyThatIsNotAStopKeywordDoesNotSuppress(t *testing.T) {
	h, smsc, tenant, header := inboundHarness(t)
	from := fmt.Sprintf("9198765%05d", rand.Intn(100000))

	smsc.push(reply(t, from, header, "please stop by the store"))
	eventually(t, "the reply in the inbox", func() bool {
		return strings.Contains(string(h.do(http.MethodGet, "/v1/conversations", tenant.Token, nil).Body), "+"+from)
	})
	if res := h.do(http.MethodGet, "/v1/suppressions", tenant.Token, nil); strings.Contains(string(res.Body), "+"+from) {
		t.Fatalf("a sentence containing 'stop' suppressed the number: %s", res.Body)
	}
}

// A reply to a header no tenant holds is logged and acknowledged; the bind stays up.
func TestAReplyToAnUnknownHeaderIsAcknowledgedAndLogged(t *testing.T) {
	h, smsc, _, _ := inboundHarness(t)
	before := smsc.acks.Load()

	smsc.push(reply(t, "919876500000", "NOSUCH", "hello"))

	eventually(t, "the deliver_sm_resp", func() bool { return smsc.acks.Load() > before })
	eventually(t, "a log line naming the unresolved reply", func() bool {
		return strings.Contains(h.logs.String(), "inbound SMS")
	})
	for _, id := range h.server.SMPP.BoundIDs() {
		if _, _, ok := h.server.SMPP.BindHealth(id); !ok {
			t.Errorf("bind %s lost after an unknown reply", id)
		}
	}
}
