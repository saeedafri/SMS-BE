package connector

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

// GoogleRBM is Google's RCS Business Messaging API, called directly.
//
// It exists for testing before launch: an agent that has not launched reaches
// the handsets invited as its testers, on any Indian operator, with real
// delivery and read receipts and no carrier contract. It is not a route to
// customers in India, which still goes through the operators.
//
// Google reviews agents, not messages, so there is no template registry: the
// message text is sent as it is. That is also why it implements neither
// RegisterTemplate nor TemplateStatus — see TemplatesReviewed.
type GoogleRBM struct {
	// BaseURL is the regional API root, e.g.
	// https://asia-rcsbusinessmessaging.googleapis.com
	BaseURL string

	// ServiceAccountJSON is the key file Google's console issues for the
	// partner's Cloud project. Held in memory; the path is config's business.
	ServiceAccountJSON []byte

	HTTP *http.Client

	mu        sync.Mutex
	token     string
	tokenTill time.Time
}

// googleRBMScope is the only scope the RBM API accepts.
const googleRBMScope = "https://www.googleapis.com/auth/rcsbusinessmessaging"

func (g *GoogleRBM) Vendor() string { return "google" }
func (g *GoogleRBM) Name() string   { return g.Vendor() }

// TemplatesReviewed is false: Google approves the agent and sends any text it
// is given, so a message needs no carrier-approved template.
func (g *GoogleRBM) TemplatesReviewed() bool { return false }

func (g *GoogleRBM) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (g *GoogleRBM) configured() bool {
	return g.BaseURL != "" && len(g.ServiceAccountJSON) > 0
}

func (g *GoogleRBM) Health(context.Context) Health {
	if !g.configured() {
		return Health{Healthy: false, Detail: "google rbm: credentials are incomplete"}
	}
	if _, err := parseServiceAccount(g.ServiceAccountJSON); err != nil {
		return Health{Healthy: false, Detail: "google rbm: " + err.Error()}
	}
	return Health{Healthy: true, Detail: "google rbm: configured"}
}

