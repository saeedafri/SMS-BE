package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/store"
)

// The operator stream carries every tenant's events, so the one thing it must
// get right is who may listen. A tenant token resolves to no operator at all,
// so an identity in the context is not enough.
func TestOperatorStreamRefusesEveryoneButAnOperator(t *testing.T) {
	t.Parallel()
	server := &Server{}

	tenant := context.WithValue(context.Background(), identityKey{},
		store.Identity{TenantID: uuid.New()})
	for name, ctx := range map[string]context.Context{
		"anonymous": context.Background(),
		"tenant":    tenant,
	} {
		request := httptest.NewRequest(http.MethodGet, "/v1/operator/events", nil).WithContext(ctx)
		recorder := httptest.NewRecorder()
		server.streamOperatorEvents(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s on /v1/operator/events = %d, want 401", name, recorder.Code)
		}
	}
}

// Without Redis a campaign must carry on: every notification is a no-op.
func TestCampaignEventsWithoutRedisDoNothingAndDoNotPanic(t *testing.T) {
	t.Parallel()
	server := &Server{}
	server.CampaignStatusChanged(context.Background(), uuid.New(), uuid.New())
	server.CampaignProgressed(context.Background(), uuid.New(), uuid.New())
}

// A Redis that is configured but unreachable must not fail or hang the caller:
// the campaign still sends (acceptance T7).
func TestCampaignEventsWithRedisDownReturnPromptly(t *testing.T) {
	t.Parallel()
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1,
		DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = down.Close() })
	server := &Server{Redis: down}

	done := make(chan struct{})
	go func() {
		server.CampaignStatusChanged(context.Background(), uuid.New(), uuid.New())
		server.CampaignProgressed(context.Background(), uuid.New(), uuid.New())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a campaign event blocked its caller while Redis was down")
	}
}

type frame struct {
	event TenantEvent
	at    time.Time
}

// listen subscribes before returning, so nothing published afterwards is missed.
func listen(t *testing.T, client *redis.Client, channel string) <-chan frame {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	subscription := client.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = subscription.Close() })
	if _, err := subscription.Receive(ctx); err != nil {
		t.Fatalf("subscribe %s: %v", channel, err)
	}
	frames := make(chan frame, 4096)
	go func() {
		for message := range subscription.Channel() {
			var event TenantEvent
			if json.Unmarshal([]byte(message.Payload), &event) == nil {
				frames <- frame{event: event, at: time.Now()}
			}
		}
	}()
	return frames
}

func drain(frames <-chan frame, settle time.Duration) []frame {
	var out []frame
	for {
		select {
		case f := <-frames:
			out = append(out, f)
		case <-time.After(settle):
			return out
		}
	}
}

func redisForTest(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// T1, T4, T6: a status change reaches the owning tenant and the operator at
// once, never coalesced, and never reaches another tenant.
func TestCampaignStatusChangeIsImmediateAndTenantScoped(t *testing.T) {
	client := redisForTest(t)
	server := &Server{Redis: client}
	owner, other, campaign := uuid.New(), uuid.New(), uuid.New()

	ownerStream := listen(t, client, fmt.Sprintf(eventChannel, owner))
	otherStream := listen(t, client, fmt.Sprintf(eventChannel, other))
	operatorStream := listen(t, client, operatorEventChannel)

	// Three in a row: a status change is not rate limited.
	for range 3 {
		server.CampaignStatusChanged(context.Background(), owner, campaign)
	}

	got := drain(ownerStream, 300*time.Millisecond)
	if len(got) != 3 {
		t.Fatalf("owner saw %d status events, want 3 (not coalesced)", len(got))
	}
	for _, f := range got {
		if f.event.Type != "campaign.status_changed" || f.event.ObjectID != campaign.String() ||
			f.event.TenantID != owner.String() {
			t.Errorf("owner frame = %+v", f.event)
		}
	}
	if leaked := drain(otherStream, 100*time.Millisecond); len(leaked) != 0 {
		t.Errorf("another tenant saw %d campaign frames, want 0", len(leaked))
	}
	// The operator stream carries the same frames, naming whose they are. It is
	// shared with every other test, so only this campaign's frames are counted.
	seen := 0
	for _, f := range drain(operatorStream, 100*time.Millisecond) {
		if f.event.ObjectID == campaign.String() {
			seen++
			if f.event.TenantID != owner.String() {
				t.Errorf("operator frame names tenant %s, want %s", f.event.TenantID, owner)
			}
		}
	}
	if seen != 3 {
		t.Errorf("operator saw %d frames for the campaign, want 3", seen)
	}
}

// T3: a burst of progress is published at most once a second, and the last
// change of the burst is always followed by a frame (T2).
func TestCampaignProgressIsCoalescedWithATrailingEvent(t *testing.T) {
	client := redisForTest(t)
	server := &Server{Redis: client}
	tenant, campaign := uuid.New(), uuid.New()
	t.Cleanup(func() { client.Del(context.Background(), progressGateKey(campaign)) })

	stream := listen(t, client, fmt.Sprintf(eventChannel, tenant))

	const burst = 2500 * time.Millisecond
	start := time.Now()
	for time.Since(start) < burst {
		server.CampaignProgressed(context.Background(), tenant, campaign)
		time.Sleep(2 * time.Millisecond)
	}
	lastChange := time.Now()

	// Long enough for the trailing event, short enough to catch a runaway.
	got := drain(stream, 1500*time.Millisecond)
	if len(got) == 0 {
		t.Fatal("no progress frames at all")
	}
	// A 2.5s burst allows the leading event plus one per elapsed second.
	if limit := int(burst/time.Second) + 2; len(got) > limit {
		t.Errorf("%d progress frames in a %s burst, want at most %d", len(got), burst, limit)
	}
	for _, f := range got {
		if f.event.Type != "campaign.progress" || f.event.ObjectID != campaign.String() {
			t.Errorf("frame = %+v", f.event)
		}
	}
	last := got[len(got)-1]
	if last.at.Before(lastChange) {
		t.Errorf("the last frame (%s) predates the last change (%s): the screen would settle on a stale number",
			last.at.Format(time.RFC3339Nano), lastChange.Format(time.RFC3339Nano))
	}
	if gap := last.at.Sub(lastChange); gap > 1100*time.Millisecond {
		t.Errorf("trailing frame came %s after the last change, want within 1s", gap)
	}
}

// A single change, alone, is published at once and not repeated.
func TestASingleCampaignProgressChangePublishesOnce(t *testing.T) {
	client := redisForTest(t)
	server := &Server{Redis: client}
	tenant, campaign := uuid.New(), uuid.New()
	t.Cleanup(func() { client.Del(context.Background(), progressGateKey(campaign)) })

	stream := listen(t, client, fmt.Sprintf(eventChannel, tenant))
	server.CampaignProgressed(context.Background(), tenant, campaign)

	if got := drain(stream, 1500*time.Millisecond); len(got) != 1 {
		t.Fatalf("one change produced %d frames, want 1", len(got))
	}
}
