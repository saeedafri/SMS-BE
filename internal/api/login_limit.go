package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// LoginLimits bounds password guessing. Counters live in Redis; with no Redis,
// or a nil LoginLimits, logins are not limited — the same fail-open stance the
// send rate limiter takes, because an unreachable counter must not lock every
// customer out.
type LoginLimits struct {
	AccountFailures int           // wrong passwords for one address before it locks
	IPFailures      int           // failed logins from one IP, any address, before it is blocked
	Window          time.Duration // how long failures are remembered
	Lock            time.Duration // first lock; doubles on each repeat, up to MaxLock
	MaxLock         time.Duration
}

// DefaultLoginLimits are the production rules for one login surface: three
// wrong passwords lock the address for 30 seconds, it then gets three fresh
// chances, and every further lock within a day doubles (30s, 1m, 2m, 4m, ...)
// up to MaxLock. Operators reach a longer ceiling: that account sees every
// customer.
func DefaultLoginLimits(scope string) LoginLimits {
	if scope == "operator" {
		return LoginLimits{AccountFailures: 3, IPFailures: 10, Window: 15 * time.Minute,
			Lock: 30 * time.Second, MaxLock: 4 * time.Hour}
	}
	return LoginLimits{AccountFailures: 3, IPFailures: 20, Window: 15 * time.Minute,
		Lock: 30 * time.Second, MaxLock: time.Hour}
}

// codeTooManyAttempts rides on the login's existing 401 so no new status is
// introduced; the message is the same whether the address exists or not.
const codeTooManyAttempts = "too_many_attempts"

func tooManyAttemptsMessage(wait time.Duration) string {
	return fmt.Sprintf("Too many sign-in attempts. Try again in %s.", waitPhrase(wait))
}

// waitPhrase says a lock's remaining time the way a person would: seconds under
// a minute, whole minutes under an hour, then hours.
func waitPhrase(wait time.Duration) string {
	seconds := int((wait + time.Second - 1) / time.Second)
	switch {
	case seconds <= 1:
		return "1 second"
	case seconds < 60:
		return fmt.Sprintf("%d seconds", seconds)
	case seconds <= 60*60:
		if minutes := (seconds + 59) / 60; minutes > 1 {
			return fmt.Sprintf("%d minutes", minutes)
		}
		return "1 minute"
	default:
		if hours := (seconds + 3599) / 3600; hours > 1 {
			return fmt.Sprintf("%d hours", hours)
		}
		return "1 hour"
	}
}

func loginKey(kind, scope, value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("relay:login:%s:%s:%x", kind, scope, sum[:12])
}

// loginBlocked reports whether this address or IP is locked, and for how long.
// Checked before the password, so a correct password during a lock is refused
// too — otherwise the lock tells the attacker nothing and stops nothing.
func (s *Server) loginBlocked(ctx context.Context, scope, email, ip string) (bool, time.Duration) {
	if s.Redis == nil {
		return false, 0
	}
	limits := DefaultLoginLimits(scope)
	if wait, err := s.Redis.PTTL(ctx, loginKey("lock", scope, email)).Result(); err == nil && wait > 0 {
		return true, wait
	}
	if ip != "" {
		count, err := s.Redis.Get(ctx, loginKey("ipfail", scope, ip)).Int()
		if err == nil && count >= limits.IPFailures {
			wait, _ := s.Redis.PTTL(ctx, loginKey("ipfail", scope, ip)).Result()
			return true, wait
		}
	}
	return false, 0
}

// loginFailed records a wrong password and locks the address once it reaches
// the limit. Unknown addresses are counted exactly like real ones, so a lock
// reveals nothing about which accounts exist.
func (s *Server) loginFailed(ctx context.Context, scope, email, ip string) (locked bool) {
	if s.Redis == nil {
		return false
	}
	limits := DefaultLoginLimits(scope)
	if ip != "" {
		key := loginKey("ipfail", scope, ip)
		if n, err := s.Redis.Incr(ctx, key).Result(); err == nil && n == 1 {
			s.Redis.Expire(ctx, key, limits.Window)
		}
	}
	key := loginKey("fail", scope, email)
	failures, err := s.Redis.Incr(ctx, key).Result()
	if err != nil {
		return false
	}
	if failures == 1 {
		s.Redis.Expire(ctx, key, limits.Window)
	}
	if failures < int64(limits.AccountFailures) {
		return false
	}
	// Each lock within a day doubles the next one, so a patient attacker gets
	// slower rather than a fresh budget every quarter hour.
	repeatKey := loginKey("locks", scope, email)
	repeats, _ := s.Redis.Incr(ctx, repeatKey).Result()
	s.Redis.Expire(ctx, repeatKey, 24*time.Hour)
	wait := limits.Lock
	for i := int64(1); i < repeats && wait < limits.MaxLock; i++ {
		wait *= 2
	}
	if wait > limits.MaxLock {
		wait = limits.MaxLock
	}
	s.Redis.Set(ctx, loginKey("lock", scope, email), 1, wait)
	s.Redis.Del(ctx, key)
	return true
}

// loginSucceeded forgets earlier mistakes, so someone who mistyped twice is
// never nearer a lock the next time they sign in.
func (s *Server) loginSucceeded(ctx context.Context, scope, email string) {
	if s.Redis != nil {
		s.Redis.Del(ctx, loginKey("fail", scope, email))
	}
}

// allowAccountEmail caps verification and reset emails per address, so nobody
// can flood a person's inbox or spend the mail quota. The caller answers as if
// the mail went out either way.
func (s *Server) allowAccountEmail(ctx context.Context, kind, email string) bool {
	// Browser-suite fixture addresses on a dev deployment request these mails
	// far more than three times an hour, by design.
	if s.Redis == nil || (s.EnableDevEndpoints && isFixtureAddress(email)) {
		return true
	}
	key := loginKey("mail:"+kind, "any", email)
	n, err := s.Redis.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	if n == 1 {
		s.Redis.Expire(ctx, key, time.Hour)
	}
	return n <= 3
}

// requestIP is the caller's address as the request logger recorded it.
func requestIP(ctx context.Context) string {
	if fields, ok := ctx.Value(callerKey{}).(*callerFields); ok {
		return fields.clientIP
	}
	return ""
}
