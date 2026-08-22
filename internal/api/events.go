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
	if err := s.Redis.Publish(ctx,
		fmt.Sprintf(eventChannel, tenantID), payload).Err(); err != nil && s.Logger != nil {
		s.Logger.Warn("live event not published",
			"type", eventType, "tenant", tenantID, "error", err)
	}
}

func (s *Server) mountEventRoutes(r chi.Router) {
	r.Get("/v1/events", s.streamEvents)
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

	subscription := s.Redis.Subscribe(r.Context(), fmt.Sprintf(eventChannel, identity.TenantID))
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
