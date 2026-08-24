package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AirtelRCS is Airtel IQ's capability API.
//
// Authentication is a static Basic credential that never expires and is reset
// by emailing their support desk, so there is no token lifecycle here — the
// whole of it is one header.
type AirtelRCS struct {
	// BaseURL is the gateway root, e.g.
	// https://iqconversation.airtel.in/gateway/airtel-xchange
	BaseURL string

	// AuthToken is the base64 blob Airtel issues, sent verbatim after "Basic ".
	// It is NOT a username:password pair we encode ourselves — Airtel hands
	// over the encoded value.
	AuthToken string

	// AgentID is the registered RBM agent. Airtel rejects an empty one, and a
	// capability answer is agent-specific: the same handset is reachable for a
	// launched agent and unreachable for one still in test.
	AgentID string

	// CustomerID and SubAccountID identify the Airtel IQ account the agent
	// hangs off. The capability endpoints do not want them; template
	// registration and send both reject the request without them, with
	// "Mandatory Request Parameter(s) cannot be null!".
	CustomerID   string
	SubAccountID string

	HTTP *http.Client
}

// AirtelBulkMinimum is Airtel's floor for the bulk endpoint. It is not a
// rounding: a list of 499 numbers is refused outright with
// "Users list must contain between 500 and 10000 unique numbers!".
const AirtelBulkMinimum = 500

func (a *AirtelRCS) Vendor() string { return "airtel" }

func (a *AirtelRCS) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// airtelEnvelope is the shape every Airtel response arrives in, success or not.
type airtelEnvelope struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Message string `json:"message"`

	Features       []string `json:"features"`
	ReachableUsers []string `json:"reachableUsers"`

	// Template registration and fetch both answer with this.
	RCSTemplate *struct {
		TemplateID      string `json:"templateId"`
		TemplateStatus  string `json:"templateStatus"`
		RejectionReason string `json:"rejectionReason"`
	} `json:"rcsTemplate"`

	// Send answers with this and a status of INITIATED.
	MessageRequestID string `json:"messageRequestId"`
	Status           string `json:"status"`
}

func (a *AirtelRCS) Capability(ctx context.Context, msisdn string) (RCSCapability, error) {
	if a.BaseURL == "" || a.AuthToken == "" || a.AgentID == "" {
		return RCSCapability{}, ErrRCSNotConfigured
	}

	body := map[string]string{"phoneNumber": msisdn, "agentId": a.AgentID}
	envelope, err := a.call(ctx, http.MethodGet, "/rcs-content-manager/v1/rcs/capabilities", nil, body)
	if err != nil {
		// Airtel reports "this handset cannot receive RCS from this agent" as a
		// validation failure carrying a wrapped Google 404, not as a successful
		// empty answer. Treating that as an error would make every unreachable
		// number look like an outage, and would fail a whole audience check on
		// its first non-RCS handset.
		if isAirtelUnreachable(err) {
			return RCSCapability{Msisdn: msisdn, Vendor: a.Vendor()}, nil
		}
		return RCSCapability{}, err
	}

	return RCSCapability{
		Msisdn:    msisdn,
		Reachable: true,
		Features:  envelope.Features,
		Vendor:    a.Vendor(),
	}, nil
}

func (a *AirtelRCS) Reachable(ctx context.Context, msisdns []string) ([]string, error) {
	if a.BaseURL == "" || a.AuthToken == "" || a.AgentID == "" {
		return nil, ErrRCSNotConfigured
	}

	unique := dedupe(msisdns)
	switch {
	case len(unique) == 0:
		return nil, nil

	// Under Airtel's floor the bulk endpoint is not merely inefficient, it
	// refuses the request. Asking one at a time is the only way to answer at
	// all, and a list this small is the interactive case anyway — someone
	// pasting a handful of numbers into a screen, not a 30,000-contact
	// audience.
	case len(unique) < AirtelBulkMinimum:
		return checkEach(ctx, a.Capability, unique)

	case len(unique) > MaxRCSBulkNumbers:
		return nil, ErrRCSTooManyNumbers
	}

	body := map[string]any{"agentId": a.AgentID, "users": unique}
	envelope, err := a.call(ctx, http.MethodGet, "/rcs-content-manager/v1/rcs/users/reachability", nil, body)
	if err != nil {
		return nil, err
	}
	return envelope.ReachableUsers, nil
}

