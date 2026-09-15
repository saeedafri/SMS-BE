package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// AbuseLimits bound how hard one caller may hit the API, on every route.
//
// PerIPMinute is requests per caller address per minute; going over bans the
// address, for longer each time it comes back. PerTokenMinute is requests per
// credential (API key or session) per minute, refused but never banned, because
// a credential is one customer and a ban would be an outage we chose for them.
// Zero turns a limit off. Ignore lists networks that are never limited: the
// office, a monitoring service.
type AbuseLimits struct {
	PerIPMinute    int
	PerTokenMinute int
	Ignore         []*net.IPNet
}

// banLadder is how long each successive ban lasts within a day.
var banLadder = []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 24 * time.Hour}

const strikeMemory = 24 * time.Hour

func abuseKey(kind, value string) string { return "relay:abuse:" + kind + ":" + value }

// abuseGuard enforces AbuseLimits. It runs after trustedProxyRealIP, so the
// address it counts is the real caller's — the dashboard user's own address
// once the BFF proves it — and never the proxy's.
//
// Carrier webhooks and /healthz are exempt: an operator delivering a burst of
// receipts is the system working. Redis being down fails open, logged: a
// limiter that takes the API down with it is the attack succeeding.
func (s *Server) abuseGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limits := s.Abuse
		if s.Redis == nil || (limits.PerIPMinute <= 0 && limits.PerTokenMinute <= 0) ||
			r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/v1/carrier-webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		ip := clientIP(r)
		if parsed := net.ParseIP(ip); parsed != nil && inAny(limits.Ignore, parsed) {
			next.ServeHTTP(w, r)
			return
		}
		minute := strconv.FormatInt(s.now().Unix()/60, 10)

		if limits.PerIPMinute > 0 {
			if wait, err := s.Redis.PTTL(ctx, abuseKey("ban", ip)).Result(); err == nil && wait > 0 {
				refuseAbuse(w, wait)
				return
			}
			count, err := s.countWindow(ctx, abuseKey("ip", ip+":"+minute))
			if err != nil {
				s.Logger.Warn("abuse guard skipped: redis", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if count > int64(limits.PerIPMinute) {
				refuseAbuse(w, s.ban(ctx, ip, count))
				return
			}
		}

		if token := credentialOf(r); limits.PerTokenMinute > 0 && token != "" {
			sum := sha256.Sum256([]byte(token))
			count, err := s.countWindow(ctx, abuseKey("token", fmt.Sprintf("%x:%s", sum[:12], minute)))
			if err == nil && count > int64(limits.PerTokenMinute) {
				refuseAbuse(w, time.Duration(60-s.now().Unix()%60)*time.Second)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) countWindow(ctx context.Context, key string) (int64, error) {
	pipe := s.Redis.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// ban bans ip for the next rung of the ladder and returns how long. Only the
// request that crosses the limit adds a strike; the rest of that burst is
// already refused by the ban.
func (s *Server) ban(ctx context.Context, ip string, count int64) time.Duration {
	strikes, err := s.Redis.Incr(ctx, abuseKey("strikes", ip)).Result()
	if err != nil {
		strikes = 1
	}
	s.Redis.Expire(ctx, abuseKey("strikes", ip), strikeMemory)
	duration := banLadder[min(int(strikes), len(banLadder))-1]
	s.Redis.Set(ctx, abuseKey("ban", ip), strikes, duration)
	s.Logger.Warn("abuse guard: address banned", "ip", ip, "requests_this_minute", count,
		"strike", strikes, "ban", duration.String())
	return duration
}

// credentialOf is the API key or session a request carries, whichever it sends.
func credentialOf(r *http.Request) string {
	if token, ok := bearerToken(r); ok {
		return token
	}
	if cookie, err := r.Cookie("relay_session"); err == nil {
		return cookie.Value
	}
	return ""
}

func refuseAbuse(w http.ResponseWriter, wait time.Duration) {
	seconds := int(wait.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, "rate_limited",
		fmt.Sprintf("Too many requests. Try again in %s.", humanWait(wait)))
}

func humanWait(wait time.Duration) string {
	if wait >= time.Hour {
		return fmt.Sprintf("%d hours", int((wait+time.Hour-1)/time.Hour))
	}
	minutes := int((wait + time.Minute - 1) / time.Minute)
	if minutes <= 1 {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// Unban lifts a ban on ip at once and forgets its strikes.
func Unban(ctx context.Context, rdb *redis.Client, ip string) error {
	return rdb.Del(ctx, abuseKey("ban", ip), abuseKey("strikes", ip)).Err()
}

// Bans lists every address banned right now and how long each ban has left.
func Bans(ctx context.Context, rdb *redis.Client) (map[string]time.Duration, error) {
	out := map[string]time.Duration{}
	iter := rdb.Scan(ctx, 0, abuseKey("ban", "*"), 500).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if wait, err := rdb.PTTL(ctx, key).Result(); err == nil && wait > 0 {
			out[strings.TrimPrefix(key, abuseKey("ban", ""))] = wait
		}
	}
	return out, iter.Err()
}
