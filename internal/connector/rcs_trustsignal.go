package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TrustsignalRCS is Trustsignal's RCS API, which Sigmo (sigmo.ai) resells
// under its own name. Unlike the other four it is an aggregator, not a
// network: one account reaches every Indian operator, so its launches and
// routes are recorded under TRUSTSIGNAL rather than under AIRTEL or JIO.
//
// Three things set it apart:
//
//   - The API key travels in the query string (?api_key=). Every error built
//     here drops the request URL, or the key would reach the logs.
//   - A send names only a template. The template is bound to a bot when it is
//     created, so the bot never travels on a send.
//   - Placeholders are named and bracketed, [NAME], and filled from a JSON
//     object of the same names, like Vi.
//
// There is no capability lookup. Reachable answers ErrRCSNoCapabilityLookup,
// which the send path already reads as "unknown, keep the page on RCS".
type TrustsignalRCS struct {
	// BaseURL is the API root, https://rcsapi.trustsignal.io.
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// ErrRCSNoCapabilityLookup is a carrier that cannot say whether a handset
// takes RCS before a send.
var ErrRCSNoCapabilityLookup = errors.New("connector: this carrier has no RCS capability lookup")

func (t *TrustsignalRCS) Vendor() string { return "trustsignal" }
func (t *TrustsignalRCS) Name() string   { return t.Vendor() }

func (t *TrustsignalRCS) configured() bool { return t.BaseURL != "" && t.APIKey != "" }

func (t *TrustsignalRCS) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (t *TrustsignalRCS) Health(context.Context) Health {
	if !t.configured() {
		return Health{Detail: "trustsignal rcs: api key or base url missing"}
	}
	return Health{Healthy: true, Detail: "trustsignal rcs: configured"}
}

func (t *TrustsignalRCS) Capability(context.Context, string, string) (RCSCapability, error) {
	return RCSCapability{}, ErrRCSNoCapabilityLookup
}

func (t *TrustsignalRCS) Reachable(context.Context, string, []string) ([]string, error) {
	return nil, ErrRCSNoCapabilityLookup
}

// trustsignalReply is the envelope every call answers with. A refusal can come
// back as 200 with success false, so the status line alone proves nothing.
type trustsignalReply struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Errors  []struct {
		Code    string `json:"code"`
		CodeMsg string `json:"codeMsg"`
		Message string `json:"message"`
	} `json:"errors"`
	Results *struct {
		TransactionID string `json:"transaction_id"`
	} `json:"results"`
	Template  *trustsignalTemplate  `json:"template"`
	Templates []trustsignalTemplate `json:"templates"`
}

type trustsignalTemplate struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// refusal is the carrier's own words for a failed call, codeMsg first.
func (r trustsignalReply) refusal() string {
	parts := []string{}
	for _, e := range r.Errors {
		parts = append(parts, strings.TrimSpace(e.CodeMsg+" "+e.Message))
	}
	if len(parts) == 0 && r.Message != "" {
		parts = append(parts, r.Message)
	}
	return strings.Join(parts, "; ")
}

// call sends one request and reads the envelope. The error never carries the
// URL: net/http quotes it in full, api key included.
func (t *TrustsignalRCS) call(ctx context.Context, method, path string, query url.Values,
	body any) (trustsignalReply, int, error) {

	if query == nil {
		query = url.Values{}
	}
	query.Set("api_key", t.APIKey)
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return trustsignalReply{}, 0, err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(t.BaseURL, "/")+path+"?"+query.Encode(), reader)
	if err != nil {
		return trustsignalReply{}, 0, errors.New("trustsignal rcs: could not build the request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client().Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return trustsignalReply{}, 0, fmt.Errorf("trustsignal rcs: %w", err)
	}
	defer response.Body.Close()
	var reply trustsignalReply
	// An unreadable body is still an answer; the status line says which.
	_ = json.NewDecoder(response.Body).Decode(&reply)
	return reply, response.StatusCode, nil
}

