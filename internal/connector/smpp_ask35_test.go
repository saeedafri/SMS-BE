package connector

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp/pdu"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

func fakeConfig(smsc *fakeSMSC, carrier string, tps, window int) SMPPConfig {
	return SMPPConfig{Carrier: carrier, Addr: smsc.addr, SystemID: "relay",
		Password: "secret", MaxTPS: tps, WindowSize: window,
		EnquireLink: 30 * time.Second, Rebind: time.Second, Protocol: DefaultSMPPProtocol(nil)}
}

func indianSMS(id, carrier string) Submission {
	return Submission{MessageID: id, Channel: "SMS", Carrier: carrier, Country: "IN",
		Msisdn: "+919820000020", Sender: "ACMERT", Body: "hi",
		DLTEntityID: "PE", DLTTemplateID: "TPL"}
}

// Ask 35 §2.5. Operators contract parallel sessions; two active connections to
// one operator must both carry traffic, not one bound and idle.
func TestTwoConnectionsToOneOperatorBothCarryTraffic(t *testing.T) {
	first, second := startFakeSMSC(t), startFakeSMSC(t)
	router := &SMPPRouter{Fallback: NewSandbox(0)}
	outcomes := router.Sync(map[string]SMPPConfig{
		"a": fakeConfig(first, "AIRTEL", 100, 4),
		"b": fakeConfig(second, "AIRTEL", 100, 4),
	}, SMPPEvents{}, true)
	for id, err := range outcomes {
		if err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
	}
	t.Cleanup(func() { router.Sync(nil, SMPPEvents{}, false) })

	batch := make([]Submission, 10)
	for i := range batch {
		batch[i] = indianSMS(fmt.Sprint("m-", i), "AIRTEL")
	}
	if _, err := router.Submit(context.Background(), batch); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if a, b := len(first.sent()), len(second.sent()); a == 0 || b == 0 {
		t.Fatalf("submits per connection = %d and %d, want both connections used", a, b)
	}
}

// Ask 35 §2.9. The receipt's done date is when the handset got the message,
// which can be hours after the receipt reaches us.
func TestAReceiptCarriesTheTimeTheHandsetGotIt(t *testing.T) {
	receipt := pdu.NewDeliverSM().(*pdu.DeliverSM)
	receipt.EsmClass = 0x04
	receipt.Message, _ = pdu.NewShortMessage(
		"id:77 sub:001 dlvrd:000 submit date:2609131200 done date:2609131405 stat:UNDELIV err:001 text:x")
	report, ok := parseDeliveryReceipt(receipt)
	if !ok {
		t.Fatal("receipt not parsed")
	}
	// Operators write receipt dates in IST, with no zone.
	want := time.Date(2026, 9, 13, 8, 35, 0, 0, time.UTC)
	if !report.OccurredAt.Equal(want) {
		t.Fatalf("OccurredAt = %s, want the done date %s", report.OccurredAt, want)
	}
}

// Ask 35 §2.2 addition. An OTP stream must not silence every campaign on the
// operator: bulk gets a floor of one tick in every few.
func TestAnOTPFloodDoesNotStarveACampaign(t *testing.T) {
	smsc := startFakeSMSC(t)
	bind, err := DialSMPP(fakeConfig(smsc, "AIRTEL", 5, 4), SMPPEvents{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 8; i++ { // more OTP demand than 5 TPS can serve
		go func(i int) {
			for n := 0; ctx.Err() == nil; n++ {
				_, _ = bind.Submit(ctx, []Submission{{MessageID: fmt.Sprint("otp-", i, "-", n),
					Msisdn: "+919820000021", Sender: "ACMERT", Channel: "SMS",
					Body: "123456 is your code", Priority: true}})
			}
		}(i)
	}
	time.Sleep(200 * time.Millisecond)

	var sent atomic.Int32
	bulk := make([]Submission, 10)
	for i := range bulk {
		bulk[i] = Submission{MessageID: fmt.Sprint("bulk-", i), Msisdn: "+919820000022",
			Sender: "ACMERT", Channel: "SMS", Body: "sale"}
	}
	receipts, _ := bind.Submit(ctx, bulk)
	for _, r := range receipts {
		if r.Accepted {
			sent.Add(1)
		}
	}
	if sent.Load() < 8 {
		t.Fatalf("%d of 10 bulk messages sent during a 10 s OTP flood at 5 TPS, want at least 8", sent.Load())
	}
}

// Ask 35 §2.2 addition. A bulk submit whose context ends while it waits for a
// token never reached the operator. That is not a timeout and not a refusal:
// it produces no receipt.
func TestAWaitThatNeverSentIsNotARefusal(t *testing.T) {
	smsc := startFakeSMSC(t)
	bind, err := DialSMPP(fakeConfig(smsc, "AIRTEL", 1, 4), SMPPEvents{})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })

	// Keep the single token busy with priority traffic.
	busy, stop := context.WithCancel(context.Background())
	defer stop()
	for i := 0; i < 3; i++ {
		go func(i int) {
			for n := 0; busy.Err() == nil; n++ {
				_, _ = bind.Submit(busy, []Submission{{MessageID: fmt.Sprint("otp-", i, "-", n),
					Msisdn: "+919820000023", Sender: "ACMERT", Channel: "SMS",
					Body: "123456 is your code", Priority: true}})
			}
		}(i)
	}
	time.Sleep(100 * time.Millisecond)

	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	receipts, _ := bind.Submit(short, []Submission{{MessageID: "waited",
		Msisdn: "+919820000024", Sender: "ACMERT", Channel: "SMS", Body: "sale"}})
	for _, r := range receipts {
		if r.MessageID == "waited" {
			t.Fatalf("a submit that never left the wait produced a receipt: %+v", r)
		}
	}
}

// The segment arithmetic billing charges for must be the split the operator
// receives, at the edges where they could disagree.
func TestSegmentCountsAgreeWithBilling(t *testing.T) {
	bodies := map[string]string{
		"160 GSM-7":           strings.Repeat("a", 160),
		"161 GSM-7":           strings.Repeat("a", 161),
		"306 GSM-7":           strings.Repeat("a", 306),
		"307 GSM-7":           strings.Repeat("a", 307),
		"70 UCS-2":            strings.Repeat("क", 70),
		"71 UCS-2":            strings.Repeat("क", 71),
		"134 UCS-2":           strings.Repeat("क", 134),
		"135 UCS-2":           strings.Repeat("क", 135),
		"80 extended (160 s)": strings.Repeat("€", 80),
		"81 extended (162 s)": strings.Repeat("€", 81),
	}
	for name, body := range bodies {
		parts, err := smppParts(Submission{Body: body, Sender: "ACMERT", Msisdn: "+919820000025"}, DefaultSMPPProtocol(nil))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := billing.SegmentCount(body); len(parts) != want {
			t.Errorf("%s: operator receives %d segments, billing charges %d", name, len(parts), want)
		}
	}
}
