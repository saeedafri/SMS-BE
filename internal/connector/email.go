package connector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EmailIdentityState is the provider's view of a sending domain, in our
// vocabulary: pending, verified, failed or temporary_failure.
type EmailIdentityState struct {
	DKIM     string
	MailFrom string
}

// EmailMessage is one email, one recipient.
type EmailMessage struct {
	MessageID, TenantID string
	From, FromName, To  string
	Subject, HTML, Text string
	// Headers are set on the message as given: List-Unsubscribe and
	// List-Unsubscribe-Post on marketing mail, nothing on transactional.
	Headers map[string]string
}

// EmailEvent is one thing the provider published about a message.
type EmailEvent struct {
	EventID           string
	ProviderMessageID string
	MessageID         string
	TenantID          string
	// Type: send, delivery, delivery_delay, bounce, complaint, reject,
	// rendering_failure, open or click.
	Type          string
	At            time.Time
	BounceType    string // permanent, transient or undetermined
	BounceSubType string
	FeedbackType  string
	BotLikely     bool
}

// EmailQuota is the provider's sending allowance.
type EmailQuota struct {
	SendQuota24h      int
	SendRatePerSecond float64
	SentLast24h       int
}

// EmailProvider is the email service behind the EMAIL channel: Amazon SES in
// ap-south-1 (decision L-4). Every call sits behind this, and FakeEmail is the
// default while no credentials are configured.
type EmailProvider interface {
	// CreateIdentity registers a domain and returns its three DKIM tokens.
	CreateIdentity(ctx context.Context, domain, mailFromDomain string) ([]string, error)
	Identity(ctx context.Context, domain string) (EmailIdentityState, error)
	Send(ctx context.Context, message EmailMessage) (string, error)
	Quota(ctx context.Context) (EmailQuota, error)
}

// SuppressedBounceSubTypes are bounces the provider never attempted because
// the address was already suppressed. Not charged, and counted in neither
// the numerator nor the divisor of the bounce rate.
var SuppressedBounceSubTypes = map[string]bool{"Suppressed": true,
	"OnAccountSuppressionList": true, "OnTenantSuppressionList": true,
	"EmailValidationSuppressed": true}

// FakeEmail replays SES: DKIM tokens derived from the domain, identities that
// stay pending until set, and the mailbox simulator's outcomes by recipient
// local part at simulator.amazonses.com (success, bounce, complaint, ooto,
// suppressionlist). Any other address is delivered. Its events wait in a
// queue, as the sandbox's receipts do, until drained.
type FakeEmail struct {
	mu         sync.Mutex
	identities map[string]EmailIdentityState
	Sent       []EmailMessage
	events     []EmailEvent
}

func NewFakeEmail() *FakeEmail {
	return &FakeEmail{identities: map[string]EmailIdentityState{}}
}

func (f *FakeEmail) CreateIdentity(_ context.Context, domain, _ string) ([]string, error) {
	tokens := make([]string, 3)
	for i := range tokens {
		sum := sha256.Sum256([]byte(domain + string(rune('a'+i))))
		tokens[i] = fmt.Sprintf("%x", sum[:16])
	}
	return tokens, nil
}

// SetIdentity is the fake's stand-in for DNS propagating.
func (f *FakeEmail) SetIdentity(domain string, state EmailIdentityState) {
	f.mu.Lock()
	f.identities[domain] = state
	f.mu.Unlock()
}

func (f *FakeEmail) Identity(_ context.Context, domain string) (EmailIdentityState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state, known := f.identities[domain]; known {
		return state, nil
	}
	return EmailIdentityState{DKIM: "pending", MailFrom: "pending"}, nil
}

func (f *FakeEmail) Send(_ context.Context, message EmailMessage) (string, error) {
	providerID := "fake-" + uuid.NewString()
	now := time.Now().UTC()
	event := func(kind string) EmailEvent {
		return EmailEvent{EventID: uuid.NewString(), ProviderMessageID: providerID,
			MessageID: message.MessageID, TenantID: message.TenantID, Type: kind, At: now}
	}
	events := []EmailEvent{event("send")}
	local, host, _ := strings.Cut(strings.ToLower(message.To), "@")
	if host == "simulator.amazonses.com" {
		switch strings.SplitN(local, "+", 2)[0] {
		case "bounce":
			bounce := event("bounce")
			bounce.BounceType, bounce.BounceSubType = "permanent", "General"
			events = append(events, bounce)
		case "suppressionlist":
			bounce := event("bounce")
			bounce.BounceType, bounce.BounceSubType = "permanent", "OnAccountSuppressionList"
			events = append(events, bounce)
		case "complaint":
			complaint := event("complaint")
			complaint.FeedbackType = "abuse"
			events = append(events, event("delivery"), complaint)
		default:
			events = append(events, event("delivery"))
		}
	} else {
		events = append(events, event("delivery"))
	}
	f.mu.Lock()
	f.Sent = append(f.Sent, message)
	f.events = append(f.events, events...)
	f.mu.Unlock()
	return providerID, nil
}

// DrainEvents returns the events the provider would have published, and
// clears them.
func (f *FakeEmail) DrainEvents() []EmailEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.events
	f.events = nil
	return out
}

func (f *FakeEmail) Quota(context.Context) (EmailQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return EmailQuota{SendQuota24h: 200, SendRatePerSecond: 1, SentLast24h: len(f.Sent)}, nil
}