func (t *TrustsignalRCS) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	if !t.configured() {
		return nil, ErrRCSNotConfigured
	}
	return submitEach(ctx, t.submitOne, submissions)
}

func (t *TrustsignalRCS) submitOne(ctx context.Context, submission Submission) Receipt {
	refused := func(code string) Receipt {
		return Receipt{MessageID: submission.MessageID, ErrorCode: code}
	}
	if submission.CarrierTemplateID == "" {
		return refused("template_not_registered")
	}

	to := submission.Msisdn
	if !strings.HasPrefix(to, "+") {
		to = "+" + to
	}
	// ponytail: no sms_fallback block. Relay runs its own fallback leg, and a
	// second one here would send the SMS twice. The docs list sms_fallback as
	// required; if the live API refuses without it, the refusal lands in
	// errorCode and this is where it goes.
	payload := map[string]any{"to": to, "template_id": submission.CarrierTemplateID}
	if len(submission.TemplateVariables) > 0 {
		named := make(map[string]string, len(submission.TemplateVariables))
		for _, variable := range submission.TemplateVariables {
			named[variable.Name] = variable.Value
		}
		payload["rcs_variables"] = named
	}
	if submission.TTLSeconds > 0 {
		payload["ttl"] = strconv.Itoa(submission.TTLSeconds) + "s"
	}

	reply, status, err := t.call(ctx, http.MethodPost, "/api/v1/rcs/with_fallback", nil, payload)
	switch {
	case err != nil:
		return refused("carrier_unreachable")
	case status == http.StatusTooManyRequests:
		return refused("carrier_throttled")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return refused("carrier_unauthorized")
	case status >= 500:
		return refused("carrier_unavailable")
	case !reply.Success || reply.Results == nil || reply.Results.TransactionID == "":
		return refused(trustsignalErrorCode(reply))
	}
	// transaction_id is what every delivery webhook quotes back.
	return Receipt{MessageID: submission.MessageID, Accepted: true,
		CarrierRef: reply.Results.TransactionID}
}

// trustsignalErrorCode separates what a customer can act on from the rest.
func trustsignalErrorCode(reply trustsignalReply) string {
	text := strings.ToUpper(reply.refusal())
	switch {
	case strings.Contains(text, "API_KEY"):
		return "carrier_unauthorized"
	case strings.Contains(text, "TEMPLATE"):
		return "template_not_approved"
	case strings.Contains(text, "CREDIT"), strings.Contains(text, "BALANCE"):
		return "carrier_account_unfunded"
	default:
		return "carrier_rejected"
	}
}

// RenderTrustsignalText turns Relay's {{named}} tokens into Trustsignal's
// [named]. The send fills them by the same names, so no order is involved.
func RenderTrustsignalText(text string) string {
	return namedTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		return "[" + strings.TrimSpace(token[2:len(token)-2]) + "]"
	})
}

func trustsignalSuggestions(suggestions []RCSSuggestion) []map[string]any {
	out := make([]map[string]any, 0, len(suggestions))
	for i, s := range suggestions {
		postback := fmt.Sprintf("%s_%d", s.Type, i+1)
		switch s.Type {
		case "open_url":
			out = append(out, map[string]any{"suggestionType": "url_action",
				"displayText": s.Text, "url": s.URL, "postback": postback,
				"application": "browser"})
		case "dial":
			out = append(out, map[string]any{"suggestionType": "dialer_action",
				"displayText": s.Text, "phoneNumber": s.PhoneNumber, "postback": postback})
		default:
			out = append(out, map[string]any{"suggestionType": "reply",
				"displayText": s.Text, "postback": postback})
		}
	}
	return out
}

