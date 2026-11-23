package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/failures"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// Channel truth: what each channel does on this deployment and for each tenant.
//
// Two facts, kept apart because they change for different reasons. The MODE
// is the deployment's: a channel is live only when a real connector with its
// credentials is configured, sandbox when sends can only be simulated, and
// disabled when the platform has nothing for it. ENABLEMENT is the tenant's:
// the operator switches a channel on or off per tenant. A send goes out only
// when both say yes.

// channelOrder is every channel the platform knows, in the order screens list them.
var channelOrder = []gen.ChannelId{gen.ChannelIdSMS, gen.ChannelIdRCS,
	gen.ChannelIdWHATSAPP, gen.ChannelIdEMAIL, gen.ChannelIdVOICE}

func channelNames() []string {
	names := make([]string, len(channelOrder))
	for i, channel := range channelOrder {
		names[i] = string(channel)
	}
	return names
}

// channelConfigKeys are the configuration keys each channel needs, by NAME.
// Their values are never read here and never returned anywhere.
var channelConfigKeys = map[gen.ChannelId][]string{
	gen.ChannelIdSMS: {"CONNECTION_ENCRYPTION_KEY", "DLT_TM_CHAIN"},
	gen.ChannelIdRCS: {"RCS_WEBHOOK_TOKEN"},
	gen.ChannelIdWHATSAPP: {"META_APP_ID", "META_APP_SECRET", "META_WEBHOOK_VERIFY_TOKEN",
		"META_EMBEDDED_SIGNUP_CONFIG_ID", "META_GRAPH_API_VERSION", "WHATSAPP_ONBOARDING_CAP"},
	gen.ChannelIdEMAIL: {"SES_REGION", "SES_CREDENTIALS", "SES_EVENT_DESTINATION",
		"SES_CONFIGURATION_SET_PREFIX", "EMAIL_WEBHOOK_SIGNING_SECRET"},
}

// ChannelConfigKeyNames is every key name above, for the process to report
// presence of at boot.
func ChannelConfigKeyNames() []string {
	var names []string
	for _, keys := range channelConfigKeys {
		names = append(names, keys...)
	}
	sort.Strings(names)
	return names
}

// channelMode is what this deployment does with a channel. The sandbox is not
// a connector: a channel whose only path is the sandbox is in sandbox mode.
func (s *Server) channelMode(channel string) gen.CapabilityMode {
	if s.ChannelModes != nil {
		return s.ChannelModes(channel)
	}
	switch channel {
	case "SMS":
		if s.SMPP != nil && s.SMPP.Configured() {
			return gen.CapabilityModeLive
		}
		return gen.CapabilityModeSandbox
	case "RCS":
		if _, sends := s.RCSCarrier.(connector.Connector); sends {
			return gen.CapabilityModeLive
		}
		if s.RCS != nil && !s.RCS.Empty() {
			return gen.CapabilityModeLive
		}
		return gen.CapabilityModeSandbox
	case "WHATSAPP", "EMAIL":
		return gen.CapabilityModeSandbox
	}
	return gen.CapabilityModeDisabled
}

// channelDisposition is the send path's question: for this tenant, does a
// message on this channel go out, get simulated, or get refused?
func (s *Server) channelDisposition(ctx context.Context, tenantID uuid.UUID, channel string,
	testMode bool) (sending.Disposition, error) {

	switch s.channelMode(channel) {
	case gen.CapabilityModeLive:
		enabled, err := store.CachedTenantChannels(ctx, s.DB, s.Hot, tenantID, channelNames())
		if err != nil {
			return sending.SendRefused, err
		}
		if enabled[channel] {
			return sending.SendLive, nil
		}
	case gen.CapabilityModeSandbox:
		if testMode {
			return sending.SendSimulated, nil
		}
	}
	return sending.SendRefused, nil
}

// channelUnavailable is the refusal a campaign create answers for a leg whose
// channel is not live for the tenant.
func channelUnavailable() gen.Error {
	return errorBody("channel_unavailable", "This channel is not live for your account yet. "+
		"Choose a live channel, or ask your account manager to enable it.")
}

// sandboxReasons say what is missing for a channel to go live, in words a
// customer can act on. Never a secret, never a configuration value.
var sandboxReasons = map[gen.ChannelId]string{
	gen.ChannelIdSMS:      "No operator connection is set up yet, so SMS can only be simulated",
	gen.ChannelIdRCS:      "No RCS carrier account is set up yet, so RCS can only be simulated",
	gen.ChannelIdWHATSAPP: "Connect a WhatsApp number to go live",
	gen.ChannelIdEMAIL:    "Verify a sending domain to go live",
}

type channelFeatures struct{ twoWay, deliveryReceipts, readReceipts bool }

