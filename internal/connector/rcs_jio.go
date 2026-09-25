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
	"sync"
	"time"

	"github.com/google/uuid"
)

// JioRCS is Jio Business Messaging (JBM), from the "User and API Integration
// Guide" v2.3.
//
// The part that differs from every other carrier here is WHOSE credential it
// is. Airtel and Vi issue one login for the whole account and take the agent
// per call. Jio issues a client id and secret per ASSISTANT — per customer
// brand — and the token it mints speaks for that one assistant. So the secrets
// live in a map keyed by assistant id, the same id stored as the launch's
// carrier_agent_id, and tokens are cached per assistant.
//
// Jio reviews assistants, not messages: there is no template API, and a plain
// text send needs no template id. That makes it Google-shaped rather than
// Airtel- or Vi-shaped, and the message text travels in Body.
type JioRCS struct {
	// TokenURL is the OAuth host, e.g. https://tgs.businessmessaging.jio.com
	TokenURL string
	// BaseURL is the messaging host, e.g. https://api.businessmessaging.jio.com
	BaseURL string

	// Assistants maps an assistant id to its secret key.
	Assistants map[string]string

	HTTP *http.Client

	mu     sync.Mutex
	tokens map[string]jioToken
}

type jioToken struct {
	value string
	till  time.Time
}

// JioBatchMinimum is the floor on Jio's batch capability endpoint: "to be
// called for MSISDNs within the range of 500 to 10000 per request".
const JioBatchMinimum = 500

func (j *JioRCS) Vendor() string { return "jio" }
func (j *JioRCS) Name() string   { return j.Vendor() }

// TemplatesReviewed is false: Jio approves the assistant and sends the text it
// is given.
func (j *JioRCS) TemplatesReviewed() bool { return false }

func (j *JioRCS) client() *http.Client {
	if j.HTTP != nil {
		return j.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (j *JioRCS) configured() bool {
	return j.TokenURL != "" && j.BaseURL != ""
}

func (j *JioRCS) Health(context.Context) Health {
	switch {
	case !j.configured():
		return Health{Detail: "jio rcs: token or messaging URL is missing"}
	case len(j.Assistants) == 0:
		return Health{Detail: "jio rcs: no assistant credentials yet"}
	}
	// No live call: a token is per assistant and a probe would mint one for
	// nothing.
	return Health{Healthy: true, Detail: fmt.Sprintf("jio rcs: %d assistant(s)", len(j.Assistants))}
}

// RegisterTemplate refuses: JBM publishes no template API.
func (j *JioRCS) RegisterTemplate(context.Context, string, RCSTemplateSpec) (RCSTemplateRegistration, error) {
	return RCSTemplateRegistration{}, ErrTemplateRegistrationManual
}

func (j *JioRCS) TemplateStatus(context.Context, string, string) (RCSTemplateRegistration, error) {
	return RCSTemplateRegistration{}, ErrTemplateRegistrationManual
}

func (j *JioRCS) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	if !j.configured() {
		return nil, ErrRCSNotConfigured
	}
	return submitEach(ctx, j.submitOne, submissions)
}

func (j *JioRCS) submitOne(ctx context.Context, submission Submission) Receipt {
	refuse := func(code string) Receipt {
		return Receipt{MessageID: submission.MessageID, ErrorCode: code}
	}
	if submission.AgentID == "" {
		return refuse("agent_not_resolved")
	}
	if strings.TrimSpace(submission.Body) == "" {
		return refuse("body_required")
	}

	payload := map[string]any{"content": map[string]any{"plainText": submission.Body}}
	if submission.Promotional {
		payload["messageTrafficType"] = "PROMOTION"
	}
	// A duration string ending in 's', per errCode 17.
	if submission.TTLSeconds > 0 {
		payload["ttl"] = strconv.Itoa(submission.TTLSeconds) + "s"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return refuse("carrier_rejected")
	}

	// Our message id is Jio's messageId (150 characters at most; a uuid is 36),
	// so status events quote it back and a duplicate is refused with 409.
	query := url.Values{}
	query.Set("messageId", submission.MessageID)
	query.Set("assistantId", submission.AgentID)
	response, err := j.do(ctx, submission.AgentID, http.MethodPost,
		"/v1/messaging/users/"+url.PathEscape(submission.Msisdn)+"/assistantMessages/async?"+query.Encode(),
		encoded)
	if err != nil {
		return refuse(jioTransportCode(err))
	}
	defer response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		// Queued, not sent: SEND_MESSAGE_SUCCESS or _FAILURE follows on the
		// webhook.
		return Receipt{MessageID: submission.MessageID, Accepted: true, CarrierRef: submission.MessageID}
	}
	return refuse(jioResponseCode(response))
}

