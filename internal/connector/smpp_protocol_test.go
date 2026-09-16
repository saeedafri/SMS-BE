package connector

import (
	"bytes"
	"context"
	hexenc "encoding/hex"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp/pdu"
)

var testChain = []string{"1702000000000000001", "1702000000000000099"}

// goldenSubmit is the submit_sm the client sent before protocol values were
// configurable, for the submission in submitGolden, with its sequence number
// zeroed. Captured from the code at 776ef5e.
const goldenSubmit = "000000c000000004000000000000000000050041434d455254000101393139383230303030303530000000000000010000001b596f7572206f72646572203132332068617320736869707065642e140000133132303131353930303030303030303030303114010013313230373136313030303030303030303030321402004063306161323532363932346538616332353435306234353164626531393133653534353462333930356536396232383939636566326366313430663333613864"

func submitGolden(t *testing.T, bind *SMPPBind, smsc *fakeSMSC) []byte {
	t.Helper()
	before := len(smsc.sent())
	if _, err := bind.Submit(context.Background(), []Submission{{MessageID: "g", Msisdn: "+919820000050",
		Sender: "ACMERT", Channel: "SMS", Country: "IN", Body: "Your order 123 has shipped.",
		DLTEntityID: "1201159000000000001", DLTTemplateID: "1207161000000000002"}}); err != nil {
		t.Fatal(err)
	}
	sent := smsc.sent()
	if len(sent) != before+1 {
		t.Fatalf("%d new submit_sm, want 1", len(sent)-before)
	}
	raw := append([]byte(nil), sent[before]...)
	copy(raw[12:16], []byte{0, 0, 0, 0})
	return raw
}

func protocolBind(t *testing.T, smsc *fakeSMSC, protocol SMPPProtocol) *SMPPBind {
	t.Helper()
	config := fakeConfig(smsc, "AIRTEL", 100, 4)
	config.Protocol = protocol
	bind, err := DialSMPP(config, SMPPEvents{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })
	return bind
}

// Ask 34 A1. An untouched connection is byte-identical to what went out before
// the values were configurable. Raw bytes, sequence number aside.
func TestNoProtocolIsByteIdenticalToToday(t *testing.T) {
	smsc := startFakeSMSC(t)
	raw := submitGolden(t, protocolBind(t, smsc, DefaultSMPPProtocol(testChain)), smsc)
	golden, _ := hexenc.DecodeString(goldenSubmit)
	// gosmpp keeps optional parameters in a map, so the three DLT TLVs go out
	// in any order. Operators match them by tag: the body before them must be
	// identical, and the TLVs the same set.
	body, tlvs := splitTLVs(t, raw, len(golden)-goldenTLVBytes)
	wantBody, wantTLVs := splitTLVs(t, golden, len(golden)-goldenTLVBytes)
	if hexenc.EncodeToString(body) != hexenc.EncodeToString(wantBody) ||
		strings.Join(tlvs, ",") != strings.Join(wantTLVs, ",") {
		t.Fatalf("submit_sm bytes changed:\n got %x\nwant %s", raw, goldenSubmit)
	}
}

// goldenTLVBytes is the length of the three DLT TLVs that end goldenSubmit.
const goldenTLVBytes = (4 + 19) + (4 + 19) + (4 + 64)

// splitTLVs returns the PDU up to offset and the TLVs after it, sorted.
func splitTLVs(t *testing.T, raw []byte, offset int) ([]byte, []string) {
	t.Helper()
	if offset > len(raw) {
		t.Fatalf("submit_sm is %d bytes, shorter than its body", len(raw))
	}
	var tlvs []string
	for rest := raw[offset:]; len(rest) > 0; {
		if len(rest) < 4 || len(rest) < 4+(int(rest[2])<<8|int(rest[3])) {
			t.Fatalf("truncated TLV at %x", rest)
		}
		size := 4 + (int(rest[2])<<8 | int(rest[3]))
		tlvs = append(tlvs, hexenc.EncodeToString(rest[:size]))
		rest = rest[size:]
	}
	sort.Strings(tlvs)
	return raw[:offset], tlvs
}