var channelFeatureSet = map[gen.ChannelId]channelFeatures{
	gen.ChannelIdSMS:      {twoWay: true, deliveryReceipts: true},
	gen.ChannelIdRCS:      {twoWay: true, deliveryReceipts: true, readReceipts: true},
	gen.ChannelIdWHATSAPP: {twoWay: true, deliveryReceipts: true, readReceipts: true},
}

// channelCapability is one channel as a tenant sees it.
func (s *Server) channelCapability(channel gen.ChannelId, enabled bool) gen.ChannelCapability {
	mode := s.channelMode(string(channel))
	capability := gen.ChannelCapability{Channel: channel, Mode: mode, EnabledForTenant: &enabled}
	var reason string
	switch {
	case mode == gen.CapabilityModeDisabled:
		reason = "Not available yet"
	case mode == gen.CapabilityModeSandbox:
		reason = sandboxReasons[channel]
	case !enabled:
		reason = "Ask your account manager to enable this channel"
	}
	if reason != "" {
		capability.Reason = &reason
	}
	features := channelFeatureSet[channel]
	capability.Features = &struct {
		DeliveryReceipts *bool `json:"deliveryReceipts,omitempty"`
		ReadReceipts     *bool `json:"readReceipts,omitempty"`
		TwoWay           *bool `json:"twoWay,omitempty"`
	}{DeliveryReceipts: &features.deliveryReceipts, ReadReceipts: &features.readReceipts,
		TwoWay: &features.twoWay}
	return capability
}

// tenantCapabilities is every channel for one tenant.
func (s *Server) tenantCapabilities(ctx context.Context, tenantID uuid.UUID) (
	[]gen.ChannelCapability, error) {

	enabled, err := store.TenantChannels(ctx, s.DB, tenantID, channelNames())
	if err != nil {
		return nil, err
	}
	out := make([]gen.ChannelCapability, 0, len(channelOrder))
	for _, channel := range channelOrder {
		out = append(out, s.channelCapability(channel, enabled[string(channel)]))
	}
	return out, nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ gen.GetCapabilitiesRequestObject) (
	gen.GetCapabilitiesResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetCapabilities401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	channels, err := s.tenantCapabilities(ctx, identity.TenantID)
	if err != nil {
		return nil, err
	}
	return gen.GetCapabilities200JSONResponse{Channels: channels, Platform: s.platformFacilities()}, nil
}

// platformFacilities are the deployment-wide facilities a screen may offer.
func (s *Server) platformFacilities() *struct {
	LinkDomains *bool `json:"linkDomains,omitempty"`
	Lookup      *bool `json:"lookup,omitempty"`
} {
	lookup, linkDomains := false, false
	return &struct {
		LinkDomains *bool `json:"linkDomains,omitempty"`
		Lookup      *bool `json:"lookup,omitempty"`
	}{LinkDomains: &linkDomains, Lookup: &lookup}
}

// channelProvider names the vendor behind a live channel.
func (s *Server) channelProvider(channel gen.ChannelId) *string {
	var provider string
	switch channel {
	case gen.ChannelIdSMS:
		if s.channelMode("SMS") == gen.CapabilityModeLive {
			provider = "smpp"
		}
	case gen.ChannelIdRCS:
		if s.RCSCarrier != nil {
			provider = s.RCSCarrier.Vendor()
		} else if s.RCS != nil && !s.RCS.Empty() {
			provider = strings.ToLower(strings.Join(s.RCS.Carriers(), ", "))
		}
	}
	if provider == "" {
		return nil
	}
	return &provider
}

func (s *Server) GetOperatorChannels(ctx context.Context, _ gen.GetOperatorChannelsRequestObject) (
	gen.GetOperatorChannelsResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorChannels401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	checks, err := store.LastChannelChecks(ctx, s.operatorPool())
	if err != nil {
		return nil, err
	}
	out := make([]gen.OperatorChannel, 0, len(channelOrder))
	for _, channel := range channelOrder {
		row := gen.OperatorChannel{Channel: channel, Mode: s.channelMode(string(channel)),
			Provider: s.channelProvider(channel)}
		row.ConfigKeys = []struct {
			Name    string `json:"name"`
			Present bool   `json:"present"`
		}{}
		for _, name := range channelConfigKeys[channel] {
			row.ConfigKeys = append(row.ConfigKeys, struct {
				Name    string `json:"name"`
				Present bool   `json:"present"`
			}{Name: name, Present: s.ConfigKeys[name]})
		}
		if check, ran := checks[string(channel)]; ran {
			row.LastCheck = &struct {
				At     time.Time `json:"at"`
				Passed bool      `json:"passed"`
			}{At: check.CheckedAt, Passed: check.Passed}
		}
		out = append(out, row)
	}
	return gen.GetOperatorChannels200JSONResponse{Channels: out}, nil
}

// ---- per-tenant enablement ----------------------------------------------------

