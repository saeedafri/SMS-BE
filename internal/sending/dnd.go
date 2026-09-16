package sending

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/saeedafri/sms-be/internal/store"
)

// DNDPreference is what India's National Customer Preference Register says
// about one number: a full block, or the commercial categories it refuses.
type DNDPreference struct {
	FullyBlocked      bool
	BlockedCategories []string
}

// Blocked reports whether a promotional message may go to this number.
//
// Any blocked category counts as blocked: a template carries no commercial
// category yet, so a partial block cannot be matched to the message, and
// guessing in the sender's favour is the guess that breaks the regulation.
func (p DNDPreference) Blocked() bool {
	return p.FullyBlocked || len(p.BlockedCategories) > 0
}

// DNDRegister answers for one number. Implementations are an operator's scrub
// API, a licensed provider, or a synced register file.
type DNDRegister interface {
	Lookup(ctx context.Context, msisdn string) (DNDPreference, error)
}

// ErrNoDNDRegister is the answer when this deployment has no register at all.
// It refuses promotional traffic rather than sending it unchecked.
var ErrNoDNDRegister = errors.New("sending: no do-not-disturb register is configured")

// NoDNDRegister is the default: every lookup fails, so every Indian
// promotional message is refused dnd_check_unavailable until a register exists.
type NoDNDRegister struct{}

func (NoDNDRegister) Lookup(context.Context, string) (DNDPreference, error) {
	return DNDPreference{}, ErrNoDNDRegister
}

// cachedRegister remembers an answer for a day. The register changes slowly —
// an opt-out takes up to seven days to apply — so a day's cache can never be
// more permissive than the register itself. Errors are never cached.
//
// ponytail: an in-process map, so each instance looks up once a day per number;
// move it to Redis if that ever costs too much.
type cachedRegister struct {
	inner DNDRegister
	now   func() time.Time
	mu    sync.Mutex
	seen  map[string]cachedAnswer
}

type cachedAnswer struct {
	preference DNDPreference
	until      time.Time
}

const dndCacheFor = 24 * time.Hour

// CacheDNDLookups wraps a register with that day-long memory.
func CacheDNDLookups(register DNDRegister, now func() time.Time) DNDRegister {
	if now == nil {
		now = time.Now
	}
	return &cachedRegister{inner: register, now: now, seen: map[string]cachedAnswer{}}
}

func (c *cachedRegister) Lookup(ctx context.Context, msisdn string) (DNDPreference, error) {
	c.mu.Lock()
	answer, ok := c.seen[msisdn]
	c.mu.Unlock()
	if ok && c.now().Before(answer.until) {
		return answer.preference, nil
	}
	preference, err := c.inner.Lookup(ctx, msisdn)
	if err != nil {
		return DNDPreference{}, err
	}
	c.mu.Lock()
	c.seen[msisdn] = cachedAnswer{preference: preference, until: c.now().Add(dndCacheFor)}
	c.mu.Unlock()
	return preference, nil
}

// dndStatus asks the register about a message, when the message is one the
// register governs: a promotional SMS to an Indian number. Anything else is
// never looked up, so transactional traffic keeps flowing whatever the
// register is doing.
func (s *Service) dndStatus(ctx context.Context, channel, msisdn string,
	template store.Template) (blocked, unavailable bool) {

	if channel != "SMS" || !strings.HasPrefix(msisdn, "+91") ||
		template.DltCategory == nil || *template.DltCategory != "PROMOTIONAL" {
		return false, false
	}
	register := s.DND
	if register == nil {
		register = NoDNDRegister{}
	}
	preference, err := register.Lookup(ctx, msisdn)
	if err != nil {
		if s.Logger != nil && !errors.Is(err, ErrNoDNDRegister) {
			s.Logger.Warn("dnd lookup failed; refusing the message", "error", err)
		}
		return false, true
	}
	return preference.Blocked(), false
}
