package store

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HotCache holds the configuration rows the send path re-reads for every single
// message.
//
// A send costs eight sequential Postgres round trips before it does any work,
// and six of them ask the same questions about the same tenant thousands of
// times a minute: which sender is this, what does this corridor cost, what is
// this template, is this tenant still active. Those rows change when an
// operator changes them — minutes or days apart — not between two messages in
// the same burst.
//
// WHAT IS DELIBERATELY NOT IN HERE:
//
//   - the wallet balance, and the charge itself. Money is read and written
//     transactionally on every message, always. A cached balance is a double
//     spend waiting for concurrency.
//   - suppression. It is keyed by recipient, not by tenant, so there is nothing
//     to reuse between two messages to different people — and a stale "not
//     suppressed" sends to someone who opted out, which is a regulatory
//     failure, not a performance trade.
//
// The TTL is the blast radius. A tenant suspended at t is still able to send
// until t+TTL, so the TTL is short enough that the window is smaller than the
// time it takes an operator to notice they clicked the button.
type HotCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]hotEntry
}

type hotEntry struct {
	value     any
	expiresAt time.Time
}

// NewHotCache returns a cache holding entries for ttl. A zero or negative ttl
// disables it entirely — every lookup misses — which is what a deployment that
// wants no staleness at all should set.
func NewHotCache(ttl time.Duration) *HotCache {
	return &HotCache{ttl: ttl, entries: make(map[string]hotEntry)}
}

// Get returns a live entry, or false when the key is absent, expired, or the
// cache is disabled.
func (c *HotCache) Get(key string) (any, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.RLock()
	entry, found := c.entries[key]
	c.mu.RUnlock()
	if !found || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// Put stores a value for the cache's TTL.
func (c *HotCache) Put(key string, value any) {
	if c == nil || c.ttl <= 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	// Evict opportunistically rather than with a background goroutine: the key
	// space is bounded by the tenant's own configuration rows, and a sweep on
	// write keeps a long-running process from holding rows nobody asks for any
	// more.
	if len(c.entries) > 4096 {
		for key, entry := range c.entries {
			if now.After(entry.expiresAt) {
				delete(c.entries, key)
			}
		}
	}
	c.entries[key] = hotEntry{value: value, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
}

// Forget drops a key immediately, for the paths that change one of these rows
// and should not wait out the TTL — an operator suspending a tenant, or a
// customer editing a sender.
func (c *HotCache) Forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// The cached read paths.
//
// Each is the uncached function with a lookup in front, so a caller that wants
// the truth of this instant still has one. The send path uses these; every
// other caller keeps reading through.

// CachedSenderID resolves a sender for the send path.
func CachedSenderID(ctx context.Context, pool *pgxpool.Pool, cache *HotCache, id Identity,
	senderID uuid.UUID) (SenderID, error) {

	key := "sender:" + id.TenantID.String() + ":" + senderID.String()
	if value, found := cache.Get(key); found {
		return value.(SenderID), nil
	}
	sender, err := GetSenderID(ctx, pool, id, senderID)
	if err != nil {
		return sender, err
	}
	cache.Put(key, sender)
	return sender, nil
}

// CachedPricingRate resolves what a corridor costs.
func CachedPricingRate(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	tenantID uuid.UUID, country, channel, category string) (PricingRate, error) {

	key := "rate:" + tenantID.String() + ":" + country + ":" + channel + ":" + category
	if value, found := cache.Get(key); found {
		return value.(PricingRate), nil
	}
	rate, err := FindPricingRate(ctx, pool, tenantID, country, channel, category)
	if err != nil {
		return rate, err
	}
	cache.Put(key, rate)
	return rate, nil
}

// CachedTenantStatus answers whether a tenant may send at all.
//
// This is the one entry whose staleness has teeth: a tenant suspended for abuse
// keeps sending until the entry expires. That is why the TTL is seconds rather
// than minutes, and why the operator paths that change standing call Forget.
func CachedTenantStatus(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	id Identity) (string, error) {

	key := TenantStatusKey(id.TenantID)
	if value, found := cache.Get(key); found {
		return value.(string), nil
	}
	status, err := TenantStatus(ctx, pool, id)
	if err != nil {
		return status, err
	}
	cache.Put(key, status)
	return status, nil
}

// TenantStatusKey is exported so the operator paths can drop the entry the
// moment they change a tenant's standing, rather than leaving it to the TTL.
func TenantStatusKey(tenantID uuid.UUID) string {
	return "tenant-status:" + tenantID.String()
}

// SenderKey is exported for the same reason: editing or deleting a sender must
// take effect on the next send, not a second later.
func SenderKey(tenantID, senderID uuid.UUID) string {
	return "sender:" + tenantID.String() + ":" + senderID.String()
}