func (j *JioRCS) Capability(ctx context.Context, agentID, msisdn string) (RCSCapability, error) {
	if !j.configured() {
		return RCSCapability{}, ErrRCSNotConfigured
	}
	if agentID == "" {
		return RCSCapability{}, ErrRCSNoAgent
	}
	response, err := j.do(ctx, agentID, http.MethodGet,
		"/v1/messaging/users/"+url.PathEscape(msisdn)+"/capabilities?requestId="+uuid.NewString(), nil)
	if err != nil {
		return RCSCapability{}, err
	}
	defer response.Body.Close()

	// 404 is "User can't be reached through JBM" — an answer, not a failure.
	if response.StatusCode == http.StatusNotFound {
		return RCSCapability{Msisdn: msisdn, Vendor: j.Vendor()}, nil
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return RCSCapability{}, ErrRCSThrottled
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RCSCapability{}, fmt.Errorf("jio rcs: http %d", response.StatusCode)
	}
	var body struct {
		Features []string `json:"features"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return RCSCapability{}, fmt.Errorf("jio rcs: unreadable body: %w", err)
	}
	features := body.Features
	if features == nil {
		features = []string{}
	}
	return RCSCapability{Msisdn: msisdn, Reachable: true, Features: features, Vendor: j.Vendor()}, nil
}

func (j *JioRCS) Reachable(ctx context.Context, agentID string, msisdns []string) ([]string, error) {
	if !j.configured() {
		return nil, ErrRCSNotConfigured
	}
	if agentID == "" {
		return nil, ErrRCSNoAgent
	}
	unique := dedupe(msisdns)
	if len(unique) == 0 {
		return nil, nil
	}
	if len(unique) > MaxRCSBulkNumbers {
		return nil, ErrRCSTooManyNumbers
	}
	// Under the batch floor, one at a time, as for Airtel.
	if len(unique) < JioBatchMinimum {
		return checkEach(ctx, func(ctx context.Context, msisdn string) (RCSCapability, error) {
			return j.Capability(ctx, agentID, msisdn)
		}, unique)
	}

	payload, err := json.Marshal(map[string]any{"phoneNumbers": unique})
	if err != nil {
		return nil, err
	}
	response, err := j.do(ctx, agentID, http.MethodPost, "/v1/messaging/usersBatchGet", payload)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRCSThrottled
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("jio rcs: http %d", response.StatusCode)
	}
	var body struct {
		ReachableUsers []string `json:"reachableUsers"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("jio rcs: unreadable body: %w", err)
	}
	// In the caller's order, whatever order Jio answered in.
	reachable := make(map[string]bool, len(body.ReachableUsers))
	for _, msisdn := range body.ReachableUsers {
		reachable[msisdn] = true
	}
	out := make([]string, 0, len(body.ReachableUsers))
	for _, msisdn := range unique {
		if reachable[msisdn] {
			out = append(out, msisdn)
		}
	}
	return out, nil
}

var (
	// errJioNoAssistant is a call for an assistant whose secret this
	// deployment does not hold.
	errJioNoAssistant  = errors.New("jio rcs: no secret held for this assistant")
	errJioTokenRefused = errors.New("jio rcs: token refused")
)

func (j *JioRCS) do(ctx context.Context, assistant, method, path string, payload []byte) (*http.Response, error) {
	token, err := j.accessToken(ctx, assistant)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(j.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := j.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("jio rcs: %w", err)
	}
	// "the token needs to be regenerated": drop it so the next call mints one.
	if response.StatusCode == http.StatusUnauthorized {
		j.mu.Lock()
		if j.tokens[assistant].value == token {
			delete(j.tokens, assistant)
		}
		j.mu.Unlock()
	}
	return response, nil
}