func (g *GoogleRBM) Capability(ctx context.Context, agentID, msisdn string) (RCSCapability, error) {
	if !g.configured() {
		return RCSCapability{}, ErrRCSNotConfigured
	}
	if agentID == "" {
		return RCSCapability{}, ErrRCSNoAgent
	}
	query := url.Values{}
	query.Set("requestId", uuid.NewString())
	query.Set("agentId", agentID)
	response, err := g.do(ctx, http.MethodGet,
		"/v1/phones/"+url.PathEscape(msisdn)+"/capabilities?"+query.Encode(), nil)
	if err != nil {
		return RCSCapability{}, err
	}
	defer response.Body.Close()

	// Google answers "this handset cannot receive RCS from this agent" with 404,
	// which covers both a phone without RCS and a tester who has not accepted
	// the invite. Both are simply unreachable.
	if response.StatusCode == http.StatusNotFound {
		return RCSCapability{Msisdn: msisdn, Vendor: g.Vendor()}, nil
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return RCSCapability{}, ErrRCSThrottled
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RCSCapability{}, fmt.Errorf("google rbm: http %d", response.StatusCode)
	}
	var body struct {
		Features []string `json:"features"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return RCSCapability{}, fmt.Errorf("google rbm: unreadable body: %w", err)
	}
	return RCSCapability{Msisdn: msisdn, Reachable: true, Features: body.Features,
		Vendor: g.Vendor()}, nil
}

// Reachable asks one handset at a time. A test agent reaches at most twenty
// invited testers, so a bulk endpoint would buy nothing.
func (g *GoogleRBM) Reachable(ctx context.Context, agentID string, msisdns []string) ([]string, error) {
	if !g.configured() {
		return nil, ErrRCSNotConfigured
	}
	if agentID == "" {
		return nil, ErrRCSNoAgent
	}
	unique := dedupe(msisdns)
	if len(unique) > MaxRCSBulkNumbers {
		return nil, ErrRCSTooManyNumbers
	}
	return checkEach(ctx, func(ctx context.Context, msisdn string) (RCSCapability, error) {
		return g.Capability(ctx, agentID, msisdn)
	}, unique)
}

func (g *GoogleRBM) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	if !g.configured() {
		return nil, ErrRCSNotConfigured
	}
	return submitEach(ctx, g.submitOne, submissions)
}

func (g *GoogleRBM) submitOne(ctx context.Context, submission Submission) Receipt {
	refuse := func(code string) Receipt {
		return Receipt{MessageID: submission.MessageID, ErrorCode: code}
	}
	if submission.AgentID == "" {
		return refuse("agent_not_resolved")
	}
	if strings.TrimSpace(submission.Body) == "" {
		// Nothing to show. Campaign sends carry no body today — the Indian
		// carriers render the template themselves — so they are refused here
		// rather than delivered as an empty bubble.
		return refuse("body_required")
	}

	payload := map[string]any{"contentMessage": map[string]any{"text": submission.Body}}
	if submission.TTLSeconds > 0 {
		payload["ttl"] = strconv.Itoa(submission.TTLSeconds) + "s"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return refuse("carrier_rejected")
	}

	// Relay's message id is Google's messageId, so delivery events quote it back
	// and a re-sent duplicate is refused by Google rather than delivered twice.
	query := url.Values{}
	query.Set("messageId", submission.MessageID)
	query.Set("agentId", submission.AgentID)
	response, err := g.do(ctx, http.MethodPost,
		"/v1/phones/"+url.PathEscape(submission.Msisdn)+"/agentMessages?"+query.Encode(), encoded)
	if err != nil {
		return refuse("carrier_unreachable")
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return Receipt{MessageID: submission.MessageID, Accepted: true,
			CarrierRef: submission.MessageID}
	case response.StatusCode == http.StatusNotFound:
		// Not RCS-capable, or a tester who has not accepted the invite.
		return refuse("unreachable_handset")
	case response.StatusCode == http.StatusTooManyRequests:
		return refuse("carrier_throttled")
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return refuse("carrier_unauthorized")
	case response.StatusCode >= 500:
		return refuse("carrier_unavailable")
	default:
		return refuse("carrier_rejected")
	}
}

func (g *GoogleRBM) do(ctx context.Context, method, path string, payload []byte) (*http.Response, error) {
	token, err := g.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(g.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := g.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("google rbm: %w", err)
	}
	// A revoked or expired token is dropped so the next call mints a new one
	// instead of replaying a dead credential.
	if response.StatusCode == http.StatusUnauthorized {
		g.mu.Lock()
		if g.token == token {
			g.token, g.tokenTill = "", time.Time{}
		}
		g.mu.Unlock()
	}
	return response, nil
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	key         *rsa.PrivateKey
}

func parseServiceAccount(raw []byte) (serviceAccount, error) {
	var account serviceAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		return account, errors.New("service account file is not JSON")
	}
	if account.ClientEmail == "" || account.PrivateKey == "" {
		return account, errors.New("service account file has no client_email or private_key")
	}
	if account.TokenURI == "" {
		account.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(account.PrivateKey))
	if block == nil {
		return account, errors.New("service account private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return account, errors.New("service account private_key is not a PKCS#8 key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return account, errors.New("service account private_key is not RSA")
	}
	account.key = key
	return account, nil
}

// accessToken exchanges a signed service-account assertion for an access token,
// cached until a minute before it expires. The lock is held across the call so
// a burst of sends on a cold cache mints one token, not one each.
func (g *GoogleRBM) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Now().Before(g.tokenTill.Add(-time.Minute)) {
		return g.token, nil
	}

	account, err := parseServiceAccount(g.ServiceAccountJSON)
	if err != nil {
		return "", fmt.Errorf("google rbm: %w", err)
	}
	assertion, err := signJWT(account, time.Now())
	if err != nil {
		return "", fmt.Errorf("google rbm: %w", err)
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, account.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("google rbm: token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("google rbm: token: http %d", response.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.AccessToken == "" {
		return "", errors.New("google rbm: token: response carried no access_token")
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 30 * time.Minute
	}
	g.token, g.tokenTill = body.AccessToken, time.Now().Add(lifetime)
	return g.token, nil
}

// signJWT is the RS256 assertion Google's token endpoint accepts for a service
// account: issuer and subject are the account, audience is the token URI.
func signJWT(account serviceAccount, now time.Time) (string, error) {
	encode := func(value any) (string, error) {
		raw, err := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw), err
	}
	header, err := encode(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := encode(map[string]any{
		"iss": account.ClientEmail, "sub": account.ClientEmail, "aud": account.TokenURI,
		"scope": googleRBMScope, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + claims
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, account.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// GoogleWebhookVerification is the handshake Google sends when a webhook URL is
// saved in its console: the partner's client token and a secret to echo back.
type GoogleWebhookVerification struct {
	ClientToken string `json:"clientToken"`
	Secret      string `json:"secret"`
}

// ParseGoogleWebhookVerification reports whether payload is that handshake.
func ParseGoogleWebhookVerification(payload []byte) (GoogleWebhookVerification, bool) {
	var handshake GoogleWebhookVerification
	if err := json.Unmarshal(payload, &handshake); err != nil ||
		handshake.ClientToken == "" || handshake.Secret == "" {
		return GoogleWebhookVerification{}, false
	}
	return handshake, true
}

// VerifyGoogleWebhookSignature checks X-Goog-Signature: base64 of HMAC-SHA512,
// keyed by the partner's client token, over the decoded message.data. Unlike
// the Indian carriers Google signs its callbacks, so an unsigned or wrongly
// signed event is refused.
func VerifyGoogleWebhookSignature(payload []byte, signature, clientToken string) bool {
	if signature == "" || clientToken == "" {
		return false
	}
	var envelope viPubSubEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil ||
		envelope.Message == nil || envelope.Message.Data == "" {
		return false
	}
	data, err := decodeBase64Either(envelope.Message.Data)
	if err != nil {
		return false
	}
	mac := hmac.New(sha512.New, []byte(clientToken))
	mac.Write(data)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// ParseGoogleWebhook reads Google's own event. Vi forwards this exact shape, so
// the parsing is Vi's; what differs is whose agent id counts. Google's agentId
// (…@rbm.goog) IS the id we send under and store, where Vi's is not.
func ParseGoogleWebhook(payload []byte) (RCSEvent, error) {
	event, err := ParseViWebhook(payload)
	if err != nil {
		return RCSEvent{}, fmt.Errorf("google webhook: %s", strings.TrimPrefix(err.Error(), "vi webhook: "))
	}
	event.Vendor = "google"

	var envelope viPubSubEnvelope
	inner := payload
	if json.Unmarshal(payload, &envelope) == nil && envelope.Message != nil && envelope.Message.Data != "" {
		if decoded, err := decodeBase64Either(envelope.Message.Data); err == nil {
			inner = decoded
		}
	}
	var body struct {
		AgentID string `json:"agentId"`
	}
	_ = json.Unmarshal(inner, &body)
	event.AgentID = strings.TrimSpace(body.AgentID)
	return event, nil
}

var (
	_ Connector            = (*GoogleRBM)(nil)
	_ RCSCapabilityChecker = (*GoogleRBM)(nil)
)