func (s *Server) mountChannelRoutes(r chi.Router) {
	r.Patch("/v1/operator/tenants/{id}/channels/{channel}", s.setTenantChannel)
}

// setTenantChannel switches a channel on or off for one tenant.
//
// Mounted by hand rather than through the generated binder, because the order
// of refusals is the contract: the tenant is looked up BEFORE the body is
// judged, so an unknown tenant is 404 whatever the body says. The binder
// decodes the body first and would answer a non-boolean value with 422 before
// this code ran.
func (s *Server) setTenantChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	operator, err := s.requireOperator(ctx)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Sign in to the operator console.")
		return
	}
	channel := gen.ChannelId(chi.URLParam(r, "channel"))
	if !channel.Valid() {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"channel must be one of SMS, RCS, WHATSAPP, EMAIL, VOICE.")
		return
	}
	tenantID, valid := parsePathID(chi.URLParam(r, "id"))
	if !valid {
		writeError(w, http.StatusNotFound, codeNotFound, "No such tenant.")
		return
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "No such tenant.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	enabled, problem := enabledFromBody(r)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	if err := store.SetTenantChannel(ctx, s.operatorPool(), tenantID, string(channel), enabled); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Hot.Forget(store.TenantChannelsKey(tenantID))
	action, verb := "tenant.channel_disable", "Disabled"
	if enabled {
		action, verb = "tenant.channel_enable", "Enabled"
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action, &tenantID,
		tenant.Name, string(channel), fmt.Sprintf("%s %s for %s", verb, channel, tenant.Name)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.channelCapability(channel, enabled))
}

// enabledFromBody reads {"enabledForTenant": true|false} and nothing else.
func enabledFromBody(r *http.Request) (bool, string) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return false, "The request body could not be read."
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return false, "The body must be a JSON object holding enabledForTenant."
	}
	for key := range body {
		if key != "enabledForTenant" {
			return false, fmt.Sprintf("Unknown field(s): %s.", key)
		}
	}
	var enabled bool
	value, present := body["enabledForTenant"]
	if !present || json.Unmarshal(value, &enabled) != nil {
		return false, "enabledForTenant is required and must be true or false."
	}
	return enabled, ""
}

// ---- go-live check --------------------------------------------------------------

// goLiveSteps are each channel's ordered, machine-checkable steps.
var goLiveSteps = map[gen.ChannelId][]string{
	gen.ChannelIdSMS: {"bind_present", "test_message_accepted", "delivery_receipt_received",
		"failure_classified"},
	gen.ChannelIdRCS: {"carrier_credentials_present", "capabilities_answer", "test_message_accepted"},
	gen.ChannelIdEMAIL: {"credentials_valid", "region_matches", "event_destination_reachable",
		"configuration_set_exists", "tenant_quota_available", "production_access_granted",
		"test_message_accepted", "delivery_event_received", "bounce_classified",
		"complaint_classified"},
	gen.ChannelIdWHATSAPP: {"app_credentials_present", "webhook_verify_token_ok",
		"webhook_signature_ok", "app_has_advanced_access", "embedded_signup_config_present",
		"token_exchange_ok", "number_registered", "webhooks_subscribed", "template_submitted",
		"template_approved", "test_message_delivered", "inbound_reply_opens_window"},
	gen.ChannelIdVOICE: {"channel_available"},
}

// goLiveStepKeys are the configuration keys a step needs present to pass.
var goLiveStepKeys = map[string][]string{
	"credentials_valid":              {"SES_CREDENTIALS"},
	"region_matches":                 {"SES_REGION"},
	"event_destination_reachable":    {"SES_EVENT_DESTINATION"},
	"configuration_set_exists":       {"SES_CONFIGURATION_SET_PREFIX"},
	"app_credentials_present":        {"META_APP_ID", "META_APP_SECRET"},
	"webhook_verify_token_ok":        {"META_WEBHOOK_VERIFY_TOKEN"},
	"webhook_signature_ok":           {"META_APP_SECRET"},
	"embedded_signup_config_present": {"META_EMBEDDED_SIGNUP_CONFIG_ID"},
}

// goLiveWindow is how far back the message log is read for evidence that a
// channel has really carried traffic.
const goLiveWindow = 7 * 24 * time.Hour

// goLiveRunning holds the channels whose check is in flight, so a second
// concurrent run answers 409 instead of racing the first.
var goLiveRunning sync.Map