// accessToken returns the assistant's cached token, minting one a minute
// before the hour runs out. The lock is held across the call so a burst on a
// cold cache mints one token, not one per message.
func (j *JioRCS) accessToken(ctx context.Context, assistant string) (string, error) {
	secret, ok := j.Assistants[assistant]
	if !ok || secret == "" {
		return "", errJioNoAssistant
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if cached := j.tokens[assistant]; cached.value != "" && time.Now().Before(cached.till.Add(-time.Minute)) {
		return cached.value, nil
	}

	// A GET with the credential in the query string, as documented. It lands
	// in Jio's access logs, not ours: nothing here logs the URL.
	query := url.Values{}
	query.Set("grant_type", "client_credentials")
	query.Set("client_id", assistant)
	query.Set("client_secret", secret)
	query.Set("scope", "read")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(j.TokenURL, "/")+"/v1/oauth/token?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := j.client().Do(request)
	if err != nil {
		// Not wrapped: the URL carries the secret and a transport error quotes it.
		return "", errors.New("jio rcs: token host unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return "", fmt.Errorf("%w: http %d", errJioTokenRefused, response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("jio rcs: token: http %d", response.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("jio rcs: token: unreadable body: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("jio rcs: token: response carried no access_token")
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	if j.tokens == nil {
		j.tokens = map[string]jioToken{}
	}
	j.tokens[assistant] = jioToken{value: body.AccessToken, till: time.Now().Add(lifetime)}
	return body.AccessToken, nil
}

func jioTransportCode(err error) string {
	switch {
	case errors.Is(err, errJioNoAssistant):
		return "agent_not_launched"
	case errors.Is(err, errJioTokenRefused):
		return "carrier_unauthorized"
	}
	return "carrier_unreachable"
}

// jioResponseCode reads a refused send: the errCode in the body when there is
// one, the status line otherwise.
func jioResponseCode(response *http.Response) string {
	var body struct {
		Error struct {
			ErrCode int `json:"errCode"`
		} `json:"error"`
		ErrCode int `json:"errCode"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	code := body.Error.ErrCode
	if code == 0 {
		code = body.ErrCode
	}
	if code != 0 {
		return JioErrorCode(code)
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return "carrier_unauthorized"
	case http.StatusForbidden:
		return "agent_not_launched"
	case http.StatusNotFound:
		return "unreachable_handset"
	case http.StatusTooManyRequests:
		return "carrier_throttled"
	}
	if response.StatusCode >= 500 {
		return "carrier_unavailable"
	}
	return "carrier_rejected"
}

// JioErrorCode maps JBM's errCode table (guide §6.2.1) to Relay's vocabulary.
func JioErrorCode(code int) string {
	switch code {
	case 3:
		return "carrier_unauthorized"
	case 4, 25, 30:
		return "agent_not_launched"
	case 5:
		// RCS disabled, assistant suspended, or the number is on DND.
		return "unreachable_handset"
	case 6:
		return "recipient_monthly_limit"
	case 9, 28:
		return "carrier_throttled"
	case 7, 11, 12, 13, 14, 20:
		return "carrier_unavailable"
	case 23:
		return "carrier_account_inactive"
	case 24:
		return "recipient_opted_out"
	default:
		// 2, 15, 16, 17, 21 and anything added later.
		return "carrier_rejected"
	}
}

var (
	_ Connector            = (*JioRCS)(nil)
	_ RCSCapabilityChecker = (*JioRCS)(nil)
	_ RCSTemplateRegistrar = (*JioRCS)(nil)
)

// jioWebhook is the one envelope JBM posts for every callback (guide §7).
type jioWebhook struct {
	UserPhoneNumber string `json:"userPhoneNumber"`
	// botId in every sample; assistantId in the field table. Either names the
	// assistant.
	BotID       string `json:"botId"`
	AssistantID string `json:"assistantId"`
	EntityType  string `json:"entityType"`
	Entity      struct {
		EventType string `json:"eventType"`
		MessageID string `json:"messageId"`
		SendTime  string `json:"sendTime"`
		Text      string `json:"text"`
		Error     *struct {
			ErrCode int `json:"errCode"`
		} `json:"error"`
		SuggestionResponse *struct {
			PostBack struct {
				Data string `json:"data"`
			} `json:"postBack"`
			PlainText string `json:"plainText"`
		} `json:"suggestionResponse"`
	} `json:"entity"`
}

// ParseJioWebhook reads a JBM callback.
//
// SEND_MESSAGE_SUCCESS is not delivery: it is JBM accepting an async send it
// had already answered 201 to. Delivery is USER_EVENT MESSAGE_DELIVERED, or
// MESSAGE_READ when the delivery event is skipped.
func ParseJioWebhook(payload []byte) (RCSEvent, error) {
	var body jioWebhook
	if err := json.Unmarshal(payload, &body); err != nil {
		return RCSEvent{}, fmt.Errorf("jio webhook: %w", err)
	}
	if body.EntityType == "" {
		return RCSEvent{}, errors.New("jio webhook: no entityType")
	}
	agent := body.AssistantID
	if agent == "" {
		agent = body.BotID
	}
	event := RCSEvent{
		Kind:       RCSEventIgnored,
		Vendor:     "jio",
		Raw:        body.EntityType + ":" + body.Entity.EventType,
		CarrierRef: body.Entity.MessageID,
		AgentID:    strings.TrimSpace(agent),
		Msisdn:     body.UserPhoneNumber,
		OccurredAt: parseCarrierTime(body.Entity.SendTime),
	}

	switch body.EntityType + ":" + body.Entity.EventType {
	case "USER_EVENT:MESSAGE_DELIVERED", "USER_EVENT:MESSAGE_READ":
		event.Kind, event.Delivered = RCSEventDelivery, true
		event.Read = event.Raw == "USER_EVENT:MESSAGE_READ"
	case "STATUS_EVENT:SEND_MESSAGE_FAILURE":
		event.Kind, event.ErrorCode = RCSEventDelivery, "carrier_failed"
		if body.Entity.Error != nil && body.Entity.Error.ErrCode != 0 {
			event.ErrorCode = JioErrorCode(body.Entity.Error.ErrCode)
		}
	case "SERVER_EVENT:TTL_EXPIRATION_REVOKED", "SERVER_EVENT:TTL_EXPIRATION_REVOKE_FAILED":
		event.Kind, event.ErrorCode = RCSEventDelivery, "expired_before_delivery"
	}
	if body.EntityType == "USER_MESSAGE" {
		event.Kind, event.Raw = RCSEventInbound, "USER_MESSAGE"
		// A user message carries no message of ours.
		event.CarrierRef = ""
		event.Text = body.Entity.Text
		if reply := body.Entity.SuggestionResponse; reply != nil {
			event.PostbackData = reply.PostBack.Data
			if event.Text == "" {
				event.Text = reply.PlainText
			}
		}
	}
	return event, nil
}
