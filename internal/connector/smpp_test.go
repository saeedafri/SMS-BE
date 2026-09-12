package connector

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp/pdu"
)

// fakeSMSC is an operator on a loopback socket: it accepts a bind, answers every
// submit_sm with an incrementing message id, and sends a DELIVRD receipt for
// each. It keeps the raw bytes of every submit_sm, so tests assert on what went
// over the wire rather than on what the library says it sent.
type fakeSMSC struct {
	addr string

	mu      sync.Mutex
	submits [][]byte
	nextID  int
	refuse  bool
}

func startFakeSMSC(t *testing.T) *fakeSMSC {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	smsc := &fakeSMSC{addr: listener.Addr().String()}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go smsc.serve(conn)
		}
	}()
	return smsc
}

func (f *fakeSMSC) serve(conn net.Conn) {
	defer conn.Close()
	write := func(p pdu.PDU) {
		buf := pdu.NewBuffer(make([]byte, 0, 128))
		p.Marshal(buf)
		_, _ = conn.Write(buf.Bytes())
	}
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		raw := make([]byte, binary.BigEndian.Uint32(header))
		copy(raw, header)
		if _, err := io.ReadFull(conn, raw[4:]); err != nil {
			return
		}
		p, err := pdu.Parse(bytes.NewReader(raw))
		if err != nil {
			return
		}
		switch v := p.(type) {
		case *pdu.BindRequest, *pdu.EnquireLink:
			write(v.GetResponse())
		case *pdu.Unbind:
			write(v.GetResponse())
			return
		case *pdu.SubmitSM:
			f.mu.Lock()
			f.submits = append(f.submits, raw)
			f.nextID++
			id := fmt.Sprint(f.nextID)
			refuse := f.refuse
			f.mu.Unlock()

			resp := v.GetResponse().(*pdu.SubmitSMResp)
			if refuse {
				resp.CommandStatus = 0x00000045 // ESME_RSUBMITFAIL
				write(resp)
				continue
			}
			resp.MessageID = id
			write(resp)

			receipt := pdu.NewDeliverSM().(*pdu.DeliverSM)
			receipt.EsmClass = 0x04
			receipt.Message, _ = pdu.NewShortMessage("id:" + id +
				" sub:001 dlvrd:001 submit date:2609131200 done date:2609131201 stat:DELIVRD err:000 text:x")
			write(receipt)
		}
	}
}

func (f *fakeSMSC) sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.submits...)
}

func tlv(tag pdu.Tag, value string) []byte {
	out := []byte{byte(tag >> 8), byte(tag), byte(len(value) >> 8), byte(len(value))}
	return append(out, value...)
}

func dialFake(t *testing.T, smsc *fakeSMSC, chain []string) (*SMPPBind, chan DeliveryReport) {
	t.Helper()
	reports := make(chan DeliveryReport, 16)
	bind, err := DialSMPP(SMPPConfig{Carrier: "AIRTEL", Addr: smsc.addr, SystemID: "relay",
		Password: "secret", MaxTPS: 100, WindowSize: 4,
		EnquireLink: 30 * time.Second, Rebind: time.Second},
		chain, func(r DeliveryReport) { reports <- r })
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })
	return bind, reports
}

