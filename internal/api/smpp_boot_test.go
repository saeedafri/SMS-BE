package api_test

import (
	"context"
	"testing"
)

// Ask 37. The first reload happens before the server takes traffic, so the
// first minute after a deploy is never a sandbox minute.
func TestTheFirstReloadHappensBeforeTraffic(t *testing.T) {
	h := newSendHarness(t)
	sandbox := h.withSMPP("live")
	h.seedConnection(h.operatorToken(), "live", 1, false)

	t.Log(h.server.BootSMPP(context.Background()))
	status, code, cost := h.sendOneSMS()
	if status != "rejected" || code != "no_operator_bind" || cost != 0 || sandbox.submitted.Load() != 0 {
		t.Fatalf("first SMS after boot: %s/%s cost %d, sandbox saw %d; want rejected/no_operator_bind at 0",
			status, code, cost, sandbox.submitted.Load())
	}
}

// Ask 37. A deployment that cannot read its own configuration at boot must not
// decide it has none.
func TestAnUnreadableConfigAtBootFailsClosed(t *testing.T) {
	h := newSendHarness(t)
	sandbox := h.withSMPP("live")
	ctx := context.Background()
	original := h.server.OperatorDB
	h.server.OperatorDB = unreadablePool(t)
	t.Log(h.server.BootSMPP(ctx))
	h.server.OperatorDB = original

	status, code, _ := h.sendOneSMS()
	if status != "rejected" || code != "no_operator_bind" || sandbox.submitted.Load() != 0 {
		t.Fatalf("SMS after an unreadable boot: %s/%s, sandbox saw %d; want refused", status, code, sandbox.submitted.Load())
	}
}
