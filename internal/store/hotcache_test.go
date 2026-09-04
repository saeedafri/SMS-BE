package store

import (
	"testing"
	"time"
)

// A cache that outlives its TTL is a stale answer with a confident face.
func TestEntriesExpire(t *testing.T) {
	cache := NewHotCache(40 * time.Millisecond)
	cache.Put("k", "v")
	if _, found := cache.Get("k"); !found {
		t.Fatal("a value put a moment ago is already gone")
	}
	time.Sleep(60 * time.Millisecond)
	if _, found := cache.Get("k"); found {
		t.Fatal("an expired entry was still served")
	}
}

// Forget is what makes a suspension take effect on the next send rather than
// whenever the entry happens to lapse. Without it the TTL is not a ceiling on
// staleness, it IS the staleness.
func TestForgetIsImmediate(t *testing.T) {
	cache := NewHotCache(time.Minute)
	cache.Put("tenant-status:x", "active")
	cache.Forget("tenant-status:x")
	if _, found := cache.Get("tenant-status:x"); found {
		t.Fatal("a forgotten entry was still served — a suspended tenant would keep sending")
	}
}

// A zero TTL is how a deployment says "no staleness at all". It must miss every
// time rather than caching forever.
func TestAZeroTTLDisablesTheCache(t *testing.T) {
	cache := NewHotCache(0)
	cache.Put("k", "v")
	if _, found := cache.Get("k"); found {
		t.Fatal("a disabled cache served a value")
	}
}

// The nil cache is the "not configured" path every caller takes when Hot is
// unset, so it has to be safe rather than a panic on the send path.
func TestANilCacheIsSafe(t *testing.T) {
	var cache *HotCache
	cache.Put("k", "v")
	cache.Forget("k")
	if _, found := cache.Get("k"); found {
		t.Fatal("a nil cache returned a value")
	}
}

// Concurrent readers and writers, because the send path is the only caller and
// it is entirely concurrent. Run with -race.
func TestConcurrentUseIsSafe(t *testing.T) {
	cache := NewHotCache(time.Second)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for n := 0; n < 500; n++ {
				cache.Put("k", n)
				cache.Get("k")
				cache.Forget("other")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
