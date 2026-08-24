package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ViRCS is Vi's capability API.
//
// Unlike Airtel's static credential, Vi issues a short-lived OAuth token from
// a separate host, and that token endpoint is itself rate limited to 60
// requests per minute per client. Minting one per capability check would
// exhaust the quota during any real audience check, so the token is cached
// here and the cache is not optional.
type ViRCS struct {
	// BaseURL is the API root, e.g. https://api.virbm.in — WITHOUT a trailing
	// /rcs. The Google-style capability path supplies its own /rcs segment and
	// the bulk path does not use one, so folding it into the base would break
	// whichever of the two came second.
	BaseURL string

	// TokenURL is the SSO endpoint, e.g.
	// https://auth.virbm.in/auth/oauth/token
	TokenURL string

	ClientID     string
	ClientSecret string

	// BotID is the registered bot, Vi's equivalent of Airtel's agentId.
	BotID string

	HTTP *http.Client

	mu        sync.Mutex
	token     string
	tokenTill time.Time
}

func (v *ViRCS) Vendor() string { return "vi" }

func (v *ViRCS) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (v *ViRCS) configured() bool {
	return v.BaseURL != "" && v.TokenURL != "" &&
		v.ClientID != "" && v.ClientSecret != "" && v.BotID != ""
}

func (v *ViRCS) Capability(ctx context.Context, msisdn string) (RCSCapability, error) {
	if !v.configured() {
		return RCSCapability{}, ErrRCSNotConfigured
	}

	// The Google-style endpoint (§3.5), not the GSMA one (§2.3): it returns the
	// same feature vocabulary Airtel does, so one Relay answer means one thing
	// regardless of which carrier served it. See rcs_capability.go.
	path := "/rcs/v1/phones/" + url.PathEscape(msisdn) + "/capabilities" +
		"?botId=" + url.QueryEscape(v.BotID)

	response, err := v.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return RCSCapability{}, err
	}
	defer response.Body.Close()

	// Vi answers "not RCS capable" with 200 and an empty object, and reserves
	// 404 for the GSMA endpoint. Decode first and let the absent features field
	// speak, rather than inferring reachability from the status line.
	if response.StatusCode == http.StatusNotFound {
		return RCSCapability{Msisdn: msisdn, Vendor: v.Vendor()}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RCSCapability{}, fmt.Errorf("vi rcs: http %d", response.StatusCode)
	}

	var body struct {
		Features []string `json:"features"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return RCSCapability{}, fmt.Errorf("vi rcs: unreadable body: %w", err)
	}

	return RCSCapability{
		Msisdn:    msisdn,
		Reachable: len(body.Features) > 0,
		Features:  body.Features,
		Vendor:    v.Vendor(),
	}, nil
}

func (v *ViRCS) Reachable(ctx context.Context, msisdns []string) ([]string, error) {
	if !v.configured() {
		return nil, ErrRCSNotConfigured
	}

	unique := dedupe(msisdns)
	if len(unique) == 0 {
		return nil, nil
	}
	if len(unique) > MaxRCSBulkNumbers {
		return nil, ErrRCSTooManyNumbers
	}

	// Vi's bulk check has a ceiling but no floor, so unlike Airtel it serves
	// small lists directly and there is no fan-out path here.
	payload, err := json.Marshal(map[string]any{"users": unique})
	if err != nil {
		return nil, err
	}

	response, err := v.do(ctx, http.MethodPost,
		"/bot/v1/"+url.PathEscape(v.BotID)+"/rcsEnabledContacts", payload)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("vi rcs: http %d", response.StatusCode)
	}

	var body struct {
		RCSEnabledContacts []string `json:"rcsEnabledContacts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("vi rcs: unreadable body: %w", err)
	}
	return body.RCSEnabledContacts, nil
}

func (v *ViRCS) do(ctx context.Context, method, path string, payload []byte) (*http.Response, error) {
	token, err := v.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	var reader *bytes.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	request, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(v.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := v.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("vi rcs: %w", err)
	}

	// A token can expire between the cache check and the call, and a rejected
	// one must not become a permanent failure: drop it so the next request
	// mints a fresh one rather than replaying the dead credential forever.
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		v.mu.Lock()
		if v.token == token {
			v.token, v.tokenTill = "", time.Time{}
		}
		v.mu.Unlock()
		return nil, fmt.Errorf("vi rcs: unauthorized")
	}
	return response, nil
}

// accessToken returns a cached token, minting one only when it is missing or
// about to expire.
//
// The lock is held across the network call on purpose. Releasing it would let
// every concurrent capability check mint its own token on a cold cache, and
// with a 60-per-minute ceiling on that endpoint a single audience check would
// lock the tenant out of RCS for a minute. Serialising the refresh costs one
// request's latency once per token lifetime.
func (v *ViRCS) accessToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Refresh a minute early. A token that expires in flight fails the request
	// that carried it, and the retry costs more than the minute given up here.
	if v.token != "" && time.Now().Before(v.tokenTill.Add(-time.Minute)) {
		return v.token, nil
	}

	tokenURL := v.TokenURL
	if !strings.Contains(tokenURL, "grant_type=") {
		separator := "?"
		if strings.Contains(tokenURL, "?") {
			separator = "&"
		}
		tokenURL += separator + "grant_type=client_credentials"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return "", err
	}
	// Vi wants clientId:secret base64-encoded as Basic auth on the token call —
	// not as form fields in the body.
	credential := base64.StdEncoding.EncodeToString([]byte(v.ClientID + ":" + v.ClientSecret))
	request.Header.Set("Authorization", "Basic "+credential)

	response, err := v.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("vi rcs: token: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("vi rcs: token: http %d", response.StatusCode)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("vi rcs: token: unreadable body: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("vi rcs: token: response carried no access_token")
	}

	// Vi does not document expires_in. Standard OAuth sends it; if this one
	// does not, five minutes is short enough to stay well inside any real
	// lifetime and long enough to keep the 60-per-minute quota untouched.
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 5 * time.Minute
	}
	v.token = body.AccessToken
	v.tokenTill = time.Now().Add(lifetime)
	return v.token, nil
}

