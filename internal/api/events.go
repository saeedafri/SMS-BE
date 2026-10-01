package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Live updates, so a screen reflects a decision without anybody pressing reload.
//
// Every state that matters to a customer is changed by somebody else: an
// operator approves their compliance registration, an owner promotes them, staff
// suspend or reinstate the tenant. Before this the customer found out by
// refreshing, or by signing out and back in — a role change was invisible to the
// nav until the next hard load, and an approval that unblocked their sending
// looked like nothing had happened.
//
// Server-Sent Events rather than WebSockets. The traffic is entirely one-way
// (server tells the browser something changed), SSE is plain HTTP so it needs no
// new nginx protocol handling, and EventSource reconnects on its own — a
// WebSocket would mean writing that reconnect logic by hand for no gain here.
//
// The payload deliberately carries only a type and the ids involved. It is a
// nudge, not a data channel: the browser re-fetches through the normal
// authenticated path, which means a client can never be shown something this
// stream forgot to authorise. Push the change itself and every event becomes a
// place to leak another tenant's data.
const (
	eventHeartbeat = 25 * time.Second
	eventChannel   = "relay:tenant:%s:events"

	// operatorEventChannel carries every tenant's events, for the operator
	// console, which spans all of them. TenantEvent.TenantID says whose each is.
	operatorEventChannel = "relay:operator:events"

	eventCampaignStatus   = "campaign.status_changed"
	eventCampaignProgress = "campaign.progress"

	// campaignProgressGate is the shortest gap between two progress events for
	// one campaign.
	campaignProgressGate = time.Second
)

// TenantEvent is what one change looks like on the wire.
type TenantEvent struct {
	Type     string `json:"type"`
	TenantID string `json:"tenantId"`
	// UserID narrows an event to one member where that matters — a role change
	// concerns the person whose role moved, not everyone in the tenant.
	UserID string `json:"userId,omitempty"`
	// ObjectID is whatever was decided: a registration, sender or template id.
	ObjectID string `json:"objectId,omitempty"`
	// ActorUserID is who made the change, when a tenant user made it.
	//
	// A client must not react to its own action. The person who just changed a
	// role has already seen the result — their own request re-rendered the page
	// — and reloading them again throws away whatever they were doing next.
	// Empty for operator-driven changes, which no tenant session caused, so
	// those always reach everyone.
	ActorUserID string `json:"actorUserId,omitempty"`
	At          string `json:"at"`
}