func (s *Server) RunChannelGoLiveCheck(ctx context.Context, request gen.RunChannelGoLiveCheckRequestObject) (
	gen.RunChannelGoLiveCheckResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.RunChannelGoLiveCheck401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	channel := request.Channel
	if !channel.Valid() {
		return gen.RunChannelGoLiveCheck404JSONResponse(errorBody(codeNotFound, "No such channel.")), nil
	}
	if _, running := goLiveRunning.LoadOrStore(channel, true); running {
		return gen.RunChannelGoLiveCheck409JSONResponse(errorBody("conflict",
			"A check for this channel is already running.")), nil
	}
	defer goLiveRunning.Delete(channel)

	steps := make([]gen.GoLiveCheckStep, 0, len(goLiveSteps[channel]))
	blockedOn := ""
	for _, step := range goLiveSteps[channel] {
		if blockedOn != "" {
			reason := "Waiting on " + blockedOn
			steps = append(steps, gen.GoLiveCheckStep{Step: step, Status: gen.Skipped, Reason: &reason})
			continue
		}
		reason, err := s.goLiveStep(ctx, channel, step)
		if err != nil {
			return nil, err
		}
		if reason == "" {
			steps = append(steps, gen.GoLiveCheckStep{Step: step, Status: gen.Pass})
			continue
		}
		blockedOn = step
		steps = append(steps, gen.GoLiveCheckStep{Step: step, Status: gen.Fail, Reason: &reason})
	}
	result := gen.GoLiveCheckResult{Channel: channel, CheckedAt: s.now().UTC(),
		Passed: blockedOn == "", Steps: steps}
	if err := store.SaveChannelCheck(ctx, s.operatorPool(), string(channel),
		store.ChannelCheck{CheckedAt: result.CheckedAt, Passed: result.Passed}); err != nil {
		return nil, err
	}
	return gen.RunChannelGoLiveCheck200JSONResponse(result), nil
}

// goLiveStep runs one step and returns why it failed, or "" when it passed.
// The reasons name what is missing in plain words, never a value.
func (s *Server) goLiveStep(ctx context.Context, channel gen.ChannelId, step string) (string, error) {
	if keys, needsKeys := goLiveStepKeys[step]; needsKeys {
		var missing []string
		for _, key := range keys {
			if !s.ConfigKeys[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return "Missing configuration: " + strings.Join(missing, ", "), nil
		}
		return "", nil
	}
	switch step {
	case "bind_present":
		if s.SMPP == nil || len(s.SMPP.BoundIDs()) == 0 {
			return "No operator bind is up", nil
		}
		return "", nil
	case "carrier_credentials_present":
		if s.channelMode("RCS") != gen.CapabilityModeLive {
			return "No RCS carrier account is configured", nil
		}
		return "", nil
	case "capabilities_answer":
		if health := s.rcsGateway().Health(ctx); !health.Healthy {
			return "The RCS carrier did not answer", nil
		}
		return "", nil
	case "test_message_accepted", "delivery_receipt_received", "failure_classified":
		return s.goLiveEvidence(ctx, channel, step)
	case "channel_available":
		return "Not available yet", nil
	}
	// Every other step needs the channel to be live first; on a sandbox
	// channel there is nothing real to have checked.
	if s.channelMode(string(channel)) != gen.CapabilityModeLive {
		return "The channel is not live on this deployment", nil
	}
	return "", nil
}

// rcsGateway is the connector RCS sends through on this deployment.
func (s *Server) rcsGateway() connector.Connector {
	if carrier, sends := s.RCSCarrier.(connector.Connector); sends {
		return carrier
	}
	if s.RCS != nil {
		return s.RCS
	}
	return s.Connector
}

// goLiveEvidence reads the message log for proof a channel has carried real,
// non-simulated traffic in the last week.
func (s *Server) goLiveEvidence(ctx context.Context, channel gen.ChannelId, step string) (string, error) {
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return "The message log is not reachable", nil
	}
	evidence, err := store.QueryChannelEvidence(ctx, conn, string(channel), s.now().Add(-goLiveWindow))
	if err != nil {
		return "", err
	}
	switch step {
	case "test_message_accepted":
		if evidence.Accepted == 0 {
			return "No message was accepted by a carrier in the last 7 days. Send a test message", nil
		}
	case "delivery_receipt_received":
		if evidence.Receipted == 0 {
			return "No delivery receipt arrived in the last 7 days", nil
		}
	case "failure_classified":
		codes, err := store.FailedCodes(ctx, conn, nil)
		if err != nil {
			return "", err
		}
		rules, since, unexplained := s.failureRules(ctx), s.now().Add(-goLiveWindow), 0
		for _, code := range codes {
			question, failed := failures.ContextFor(code.Channel, code.Status, code.ErrorCode,
				code.RouteID, code.Carrier)
			if failed && code.Channel == string(channel) && code.LastSeen.After(since) &&
				failures.Resolve(rules, question).Unmapped() {
				unexplained += int(code.Count)
			}
		}
		if unexplained+int(evidence.Unclassified) > 0 {
			return fmt.Sprintf("%d failed message(s) in the last 7 days have no explanation. "+
				"Add rules from the unmapped queue", unexplained+int(evidence.Unclassified)), nil
		}
	}
	return "", nil
}