func (v *ViRCS) Name() string { return v.Vendor() }

func (v *ViRCS) Health(context.Context) Health {
	if !v.configured() {
		return Health{Healthy: false, Detail: "vi rcs: credentials are incomplete"}
	}
	// No live call: Vi's token endpoint allows 60 requests a minute for the
	// whole account, and a health probe is exactly the kind of caller that
	// would spend that budget on nothing.
	return Health{Healthy: true, Detail: "vi rcs: configured"}
}

// RegisterTemplate always refuses.
//
// Vi has no template API at all: templates are created in the Vi RBM portal and
// approved by a Vi admin, and the brand is handed a template code afterwards.
// This is implemented rather than left off the type so the product can say that
// clearly in one place — a customer on Vi needs to be told where to go, not
// shown a failure that looks like an outage.
func (v *ViRCS) RegisterTemplate(context.Context, RCSTemplateSpec) (RCSTemplateRegistration, error) {
	return RCSTemplateRegistration{}, ErrTemplateRegistrationManual
}

// TemplateStatus refuses for the same reason. Vi publishes no way to read a
// template's approval state, so the only source of truth is the portal.
func (v *ViRCS) TemplateStatus(context.Context, string) (RCSTemplateRegistration, error) {
	return RCSTemplateRegistration{}, ErrTemplateRegistrationManual
}

func (v *ViRCS) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	if !v.configured() {
		return nil, ErrRCSNotConfigured
	}
	return submitEach(ctx, v.submitOne, submissions)
}

func (v *ViRCS) submitOne(ctx context.Context, submission Submission) Receipt {
	if submission.CarrierTemplateID == "" {
		// "only template messages from the brand will be allowed outside of a
		// conversation" — §3.2. A free-form send would be refused, and paying
		// for the round trip to learn that helps nobody.
		return Receipt{
			MessageID: submission.MessageID,
			Accepted:  false,
			ErrorCode: "template_not_registered",
		}
	}

	message := map[string]any{
		"templateCode": submission.CarrierTemplateID,
	}
	// Vi's placeholders are named ([DISCOUNT]) and customParams is a JSON
	// STRING, not an object — a nested object is rejected. Encoded here rather
	// than at the caller so the quoting lives next to the reason for it.
	if len(submission.TemplateVariables) > 0 {
		named := make(map[string]string, len(submission.TemplateVariables))
		for _, variable := range submission.TemplateVariables {
			named[variable.Name] = variable.Value
		}
		encoded, err := json.Marshal(named)
		if err != nil {
			return Receipt{MessageID: submission.MessageID, Accepted: false,
				ErrorCode: "template_variables_invalid"}
		}
		message["customParams"] = string(encoded)
	}

	payload := map[string]any{
		"contentMessage": map[string]any{"templateMessage": message},
	}
	// Vi wants a duration string ending in 's', not a number. "120" is rejected
	// where "120s" is accepted.
	if submission.TTLSeconds > 0 {
		payload["ttl"] = strconv.Itoa(submission.TTLSeconds) + "s"
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return Receipt{MessageID: submission.MessageID, Accepted: false,
			ErrorCode: "carrier_rejected"}
	}

	// Relay's own message id doubles as Vi's messageId. Vi rejects a duplicate,
	// which turns an accidental re-send of the same message into a refusal
	// rather than a second charge and a second handset notification.
	query := url.Values{}
	query.Set("botId", v.BotID)
	query.Set("messageId", submission.MessageID)
	path := "/rcs/v1/phones/" + url.PathEscape(submission.Msisdn) +
		"/agentMessages/async?" + query.Encode()

	response, err := v.do(ctx, http.MethodPost, path, encoded)
	if err != nil {
		return Receipt{MessageID: submission.MessageID, Accepted: false,
			ErrorCode: "carrier_unreachable"}
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return Receipt{MessageID: submission.MessageID, Accepted: false,
			ErrorCode: "carrier_throttled"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Receipt{MessageID: submission.MessageID, Accepted: false,
			ErrorCode: viSendErrorCode(response)}
	}

	// Vi answers a successful async submit with a pending event. It carries no
	// carrier reference of its own, so the message id we supplied IS the
	// correlation key — and it is what the delivery webhook will quote back.
	return Receipt{
		MessageID:  submission.MessageID,
		Accepted:   true,
		CarrierRef: submission.MessageID,
	}
}

// viSendErrorCode reads Vi's failure body far enough to separate the cases a
// customer can act on from the ones they cannot.
func viSendErrorCode(response *http.Response) string {
	var body struct {
		Reason string `json:"reason"`
		Error  struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	// A body we cannot read is still a rejection; the status line already told
	// us that much.
	_ = json.NewDecoder(response.Body).Decode(&body)

	text := body.Reason + " " + body.Error.Message + " " + body.Error.Status
	switch {
	case strings.Contains(text, "template"), strings.Contains(text, "Template"):
		return "template_not_approved"
	case strings.Contains(text, "NOT_FOUND"), response.StatusCode == http.StatusNotFound:
		return "unreachable_handset"
	case response.StatusCode == http.StatusUnauthorized:
		return "carrier_unauthorized"
	case response.StatusCode >= 500:
		return "carrier_unavailable"
	default:
		return "carrier_rejected"
	}
}