// Ask 34 A1. The configured tags are the tags on the wire: a swapped
// entity/template pair puts each id under the other tag.
func TestTheConfiguredTagsAreTheTagsOnTheWire(t *testing.T) {
	smsc := startFakeSMSC(t)
	swapped := DefaultSMPPProtocol(testChain)
	swapped.EntityTag, swapped.TemplateTag = 0x1401, 0x1400
	for name, tc := range map[string]struct {
		protocol    SMPPProtocol
		entity, tpl pdu.Tag
	}{
		"defaults": {DefaultSMPPProtocol(testChain), 0x1400, 0x1401},
		"swapped":  {swapped, 0x1401, 0x1400},
	} {
		raw := submitGolden(t, protocolBind(t, smsc, tc.protocol), smsc)
		if !bytes.Contains(raw, tlv(tc.entity, "1201159000000000001")) ||
			!bytes.Contains(raw, tlv(tc.tpl, "1207161000000000002")) {
			t.Errorf("%s: PE id not under %#x or template id not under %#x", name, tc.entity, tc.tpl)
		}
	}
}

func syncAndSubmit(t *testing.T, router *SMPPRouter, smsc *fakeSMSC, protocol SMPPProtocol) *pdu.SubmitSM {
	t.Helper()
	config := fakeConfig(smsc, "AIRTEL", 100, 4)
	config.Protocol = protocol
	router.Sync(map[string]SMPPConfig{"c": config}, SMPPEvents{}, true)
	before := len(smsc.sent())
	if _, err := router.Submit(context.Background(), []Submission{indianSMS("m", "AIRTEL")}); err != nil {
		t.Fatal(err)
	}
	sent := smsc.sent()
	if len(sent) != before+1 {
		t.Fatalf("%d new submit_sm, want 1", len(sent)-before)
	}
	p, err := pdu.Parse(bytes.NewReader(sent[before]))
	if err != nil {
		t.Fatal(err)
	}
	return p.(*pdu.SubmitSM)
}

// Ask 34 A1. A changed value redials on the next reload.
func TestAProtocolPatchRedialsOnTheNextReload(t *testing.T) {
	smsc := startFakeSMSC(t)
	router := &SMPPRouter{Fallback: NewSandbox(0)}
	t.Cleanup(func() { router.Sync(nil, SMPPEvents{}, false) })
	protocol := DefaultSMPPProtocol(testChain)
	if got := syncAndSubmit(t, router, smsc, protocol).RegisteredDelivery; got != 1 {
		t.Fatalf("registered_delivery = %d, want the default 1", got)
	}
	protocol.RegisteredDelivery = 0
	if got := syncAndSubmit(t, router, smsc, protocol).RegisteredDelivery; got != 0 {
		t.Fatalf("after the change registered_delivery = %d, want 0", got)
	}
}

// Ask 34 A1. The trap: a chain is a slice, and leaving it out of the reload's
// comparison would keep the old bind, and the old hash, forever.
func TestAChangedChainRedials(t *testing.T) {
	smsc := startFakeSMSC(t)
	router := &SMPPRouter{Fallback: NewSandbox(0)}
	t.Cleanup(func() { router.Sync(nil, SMPPEvents{}, false) })
	hashOf := func(sm *pdu.SubmitSM) string {
		field, ok := sm.OptionalParameters[0x1402]
		if !ok {
			t.Fatal("no chain TLV")
		}
		return string(field.Data)
	}
	first := hashOf(syncAndSubmit(t, router, smsc, DefaultSMPPProtocol(testChain)))
	changed := []string{"1702000000000000002", "1702000000000000099"}
	second := hashOf(syncAndSubmit(t, router, smsc, DefaultSMPPProtocol(changed)))
	if first == second || second != DLTChainHash("PE", changed) {
		t.Fatalf("chain hash after the change = %s, want the hash of the new chain", second)
	}
	_ = time.Second
}