// publishTenantEvent tells every open stream for a tenant that something moved.
//
// Fire and forget by design: a delivery decision that cannot notify is still a
// delivery decision, and failing the operator's approval because Redis blinked
// would be the wrong trade. The screen falls back to what it did before this
// existed — showing the change on the next load.
func (s *Server) publishTenantEvent(ctx context.Context, tenantID uuid.UUID,
	eventType string, userID, objectID string, actorUserID ...string) {

	if s.Redis == nil {
		return
	}
	actor := ""
	if len(actorUserID) > 0 {
		actor = actorUserID[0]
	}
	payload, err := json.Marshal(TenantEvent{
		Type: eventType, TenantID: tenantID.String(),
		UserID: userID, ObjectID: objectID, ActorUserID: actor,
		At: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	// The tenant's own stream, then the operator's. Two publishes, not one
	// shared channel: a tenant must never be subscribed to a channel that
	// carries anyone else's events. A failure on the first does not skip the
	// second.
	for _, channel := range []string{fmt.Sprintf(eventChannel, tenantID), operatorEventChannel} {
		if err := s.Redis.Publish(ctx, channel, payload).Err(); err != nil && s.Logger != nil {
			s.Logger.Warn("live event not published",
				"type", eventType, "tenant", tenantID, "channel", channel, "error", err)
		}
	}
}

// Campaign events.
//
// Both are nudges like every other event here: the campaign id and nothing
// else, and the screen re-fetches. A campaign finishes in seconds, so polling
// misses it entirely and the stream is the only way a live screen can show it.

// CampaignStatusChanged satisfies sending.CampaignNotifier. A status change is
// rare, so it is published at once and never coalesced.
func (s *Server) CampaignStatusChanged(ctx context.Context, tenantID, campaignID uuid.UUID) {
	s.publishTenantEvent(detach(ctx), tenantID, eventCampaignStatus, "", campaignID.String())
}

// CampaignProgressed satisfies sending.CampaignNotifier. It is called once per
// settled message, so it is coalesced to at most one event per campaign per
// campaignProgressGate, with a trailing event guaranteed after the last change.
//
// Leading edge: the first change of a burst takes a Redis gate and publishes
// immediately. Trailing edge: a change that finds the gate held schedules one
// publish for when the gate clears, so a screen never settles on a stale
// number. At most one trailing publish is pending per campaign per process;
// changes arriving meanwhile are covered by it, because the screen re-fetches
// the current counts rather than reading them from the event.
func (s *Server) CampaignProgressed(ctx context.Context, tenantID, campaignID uuid.UUID) {
	if s.Redis == nil {
		return
	}
	ctx = detach(ctx)
	if s.takeProgressGate(ctx, campaignID) {
		s.publishTenantEvent(ctx, tenantID, eventCampaignProgress, "", campaignID.String())
		return
	}
	if _, pending := s.progressPending.LoadOrStore(campaignID, struct{}{}); pending {
		return
	}
	wait := s.Redis.PTTL(ctx, progressGateKey(campaignID)).Val()
	if wait <= 0 || wait > campaignProgressGate {
		wait = campaignProgressGate
	}
	time.AfterFunc(wait+10*time.Millisecond, func() {
		// Cleared before the gate is tried, so a change landing from here on
		// schedules its own trailing event instead of trusting this one.
		s.progressPending.Delete(campaignID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Not taking the gate means another publish happened after our change
		// (the gate was free when we fired, so somebody took it since), and the
		// screen is already refreshing from it.
		if s.takeProgressGate(ctx, campaignID) {
			s.publishTenantEvent(ctx, tenantID, eventCampaignProgress, "", campaignID.String())
		}
	})
}

func progressGateKey(campaignID uuid.UUID) string {
	return "relay:campaign:" + campaignID.String() + ":progress"
}

// takeProgressGate reports whether this caller may publish a progress event
// now. A Redis error answers false: publishing would fail on the same outage.
func (s *Server) takeProgressGate(ctx context.Context, campaignID uuid.UUID) bool {
	taken, err := s.Redis.SetNX(ctx, progressGateKey(campaignID), 1, campaignProgressGate).Result()
	return err == nil && taken
}

// detach keeps the caller's values but not its cancellation. The caller is often
// a request or a settle whose context ends the moment it returns, and the nudge
// still has to go out.
func detach(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

func (s *Server) mountEventRoutes(r chi.Router) {
	r.Get("/v1/events", s.streamEvents)
	r.Get("/v1/operator/events", s.streamOperatorEvents)
}

// streamEvents holds one SSE connection open for the caller's tenant.
//
// Not part of the 151-operation contract: oapi-codegen models request/response
// pairs, and this is a response that never ends. The frontend consumes it with
// EventSource, which needs no generated client.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated,
			"Missing or invalid bearer token")
		return
	}
	s.serveEventStream(w, r, fmt.Sprintf(eventChannel, identity.TenantID))
}

// streamOperatorEvents is streamEvents for the operator console: every tenant's
// events, authorised by an operator session. A tenant token resolves to no
// operator at all (see authenticate), so it is refused here with 401.
func (s *Server) streamOperatorEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireOperator(r.Context()); err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated,
			"Missing or invalid bearer token")
		return
	}
	s.serveEventStream(w, r, operatorEventChannel)
}

// serveEventStream holds an SSE connection open on one Redis channel. The caller
// has already decided who may listen.
func (s *Server) serveEventStream(w http.ResponseWriter, r *http.Request, channel string) {
	if s.Redis == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"Live updates are not configured on this deployment.")
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeError(w, http.StatusInternalServerError, "internal",
			"This server cannot stream.")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tells nginx not to buffer this response. Without it nginx holds each event
	// until its buffer fills, which for a few hundred bytes means the browser
	// sees nothing for minutes and the feature looks broken rather than absent.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	subscription := s.Redis.Subscribe(r.Context(), channel)
	defer func() { _ = subscription.Close() }()
	messages := subscription.Channel()

	// A comment line immediately, so the browser's EventSource fires onopen and
	// any proxy in between commits to streaming rather than waiting for content.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(eventHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			// Idle connections are reaped by proxies and load balancers. A
			// comment costs nothing and resets every timer between here and the
			// browser.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case message, open := <-messages:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", message.Payload)
			flusher.Flush()
		}
	}
}
