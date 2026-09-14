package connector

import (
	"testing"

	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"
)

func deliverSM(t *testing.T, esm byte, from, to, text string, coding data.Encoding) *pdu.DeliverSM {
	t.Helper()
	d := pdu.NewDeliverSM().(*pdu.DeliverSM)
	d.EsmClass = esm
	_ = d.SourceAddr.SetAddress(from)
	_ = d.DestAddr.SetAddress(to)
	message, err := pdu.NewShortMessageWithEncoding(text, coding)
	if err != nil {
		t.Fatal(err)
	}
	d.Message = message
	return d
}

func recordingBind() (*SMPPBind, *[]DeliveryReport, *[]InboundSMS) {
	var reports []DeliveryReport
	var inbound []InboundSMS
	b := &SMPPBind{config: SMPPConfig{Carrier: "VIDEOCON"}, events: SMPPEvents{
		Report:  func(r DeliveryReport) { reports = append(reports, r) },
		Inbound: func(m InboundSMS) { inbound = append(inbound, m) },
	}}
	return b, &reports, &inbound
}

// P1-2. A handset's reply is a deliver_sm without the receipt bit. It used to be
// acknowledged and thrown away.
func TestAHandsetReplyBecomesAnInboundMessage(t *testing.T) {
	b, reports, inbound := recordingBind()
	resp, closing := b.handle(deliverSM(t, 0x00, "919876501234", "TEXTFI", "STOP", data.GSM7BIT))
	if resp == nil || closing {
		t.Fatalf("a reply must be acknowledged and keep the bind: resp=%v closing=%v", resp, closing)
	}
	if len(*reports) != 0 {
		t.Errorf("a reply was treated as a delivery receipt: %+v", *reports)
	}
	if len(*inbound) != 1 {
		t.Fatalf("inbound events = %d, want 1", len(*inbound))
	}
	got := (*inbound)[0]
	if got.Carrier != "VIDEOCON" || got.From != "919876501234" || got.To != "TEXTFI" || got.Text != "STOP" {
		t.Errorf("inbound = %+v", got)
	}
}

// Hindi replies arrive UCS-2 and must reach the inbox as text, not bytes.
func TestAUnicodeReplyIsDecoded(t *testing.T) {
	b, _, inbound := recordingBind()
	b.handle(deliverSM(t, 0x00, "919876501234", "TEXTFI", "रोकें", data.UCS2))
	if len(*inbound) != 1 || (*inbound)[0].Text != "रोकें" {
		t.Fatalf("inbound = %+v, want the Devanagari text", *inbound)
	}
}

// No regression: a receipt still settles and is not also an inbound message.
func TestAReceiptIsStillAReceipt(t *testing.T) {
	b, reports, inbound := recordingBind()
	b.handle(deliverSM(t, 0x04, "919876501234", "TEXTFI",
		"id:77 sub:001 dlvrd:001 submit date:2609131200 done date:2609131201 stat:DELIVRD err:000 text:x", data.GSM7BIT))
	if len(*reports) != 1 || (*reports)[0].CarrierRef != "77" {
		t.Fatalf("reports = %+v, want the receipt for 77", *reports)
	}
	if len(*inbound) != 0 {
		t.Errorf("a receipt was also reported as a reply: %+v", *inbound)
	}
}