// The whole point of the client: a message reaches the operator carrying the
// DLT identity India scrubs against, and the operator's receipt comes back as a
// delivery report naming the id the operator issued.
func TestAnSMSCarriesItsDLTIdentityAndItsReceiptComesBack(t *testing.T) {
	smsc := startFakeSMSC(t)
	bind, reports := dialFake(t, smsc, []string{"1702TM0001", "1702RELAY01"})

	receipts, err := bind.Submit(context.Background(), []Submission{{
		MessageID: "m-1", Msisdn: "+919820000001", Sender: "ACMERT", Channel: "SMS",
		Country: "IN", Body: "Your OTP is 482913. Do not share it.",
		DLTEntityID: "1201159000000000001", DLTTemplateID: "1207161000000000002",
	}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(receipts) != 1 || !receipts[0].Accepted || receipts[0].CarrierRef != "1" {
		t.Fatalf("receipt = %+v, want accepted with the operator's id", receipts)
	}

	raw := smsc.sent()[0]
	wantHash := DLTChainHash("1201159000000000001", []string{"1702TM0001", "1702RELAY01"})
	for name, field := range map[string][]byte{
		"PE id":       tlv(TagDLTEntityID, "1201159000000000001"),
		"template id": tlv(TagDLTTemplateID, "1207161000000000002"),
		"chain hash":  tlv(TagDLTChainHash, wantHash),
	} {
		if !bytes.Contains(raw, field) {
			t.Errorf("submit_sm on the wire has no %s TLV", name)
		}
	}
	// The chain is the TRAI format: SHA-256 hex of PE,TM,...,TM with no spaces.
	if len(wantHash) != 64 || DLTChainHash("PE", []string{"TM"}) == DLTChainHash("PE ", []string{"TM"}) {
		t.Errorf("chain hash is not a SHA-256 over the exact joined ids: %q", wantHash)
	}

	select {
	case report := <-reports:
		if report.CarrierRef != "1" || !report.Delivered {
			t.Errorf("report = %+v, want delivered for operator id 1", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery report arrived from the operator's deliver_sm")
	}
}

// Hindi does not fit GSM 03.38 and a long body does not fit one segment. Both
// have to reach the handset whole: UCS-2, concatenated with a UDH.
func TestALongHindiMessageGoesAsUCS2Segments(t *testing.T) {
	smsc := startFakeSMSC(t)
	bind, _ := dialFake(t, smsc, nil)

	body := strings.Repeat("आपका ऑर्डर भेज दिया गया है। ", 6)
	receipts, _ := bind.Submit(context.Background(), []Submission{{
		MessageID: "m-2", Msisdn: "+919820000002", Sender: "ACMERT", Channel: "SMS", Body: body,
	}})
	if !receipts[0].Accepted {
		t.Fatalf("receipt = %+v, want accepted", receipts[0])
	}
	parts := smsc.sent()
	if len(parts) < 2 {
		t.Fatalf("%d submit_sm, want the body split into segments", len(parts))
	}
	for i, raw := range parts {
		p, _ := pdu.Parse(bytes.NewReader(raw))
		sm := p.(*pdu.SubmitSM)
		if sm.EsmClass&0x40 == 0 {
			t.Errorf("segment %d has no UDH flag, so the handset shows fragments", i)
		}
		if sm.Message.Encoding().DataCoding() != 0x08 {
			t.Errorf("segment %d data_coding = %#x, want UCS-2 (0x08)", i, sm.Message.Encoding().DataCoding())
		}
	}
}

// An operator refusal is a refusal, with the operator's own status code, never
// an acceptance.
func TestAnOperatorRefusalIsNotAnAcceptance(t *testing.T) {
	smsc := startFakeSMSC(t)
	smsc.refuse = true
	bind, _ := dialFake(t, smsc, nil)

	receipts, _ := bind.Submit(context.Background(), []Submission{{
		MessageID: "m-3", Msisdn: "+919820000003", Sender: "ACMERT", Channel: "SMS", Body: "hi",
	}})
	if receipts[0].Accepted || receipts[0].ErrorCode != "0x00000045" {
		t.Errorf("receipt = %+v, want refused with the operator's status", receipts[0])
	}
}

// With real operators bound, an SMS must never be handed to the sandbox: its
// "delivered" would be a lie the customer pays for. With none bound, nothing
// changes from before the client existed.
func TestTheRouterNeverHandsARealSMSToTheSandbox(t *testing.T) {
	sandbox := NewSandbox(0)
	empty := &SMPPRouter{Fallback: sandbox}
	receipts, _ := empty.Submit(context.Background(), []Submission{{
		MessageID: "a", Msisdn: "+919820000004", Channel: "SMS", Carrier: "JIO", Body: "hi"}})
	if !receipts[0].Accepted {
		t.Errorf("with no binds an SMS should reach the sandbox as before: %+v", receipts[0])
	}

	smsc := startFakeSMSC(t)
	router := &SMPPRouter{Fallback: sandbox}
	outcomes := router.Sync(map[string]SMPPConfig{"c1": {Carrier: "AIRTEL", Addr: smsc.addr,
		SystemID: "relay", Password: "secret", MaxTPS: 100, WindowSize: 4,
		EnquireLink: 30 * time.Second, Rebind: time.Second}}, nil, func(DeliveryReport) {})
	if outcomes["c1"] != nil {
		t.Fatalf("sync bind: %v", outcomes["c1"])
	}
	t.Cleanup(func() { router.Sync(nil, nil, nil) })

	got, _ := router.Submit(context.Background(), []Submission{
		{MessageID: "no-bind", Channel: "SMS", Carrier: "JIO", Country: "IN", Msisdn: "+919820000005", Sender: "ACMERT", Body: "hi"},
		{MessageID: "no-dlt", Channel: "SMS", Carrier: "AIRTEL", Country: "IN", Msisdn: "+919820000006", Sender: "ACMERT", Body: "hi"},
		{MessageID: "email", Channel: "EMAIL", Msisdn: "+919820000007", Body: "hi"},
		{MessageID: "real", Channel: "SMS", Carrier: "airtel", Country: "IN", Msisdn: "+919820000008", Sender: "ACMERT", Body: "hi",
			DLTEntityID: "PE", DLTTemplateID: "TPL"},
	})
	byID := map[string]Receipt{}
	for _, r := range got {
		byID[r.MessageID] = r
	}
	if r := byID["no-bind"]; r.Accepted || r.ErrorCode != "NO_OPERATOR_BIND" {
		t.Errorf("SMS for an unbound operator = %+v, want NO_OPERATOR_BIND", r)
	}
	if r := byID["no-dlt"]; r.Accepted || r.ErrorCode != "DLT_IDS_MISSING" {
		t.Errorf("Indian SMS without DLT ids = %+v, want DLT_IDS_MISSING", r)
	}
	if r := byID["email"]; !r.Accepted {
		t.Errorf("non-SMS should still reach the fallback: %+v", r)
	}
	if r := byID["real"]; !r.Accepted || r.CarrierRef == "" {
		t.Errorf("SMS for the bound operator = %+v, want accepted by the operator", r)
	}
	if n := len(smsc.sent()); n != 1 {
		t.Errorf("operator saw %d submit_sm, want exactly the one routable message", n)
	}
}