// call issues one Airtel request and unwraps their envelope.
//
// Both capability endpoints are documented as GET carrying a JSON body, which
// is unusual and deliberate on their side — it is transcribed from their curl
// examples, not a mistake here. net/http sends it; some proxies will not, and
// that is worth knowing if this ever fails in an environment where it worked
// locally. Template registration and send are ordinary POSTs.
func (a *AirtelRCS) call(ctx context.Context, method, path string, query url.Values, payload any) (airtelEnvelope, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return airtelEnvelope{}, err
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := strings.TrimRight(a.BaseURL, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return airtelEnvelope{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Basic "+a.AuthToken)

	response, err := a.client().Do(request)
	if err != nil {
		return airtelEnvelope{}, fmt.Errorf("airtel rcs: %w", err)
	}
	defer response.Body.Close()

	// 429 is the one status worth naming. Airtel allows 40 TPS per customer id
	// by default, and a caller that cannot tell throttling from a malformed
	// request will retry the wrong things and back off from the right ones.
	if response.StatusCode == http.StatusTooManyRequests {
		return airtelEnvelope{}, ErrRCSThrottled
	}

	var envelope airtelEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return airtelEnvelope{}, fmt.Errorf("airtel rcs: http %d, unreadable body: %w",
			response.StatusCode, err)
	}

	// success is authoritative, not the HTTP status: Airtel's documented error
	// bodies carry code 400 in JSON, and relying on the transport status alone
	// would let one through as an empty success.
	if !envelope.Success {
		return airtelEnvelope{}, fmt.Errorf("airtel rcs: %s", envelope.Message)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return airtelEnvelope{}, fmt.Errorf("airtel rcs: http %d", response.StatusCode)
	}
	return envelope, nil
}

// isAirtelUnreachable recognises the wrapped Google NOT_FOUND that Airtel
// returns for a handset their agent cannot reach — either the handset has no
// RCS, or the agent has not launched on that subscriber's carrier.
//
// Matching on the message text is not something to be proud of. Airtel gives
// no distinguishing code for it: the envelope says 400 and "Validation Error"
// for a malformed number and for an unreachable one alike, and the only thing
// separating them is the Google error they pass through verbatim. The
// alternative is reporting every non-RCS handset as a system failure, which is
// worse. Revisit if Airtel ever gives this its own code.
func isAirtelUnreachable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "404 Not Found") ||
		strings.Contains(message, "NOT_FOUND") ||
		strings.Contains(message, "has not launched on the user's carrier")
}

// Name is the Connector interface's word for what Vendor() already answers.
// Kept as a one-line delegation rather than a second literal so the two can
// never drift.
func (a *AirtelRCS) Name() string { return a.Vendor() }

func (a *AirtelRCS) Health(ctx context.Context) Health {
	if !a.sendConfigured() {
		return Health{Healthy: false, Detail: "airtel rcs: credentials are incomplete"}
	}
	// Deliberately not a live call. Airtel publishes no health endpoint, and
	// spending a capability check against a real handset every time something
	// asks whether the process is up would bill for a status page.
	return Health{Healthy: true, Detail: "airtel rcs: configured"}
}

// sendConfigured is stricter than the capability check's own guard: templates
// and sends need the account identifiers that capability discovery does not.
func (a *AirtelRCS) sendConfigured() bool {
	return a.BaseURL != "" && a.AuthToken != "" && a.AgentID != "" &&
		a.CustomerID != "" && a.SubAccountID != ""
}

// account is the four identifiers every non-capability Airtel call repeats.
func (a *AirtelRCS) account() map[string]any {
	return map[string]any{
		"customerId":   a.CustomerID,
		"subAccountId": a.SubAccountID,
		"agentId":      a.AgentID,
	}
}

func (a *AirtelRCS) RegisterTemplate(ctx context.Context, spec RCSTemplateSpec) (RCSTemplateRegistration, error) {
	if !a.sendConfigured() {
		return RCSTemplateRegistration{}, ErrRCSNotConfigured
	}
	// Checked here rather than only at the API layer so a caller that reaches
	// this connector any other way still cannot spend a 24-hour review on
	// something countable.
	if err := ValidateAirtelTemplate(spec); err != nil {
		return RCSTemplateRegistration{}, err
	}

	body := a.account()
	body["templateName"] = spec.Name
	// Only the text shape is modelled. Submitting a card as TEXT would have
	// Airtel store and approve something that is not the template Relay holds.
	body["templateCategory"] = "TEXT"
	body["templateUseCase"] = spec.UseCase
	body["createdBy"] = spec.SubmittedBy
	body["updatedBy"] = spec.SubmittedBy
	body["templateData"] = map[string]any{
		"text": RenderAirtelText(spec.Text, spec.Variables),
	}
	if spec.TTL > 0 {
		body["ttl"] = spec.TTL
	}

	envelope, err := a.call(ctx, http.MethodPost,
		"/rcs-content-manager/v1/rcs/template", nil, body)
	if err != nil {
		return RCSTemplateRegistration{}, err
	}
	if envelope.RCSTemplate == nil || envelope.RCSTemplate.TemplateID == "" {
		// Without an id there is nothing to send against and nothing for the
		// approval webhook to match on later, so a "success" that omits it is
		// a failure however Airtel labelled it.
		return RCSTemplateRegistration{}, fmt.Errorf(
			"airtel rcs: template accepted but no templateId returned")
	}
	return RCSTemplateRegistration{
		CarrierTemplateID: envelope.RCSTemplate.TemplateID,
		Status:            normaliseCarrierTemplateStatus(envelope.RCSTemplate.TemplateStatus),
		RejectionReason:   envelope.RCSTemplate.RejectionReason,
	}, nil
}

