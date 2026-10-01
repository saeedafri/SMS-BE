package sending

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// Per-recipient frequency caps.
//
// A cap says how many messages one handset may receive from a tenant on one
// channel in a calendar day, week and month, counted in the tenant's own time
// zone like every other daily figure here (store.SendDay).
//
// The counters live in Redis, one per recipient and window, because the check
// runs for every message and a hundred thousand ClickHouse counts per campaign
// would be the slowest thing in it. Redis is hot state, not the record: if it
// is down or lost the cap lets the message through. A cap that stops sends
// when its own store fails would turn a Redis outage into a messaging outage.
//
// The check and the increment are one script, so two sends racing for the last
// slot cannot both take it. The slot is taken before the wallet is held, so a
// send refused for funds in that instant still uses one; that is the cheap side
// to be wrong on, and it only ever tightens the cap.
const (
	dayWindowTTL   = 2 * 24 * time.Hour
	weekWindowTTL  = 8 * 24 * time.Hour
	monthWindowTTL = 32 * 24 * time.Hour
)

var takeFrequencySlot = redis.NewScript(`
for i = 1, 3 do
  local limit = tonumber(ARGV[i])
  if limit > 0 and tonumber(redis.call('GET', KEYS[i]) or '0') >= limit then
    return 0
  end
end
for i = 1, 3 do
  if tonumber(ARGV[i]) > 0 then
    redis.call('INCR', KEYS[i])
    redis.call('EXPIRE', KEYS[i], ARGV[3 + i])
  end
end
return 1`)

// frequencyCapFor is the tenant's cap on a channel, read once per send or per
// campaign page and then applied to each recipient.
func (s *Service) frequencyCapFor(ctx context.Context, identity store.Identity,
	channel string) store.FrequencyCap {

	if s.Redis == nil {
		return store.FrequencyCap{}
	}
	frequencyCap, err := store.CachedFrequencyCap(ctx, s.DB, s.Hot, identity, channel)
	if err != nil {
		s.logFrequencyFailure("read cap", err)
		return store.FrequencyCap{}
	}
	return frequencyCap
}

// overFrequencyCap takes a slot for this recipient, or says there is none.
// Nil means the message may go.
func (s *Service) overFrequencyCap(ctx context.Context, identity store.Identity,
	frequencyCap store.FrequencyCap, country, msisdn string) error {

	if s.Redis == nil || !frequencyCap.Active() || frequencyCap.Excludes(msisdn) {
		return nil
	}
	day := store.SendDay(country, s.now())
	year, week := day.ISOWeek()
	who := recipientKey(identity, frequencyCap.Channel, msisdn)
	keys := []string{
		fmt.Sprintf("fcap:%s:d:%s", who, day.Format("20060102")),
		fmt.Sprintf("fcap:%s:w:%d-%02d", who, year, week),
		fmt.Sprintf("fcap:%s:m:%s", who, day.Format("200601")),
	}
	taken, err := takeFrequencySlot.Run(ctx, s.Redis, keys,
		limitArg(frequencyCap.DailyLimit), limitArg(frequencyCap.WeeklyLimit),
		limitArg(frequencyCap.MonthlyLimit),
		int(dayWindowTTL.Seconds()), int(weekWindowTTL.Seconds()),
		int(monthWindowTTL.Seconds())).Int()
	if err != nil {
		s.logFrequencyFailure("take slot", err)
		return nil
	}
	if taken == 0 {
		return messaging.ErrFrequencyCapped
	}
	return nil
}

// limitArg is the script's spelling of "no limit": zero.
func limitArg(limit *int) int {
	if limit == nil {
		return 0
	}
	return *limit
}

// recipientKey names a recipient without putting their number in Redis.
func recipientKey(identity store.Identity, channel, msisdn string) string {
	sum := sha256.Sum256([]byte(identity.TenantID.String() + "|" + channel + "|" + msisdn))
	return hex.EncodeToString(sum[:12])
}

func (s *Service) logFrequencyFailure(what string, err error) {
	if s.Logger != nil {
		s.Logger.Warn("frequency cap unavailable, letting the message through",
			"step", what, "error", err.Error())
	}
}
