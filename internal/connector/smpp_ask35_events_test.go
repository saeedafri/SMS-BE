package connector

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// countingConnector records what reached the sandbox, so a test asserts what
// the sandbox did rather than trusting a receipt code.
type countingConnector struct {
	Connector
	submitted atomic.Int32
}

func (c *countingConnector) Submit(ctx context.Context, s []Submission) ([]Receipt, error) {
	c.submitted.Add(int32(len(s)))
	return c.Connector.Submit(ctx, s)
}

func lateEvents(reports chan DeliveryReport, late chan LateSubmit) SMPPEvents {
	return SMPPEvents{
		Report:     func(r DeliveryReport) { reports <- r },
		LateSubmit: func(l LateSubmit) { late <- l },
	}
}

// Ask 35 §2.1. A receipt is only meaningful on the operator it arrived from,
// because operators number messages independently.
func TestAReceiptNamesTheCarrierItArrivedOn(t *testing.T) {
	smsc := startFakeSMSC(t)
	reports := make(chan DeliveryReport, 4)
	bind, err := DialSMPP(fakeConfig(smsc, "AIRTEL", 100, 4), nil,
		SMPPEvents{Report: func(r DeliveryReport) { reports <- r }})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })
	if _, err := bind.Submit(context.Background(), []Submission{indianSMS("m", "AIRTEL")}); err != nil {
		t.Fatal(err)
	}
	select {
	case report := <-reports:
		if report.Carrier != "AIRTEL" {
			t.Fatalf("report.Carrier = %q, want AIRTEL", report.Carrier)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no receipt")
	}
}

// Ask 35 §2.2. A missing submit_sm_resp means we do not know. The operator may
// still answer, and that answer must reach the message.
func TestALateSubmitResponseIsStillRecorded(t *testing.T) {
	previous := smppResponseTimeout
	smppResponseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { smppResponseTimeout = previous })

	smsc := startFakeSMSC(t)
	smsc.respondAfter = 600 * time.Millisecond
	late := make(chan LateSubmit, 1)
	bind, err := DialSMPP(fakeConfig(smsc, "AIRTEL", 100, 4), nil,
		lateEvents(make(chan DeliveryReport, 4), late))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })

	receipts, _ := bind.Submit(context.Background(), []Submission{indianSMS("slow", "AIRTEL")})
	if len(receipts) != 1 || receipts[0].Accepted || receipts[0].ErrorCode != "SUBMIT_TIMEOUT" {
		t.Fatalf("receipt = %+v, want SUBMIT_TIMEOUT", receipts)
	}
	select {
	case got := <-late:
		if got.MessageID != "slow" || !got.Accepted || got.CarrierRef != "1" || got.Carrier != "AIRTEL" {
			t.Fatalf("late submit = %+v, want slow accepted as 1 on AIRTEL", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the late submit_sm_resp was dropped")
	}
}

// Ask 35 §2.2 addition. A message that waited and was never sent is retried,
// and its outcome reported, once the bind has room.
func TestAWaitThatNeverSentIsSentLater(t *testing.T) {
	smsc := startFakeSMSC(t)
	// Every message that was waiting when its caller gave up is retried, the
	// cancelled OTPs included, so the channel carries more than the one asserted.
	late := make(chan LateSubmit, 64)
	bind, err := DialSMPP(fakeConfig(smsc, "AIRTEL", 1, 4), nil,
		lateEvents(make(chan DeliveryReport, 64), late))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = bind.Close() })

	busy, stop := context.WithCancel(context.Background())
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
	_, _ = bind.Submit(short, []Submission{{MessageID: "waited",
		Msisdn: "+919820000024", Sender: "ACMERT", Channel: "SMS", Body: "sale"}})
	stop()

	deadline := time.After(15 * time.Second)
	for {
		select {
		case got := <-late:
			if got.MessageID != "waited" {
				continue
			}
			if !got.Accepted || got.CarrierRef == "" {
				t.Fatalf("late submit = %+v, want waited accepted", got)
			}
			return
		case <-deadline:
			t.Fatal("the message that waited was never sent")
		}
	}
}

// Ask 35 §2.7. A carrier host that drops packets must not hold the console's
// Test connection request for the operating system's connect timeout.
func TestADeadHostDoesNotHoldTheConsole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := ProbeSMPP(ctx, SMPPConfig{Carrier: "AIRTEL", Addr: "10.255.255.1:2775",
		SystemID: "relay", Password: "secret", MaxTPS: 1, WindowSize: 1})
	if err == nil {
		t.Fatal("a probe of a dead host succeeded")
	}
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("probe held for %s, want it to end with its request", waited)
	}
}

// Ask 35 §2.7. One dead host must not delay every other connection in a tick.
func TestADeadHostDoesNotDelayTheOtherBinds(t *testing.T) {
	smsc := startFakeSMSC(t)
	dead := SMPPConfig{Carrier: "JIO", SystemID: "relay", Password: "secret",
		MaxTPS: 1, WindowSize: 1, EnquireLink: 30 * time.Second, Rebind: time.Hour}
	first, second := dead, dead
	first.Addr, second.Addr = "10.255.255.1:2775", "10.255.255.2:2775"
	router := &SMPPRouter{Fallback: NewSandbox(0)}
	started := time.Now()
	router.Sync(map[string]SMPPConfig{
		"dead-1": first, "dead-2": second, "live": fakeConfig(smsc, "AIRTEL", 100, 4),
	}, nil, SMPPEvents{}, true)
	t.Cleanup(func() { router.Sync(nil, nil, SMPPEvents{}, true) })
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("Sync took %s with two dead hosts, want the dials in parallel", took)
	}
}

// Ask 37. Once any connection row exists, an SMS with no bind that can submit
// it is refused, whatever the reason there is no bind. The sandbox only serves
// a deployment with no connections at all.
func TestAConfiguredDeploymentNeverUsesTheSandbox(t *testing.T) {
	sms := indianSMS("s", "AIRTEL")
	cases := []struct {
		name       string
		wanted     map[string]SMPPConfig
		configured bool
		submission Submission
		sandbox    int32
		code       string
	}{
		{"disabled", map[string]SMPPConfig{}, true, sms, 0, "NO_OPERATOR_BIND"},
		{"dial failed", map[string]SMPPConfig{"c1": {Carrier: "AIRTEL", Addr: "127.0.0.1:1",
			SystemID: "relay", Password: "secret", MaxTPS: 1, WindowSize: 1,
			EnquireLink: 30 * time.Second, Rebind: time.Hour}}, true, sms, 0, "NO_OPERATOR_BIND"},
		{"unconfigured", map[string]SMPPConfig{}, false, sms, 1, ""},
		{"non-SMS", map[string]SMPPConfig{}, true,
			Submission{MessageID: "e", Channel: "EMAIL", Msisdn: "+919820000026", Body: "hi"}, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := &countingConnector{Connector: NewSandbox(0)}
			router := &SMPPRouter{Fallback: sandbox}
			router.Sync(tc.wanted, nil, SMPPEvents{}, tc.configured)
			t.Cleanup(func() { router.Sync(nil, nil, SMPPEvents{}, false) })
			receipts, _ := router.Submit(context.Background(), []Submission{tc.submission})
			if got := sandbox.submitted.Load(); got != tc.sandbox {
				t.Fatalf("sandbox received %d submissions, want %d", got, tc.sandbox)
			}
			if tc.code != "" && (len(receipts) != 1 || receipts[0].ErrorCode != tc.code) {
				t.Fatalf("receipts = %+v, want %s", receipts, tc.code)
			}
		})
	}
}