func (a *AirtelRCS) TemplateStatus(ctx context.Context, carrierTemplateID string) (RCSTemplateRegistration, error) {
	if !a.sendConfigured() {
		return RCSTemplateRegistration{}, ErrRCSNotConfigured
	}

	query := url.Values{}
	query.Set("customerId", a.CustomerID)
	query.Set("subAccountId", a.SubAccountID)
	query.Set("agentId", a.AgentID)
	query.Set("templateId", carrierTemplateID)

	envelope, err := a.call(ctx, http.MethodGet,
		"/rcs-content-manager/v1/rcs/template", query, nil)
	if err != nil {
		return RCSTemplateRegistration{}, err
	}
	if envelope.RCSTemplate == nil {
		return RCSTemplateRegistration{}, fmt.Errorf(
			"airtel rcs: no template returned for %s", carrierTemplateID)
	}
	return RCSTemplateRegistration{
		CarrierTemplateID: envelope.RCSTemplate.TemplateID,
		Status:            normaliseCarrierTemplateStatus(envelope.RCSTemplate.TemplateStatus),
		RejectionReason:   envelope.RCSTemplate.RejectionReason,
	}, nil
}

// Submit sends one message per submission — Airtel documents no batch send
// endpoint, so the batching in the Connector interface is satisfied here rather
// than on the wire.
//
// Concurrency is capped well under the default 40 TPS. Exceeding it earns a 429
// for the whole customer id, which would take down every tenant's sending on
// this deployment, not just the campaign that caused it.
func (a *AirtelRCS) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	if !a.sendConfigured() {
		return nil, ErrRCSNotConfigured
	}
	return submitEach(ctx, a.submitOne, submissions)
}

func (a *AirtelRCS) submitOne(ctx context.Context, submission Submission) Receipt {
	if submission.CarrierTemplateID == "" {
		// Airtel refuses a free-form send outside an open conversation, so this
		// is not a case worth attempting and discovering at the gateway.
		return Receipt{
			MessageID: submission.MessageID,
			Accepted:  false,
			ErrorCode: "template_not_registered",
		}
	}

	body := a.account()
	body["msisdn"] = submission.Msisdn
	body["templateId"] = submission.CarrierTemplateID
	// Airtel wants the values alone, in the template's own order. The names
	// travel with them only so Vi can use the same submission.
	if len(submission.TemplateVariables) > 0 {
		values := make([]string, 0, len(submission.TemplateVariables))
		for _, variable := range submission.TemplateVariables {
			values = append(values, variable.Value)
		}
		body["templateVariableData"] = map[string]any{"textVariables": values}
	}
	if submission.TTLSeconds > 0 {
		body["ttl"] = submission.TTLSeconds
	}

	envelope, err := a.call(ctx, http.MethodPost,
		"/conversation-message-acceptor/v1/rcs/message/send", nil, body)
	if err != nil {
		return Receipt{
			MessageID: submission.MessageID,
			Accepted:  false,
			ErrorCode: airtelSendErrorCode(err),
		}
	}
	// INITIATED means Airtel took it, not that a handset received it. Delivery
	// arrives later on the webhook, which is why this sets no delivered flag.
	return Receipt{
		MessageID:  submission.MessageID,
		Accepted:   true,
		CarrierRef: envelope.MessageRequestID,
	}
}

// airtelSendErrorCode turns Airtel's prose into something a ledger can hold and
// a screen can group by.
//
// Their send-time failures are documented as sentences, not codes — "Template
// is not active/approved!!", "Agent is not launched yet." — so this is text
// matching, with the same caveat as isAirtelUnreachable. The fallback is
// deliberately generic rather than the carrier's sentence: the raw message
// quotes the agent id, and error codes end up on tenant-visible screens.
func airtelSendErrorCode(err error) string {
	message := err.Error()
	switch {
	case errors.Is(err, ErrRCSThrottled):
		return "carrier_throttled"
	case strings.Contains(message, "Template not found"),
		strings.Contains(message, "Template is not active"):
		return "template_not_approved"
	case strings.Contains(message, "Agent is not launched"):
		return "agent_not_launched"
	case strings.Contains(message, "RCS is not enabled"):
		return "rcs_not_enabled"
	case strings.Contains(message, "Invalid Phone Number"),
		strings.Contains(message, "Phone Number Parse Error"):
		return "invalid_recipient"
	case strings.Contains(message, "Free Flow Messages have been limited"):
		return "conversation_required"
	case strings.Contains(message, "variable"), strings.Contains(message, "Variable"):
		return "template_variables_invalid"
	default:
		return "carrier_rejected"
	}
}
