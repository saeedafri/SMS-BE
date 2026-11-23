package connector

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
)

// WhatsAppAccount is what Meta says about a connected number: how it pays,
// in what currency, its display name's review, and its daily limit.
type WhatsAppAccount struct {
	PaymentMethod     string // none, prepaid_upi, card, credit_line, unknown
	BillingCurrency   string
	DisplayNameStatus string // none, pending_review, approved, declined
	MessagingLimit    string // TIER_250 ... TIER_UNLIMITED
}

var (
	// ErrInvalidCode is an Embedded Signup code that is malformed, used, or
	// older than its 30 seconds.
	ErrInvalidCode = errors.New("whatsapp: the signup code is invalid, used or expired")
	// ErrRegistrationLimit is Meta's 133016: ten registration attempts per
	// number in 72 hours are spent.
	ErrRegistrationLimit = errors.New("whatsapp: registration attempts exhausted (133016)")
	// ErrTokenInvalid is Meta's 190 or a permission error on the business token.
	ErrTokenInvalid = errors.New("whatsapp: the business token is invalid (190)")
)

// WhatsAppProvider is Meta's Cloud API as Relay, a Tech Provider, calls it
// (decision L-3). Every call sits behind this; FakeWhatsApp is the default
// while no app credentials are configured.
type WhatsAppProvider interface {
	// ExchangeCode turns an Embedded Signup code into a business token.
	ExchangeCode(ctx context.Context, code string) (string, error)
	SubscribeApp(ctx context.Context, token, wabaID string) error
	RegisterNumber(ctx context.Context, token, phoneNumberID, pin string) error
	Account(ctx context.Context, token, wabaID, phoneNumberID string) (WhatsAppAccount, error)
	// SubmitTemplate sends a template definition for Meta's review and
	// returns Meta's id for it, which its status events quote.
	SubmitTemplate(ctx context.Context, token, wabaID string, definition []byte) (string, error)
}

// FakeWhatsApp replays Meta: a code is good once, starts "code-" and never
// "code-expired"; a number registers unless set to refuse; an account answers
// what it was set to, or a fresh number with no payment method.
type FakeWhatsApp struct {
	mu       sync.Mutex
	used     map[string]bool
	refuse   map[string]bool
	accounts map[string]WhatsAppAccount
}

func NewFakeWhatsApp() *FakeWhatsApp {
	return &FakeWhatsApp{used: map[string]bool{}, refuse: map[string]bool{},
		accounts: map[string]WhatsAppAccount{}}
}

func (f *FakeWhatsApp) ExchangeCode(_ context.Context, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(code, "code-") || code == "code-expired" || f.used[code] {
		return "", ErrInvalidCode
	}
	f.used[code] = true
	return "token-for-" + code, nil
}

func (f *FakeWhatsApp) SubscribeApp(context.Context, string, string) error { return nil }

// RefuseRegistration makes a number answer 133016 from now on.
func (f *FakeWhatsApp) RefuseRegistration(phoneNumberID string) {
	f.mu.Lock()
	f.refuse[phoneNumberID] = true
	f.mu.Unlock()
}

func (f *FakeWhatsApp) RegisterNumber(_ context.Context, _, phoneNumberID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse[phoneNumberID] {
		return ErrRegistrationLimit
	}
	return nil
}

// SetAccount is what Meta will say about a number from now on.
func (f *FakeWhatsApp) SetAccount(phoneNumberID string, account WhatsAppAccount) {
	f.mu.Lock()
	f.accounts[phoneNumberID] = account
	f.mu.Unlock()
}

func (f *FakeWhatsApp) Account(_ context.Context, _, _, phoneNumberID string) (WhatsAppAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if account, set := f.accounts[phoneNumberID]; set {
		return account, nil
	}
	return WhatsAppAccount{PaymentMethod: "none", DisplayNameStatus: "pending_review",
		MessagingLimit: "TIER_250"}, nil
}

// SubmitTemplate accepts every definition for review. Meta's ids are numbers;
// random ones keep tests that share a database from colliding.
func (f *FakeWhatsApp) SubmitTemplate(context.Context, string, string, []byte) (string, error) {
	return fmt.Sprint(rand.Int64N(1 << 52)), nil
}
