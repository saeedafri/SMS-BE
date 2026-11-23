package api

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// A channel is live only when a real connector is configured. The sandbox is
// not one: a deployment whose only path for a channel is the sandbox has that
// channel in sandbox mode, and Voice has nothing at all.
func TestAChannelIsLiveOnlyWithARealConnector(t *testing.T) {
	bare := &Server{Connector: connector.NewSandbox(0),
		SMPP: &connector.SMPPRouter{Fallback: connector.NewSandbox(0)}}
	want := map[string]gen.CapabilityMode{
		"SMS": gen.CapabilityModeSandbox, "RCS": gen.CapabilityModeSandbox,
		"WHATSAPP": gen.CapabilityModeSandbox, "EMAIL": gen.CapabilityModeSandbox,
		"VOICE": gen.CapabilityModeDisabled,
	}
	for channel, mode := range want {
		if got := bare.channelMode(channel); got != mode {
			t.Errorf("no connectors: %s = %s, want %s", channel, got, mode)
		}
	}

	bound := &connector.SMPPRouter{Fallback: connector.NewSandbox(0)}
	bound.FailClosed() // the deployment has operator connections
	withBinds := &Server{SMPP: bound}
	if got := withBinds.channelMode("SMS"); got != gen.CapabilityModeLive {
		t.Errorf("SMS with operator connections = %s, want live", got)
	}
	if got := withBinds.channelMode("WHATSAPP"); got != gen.CapabilityModeSandbox {
		t.Errorf("WhatsApp with only SMPP configured = %s, want sandbox", got)
	}
}

// A sandbox channel accepts a send only from a test key, and then simulates
// it. Every other send on a channel that is not live is refused.
func TestASandboxChannelOnlySimulatesTestKeySends(t *testing.T) {
	server := &Server{ChannelModes: func(channel string) gen.CapabilityMode {
		if channel == "VOICE" {
			return gen.CapabilityModeDisabled
		}
		return gen.CapabilityModeSandbox
	}}
	cases := []struct {
		channel  string
		testMode bool
		want     sending.Disposition
	}{
		{"WHATSAPP", true, sending.SendSimulated},
		{"WHATSAPP", false, sending.SendRefused},
		{"EMAIL", false, sending.SendRefused},
		{"VOICE", true, sending.SendRefused},
	}
	for _, tc := range cases {
		got, err := server.channelDisposition(context.Background(), uuid.New(), tc.channel, tc.testMode)
		if err != nil || got != tc.want {
			t.Errorf("%s testMode=%v = %v (%v), want %v", tc.channel, tc.testMode, got, err, tc.want)
		}
	}
}

// A second go-live check of a channel while one is running answers 409.
func TestASecondGoLiveCheckWhileOneRunsIsRefused(t *testing.T) {
	ctx := context.WithValue(context.Background(), operatorKey{}, store.OperatorIdentity{Role: "admin"})
	goLiveRunning.Store(gen.ChannelIdVOICE, true)
	defer goLiveRunning.Delete(gen.ChannelIdVOICE)

	res, err := (&Server{}).RunChannelGoLiveCheck(ctx,
		gen.RunChannelGoLiveCheckRequestObject{Channel: gen.ChannelIdVOICE})
	if err != nil {
		t.Fatal(err)
	}
	if _, conflict := res.(gen.RunChannelGoLiveCheck409JSONResponse); !conflict {
		t.Fatalf("second concurrent check = %T, want 409", res)
	}
}
