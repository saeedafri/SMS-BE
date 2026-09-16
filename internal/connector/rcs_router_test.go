package connector

import (
	"context"
	"errors"
	"testing"
)

type recordingGateway struct {
	name string
	got  []Submission
}

func (g *recordingGateway) Name() string                  { return g.name }
func (g *recordingGateway) Vendor() string                { return g.name }
func (g *recordingGateway) Health(context.Context) Health { return Health{Healthy: true} }
func (g *recordingGateway) Capability(context.Context, string, string) (RCSCapability, error) {
	return RCSCapability{}, nil
}
func (g *recordingGateway) Reachable(context.Context, string, []string) ([]string, error) {
	return nil, nil
}
func (g *recordingGateway) Submit(_ context.Context, s []Submission) ([]Receipt, error) {
	g.got = append(g.got, s...)
	receipts := make([]Receipt, len(s))
	for i := range s {
		receipts[i] = Receipt{MessageID: s[i].MessageID, Accepted: true}
	}
	return receipts, nil
}

func account(carrier, version string, gateway RCSGateway, builds *int) RCSAccount {
	return RCSAccount{Carrier: carrier, Version: version, Build: func() (RCSGateway, error) {
		*builds++
		return gateway, nil
	}}
}

func TestTheRCSRouterSendsEachMessageThroughItsOwnOperator(t *testing.T) {
	jio, airtel := &recordingGateway{name: "jio"}, &recordingGateway{name: "airtel"}
	router := &RCSRouter{}
	builds := 0
	router.Sync([]RCSAccount{account("jio", "1", jio, &builds), account("AIRTEL", "1", airtel, &builds)})

	receipts, err := router.Submit(context.Background(), []Submission{
		{MessageID: "a", Carrier: "JIO"}, {MessageID: "b", Carrier: "AIRTEL"},
		{MessageID: "c", Carrier: "VI"}, {MessageID: "d", Carrier: "JIO"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jio.got) != 2 || len(airtel.got) != 1 {
		t.Errorf("jio got %d, airtel got %d; want 2 and 1", len(jio.got), len(airtel.got))
	}
	codes := map[string]string{}
	for _, r := range receipts {
		codes[r.MessageID] = r.ErrorCode
	}
	if len(receipts) != 4 || codes["c"] != "NO_RCS_CONNECTION" {
		t.Errorf("receipts = %+v, want the VI message refused NO_RCS_CONNECTION", receipts)
	}
}

func TestTheRCSRouterKeepsAnUnchangedGatewayAndItsTokens(t *testing.T) {
	router := &RCSRouter{}
	builds := 0
	jio := &recordingGateway{name: "jio"}
	router.Sync([]RCSAccount{account("JIO", "v1", jio, &builds)})
	router.Sync([]RCSAccount{account("JIO", "v1", jio, &builds)})
	if builds != 1 {
		t.Fatalf("an unchanged account was built %d times, want once", builds)
	}
	router.Sync([]RCSAccount{account("JIO", "v2", jio, &builds)})
	if builds != 2 {
		t.Errorf("a changed account was not rebuilt")
	}
	failures := router.Sync([]RCSAccount{{Carrier: "JIO", Version: "v3",
		Build: func() (RCSGateway, error) { return nil, errors.New("secret undecryptable") }}})
	if failures["JIO"] == nil || !router.Empty() {
		t.Errorf("a failed build = %v, empty %t; want reported and left out", failures, router.Empty())
	}
}

func TestAnEmptyRCSRouterIsNoGateway(t *testing.T) {
	sandbox := &recordingGateway{name: "sandbox"}
	router := &RCSRouter{}
	registry := Registry{Default: sandbox, ByChannel: map[string]Connector{"RCS": router}}
	if _, dedicated := registry.Dedicated("RCS"); dedicated || registry.For("RCS") != Connector(sandbox) {
		t.Fatal("an RCS router with no accounts took RCS away from the default")
	}
	builds := 0
	router.Sync([]RCSAccount{account("JIO", "1", &recordingGateway{name: "jio"}, &builds)})
	if _, dedicated := registry.Dedicated("RCS"); !dedicated {
		t.Error("an RCS router with an account is not the RCS gateway")
	}
}