// RegisterTemplate creates the template under the bot, as text or as a single
// rich card. Review happens at Trustsignal; the outcome arrives on the
// webhook and can be read back with TemplateStatus.
func (t *TrustsignalRCS) RegisterTemplate(ctx context.Context, agentID string,
	spec RCSTemplateSpec) (RCSTemplateRegistration, error) {

	if !t.configured() {
		return RCSTemplateRegistration{}, ErrRCSNotConfigured
	}
	if agentID == "" {
		return RCSTemplateRegistration{}, ErrRCSNoAgent
	}
	if strings.TrimSpace(spec.Name) == "" {
		return RCSTemplateRegistration{}, errors.New("the template needs a name")
	}

	shape := "1"
	body := map[string]any{"name": spec.Name, "botId": agentID,
		"suggestions": trustsignalSuggestions(spec.Suggestions)}
	if spec.Card != nil {
		shape = "2"
		body = map[string]any{"name": spec.Name, "type": "rich_card", "botId": agentID,
			"orientation": "VERTICAL", "height": "MEDIUM_HEIGHT",
			"standAlone": map[string]any{
				"cardTitle":       RenderTrustsignalText(spec.Card.Title),
				"cardDescription": RenderTrustsignalText(spec.Card.Description),
				"mediaUrl":        spec.Card.MediaURL,
				"suggestions":     trustsignalSuggestions(spec.Suggestions),
			}}
	} else {
		if strings.TrimSpace(spec.Text) == "" {
			return RCSTemplateRegistration{}, errors.New("the template body is empty")
		}
		body["type"] = "text_message"
		body["textMessageContent"] = RenderTrustsignalText(spec.Text)
	}

	reply, status, err := t.call(ctx, http.MethodPost, "/api/v1/template",
		url.Values{"s": {shape}}, body)
	if err != nil {
		return RCSTemplateRegistration{}, err
	}
	if status >= 500 || status == http.StatusUnauthorized || status == http.StatusTooManyRequests {
		return RCSTemplateRegistration{}, fmt.Errorf("trustsignal rcs: http %d %s", status, reply.refusal())
	}
	if !reply.Success || reply.Template == nil || reply.Template.ID == "" {
		// Their words, unprefixed: a content refusal is the customer's to fix.
		reason := reply.refusal()
		if reason == "" {
			reason = fmt.Sprintf("http %d", status)
		}
		return RCSTemplateRegistration{}, errors.New("the carrier refused the template: " + reason)
	}
	return RCSTemplateRegistration{CarrierTemplateID: reply.Template.ID,
		Status: trustsignalTemplateStatus(reply.Template.Status)}, nil
}

func (t *TrustsignalRCS) TemplateStatus(ctx context.Context, _ string,
	carrierTemplateID string) (RCSTemplateRegistration, error) {

	if !t.configured() {
		return RCSTemplateRegistration{}, ErrRCSNotConfigured
	}
	reply, status, err := t.call(ctx, http.MethodGet,
		"/api/v1/template/"+url.PathEscape(carrierTemplateID), nil, nil)
	if err != nil {
		return RCSTemplateRegistration{}, err
	}
	if status < 200 || status >= 300 || !reply.Success || len(reply.Templates) == 0 {
		return RCSTemplateRegistration{}, fmt.Errorf("trustsignal rcs: template %s: http %d %s",
			carrierTemplateID, status, reply.refusal())
	}
	found := reply.Templates[0]
	registration := RCSTemplateRegistration{CarrierTemplateID: found.ID,
		Status: trustsignalTemplateStatus(found.Status)}
	if registration.Status == RCSTemplateRejected {
		registration.RejectionReason = found.Error
	}
	return registration, nil
}

// trustsignalTemplateStatus maps their word to Relay's. "active" is their
// approved; anything unknown stays pending, never approved.
func trustsignalTemplateStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "approved":
		return RCSTemplateApproved
	case "rejected", "failed":
		return RCSTemplateRejected
	default:
		return RCSTemplatePending
	}
}

var _ RCSGateway = (*TrustsignalRCS)(nil)
var _ RCSTemplateRegistrar = (*TrustsignalRCS)(nil)
