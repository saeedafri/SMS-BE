package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// What every tenant sent, for the operator console: which tenant sent which
// campaign or journey, who in that tenant started it, every message under it,
// to whom, when, and what became of it.
//
// Mounted directly rather than through the generated contract, like
// /v1/events: these routes are not in openapi.json yet. The shapes below are
// the proposal in docs/HANDOFF_TO_UI_2026-09-25-operator-send-visibility.md;
// once the frontend adds them to the contract they move behind the generated
// interface without changing on the wire.
func (s *Server) mountOperatorSendRoutes(r chi.Router) {
	r.Get("/v1/operator/campaigns", s.listOperatorCampaigns)
	r.Get("/v1/operator/journeys", s.listOperatorJourneys)
	r.Get("/v1/operator/messages", s.listOperatorMessages)
	r.Get("/v1/operator/messages/summary", s.operatorMessageSummary)
	r.Get("/v1/operator/messages/export", s.exportOperatorMessages)
	r.Get("/v1/operator/messages/{id}", s.getOperatorMessage)
}

// The widest message window one request may ask for. Messages are partitioned
// by day and the box has two CPUs; an unbounded cross-tenant scan is a query
// that takes the warehouse down for everyone, so the range is refused rather
// than quietly clamped.
const (
	maxMessageWindow     = 92 * 24 * time.Hour
	defaultMessageWindow = 7 * 24 * time.Hour
	maxExportRows        = 100_000
)

type operatorAuthor struct {
	UserID *uuid.UUID `json:"userId"`
	Name   *string    `json:"name"`
	Email  *string    `json:"email"`
}

// authorOf is nil for a send nobody is recorded against: everything created
// before 00066, which the console shows as "not recorded" rather than a blank
// that reads like a system send.
func authorOf(author store.Author) *operatorAuthor {
	if author.UserID == nil && author.Name == nil && author.Email == nil {
		return nil
	}
	return &operatorAuthor{UserID: author.UserID, Name: author.Name, Email: author.Email}
}

type operatorSendCounts struct {
	Total     int   `json:"total"`
	Queued    int   `json:"queued"`
	Sent      int   `json:"sent"`
	Delivered int   `json:"delivered"`
	Read      int   `json:"read"`
	Failed    int   `json:"failed"`
	Rejected  int   `json:"rejected"`
	CostMinor int64 `json:"costMinor"`
}

func toSendCounts(counts store.SendCounts) operatorSendCounts {
	return operatorSendCounts{Total: counts.Total, Queued: counts.Queued,
		Sent: counts.Sent, Delivered: counts.Delivered, Read: counts.Read,
		Failed: counts.Failed, Rejected: counts.Rejected, CostMinor: counts.CostMinor}
}

type operatorCampaign struct {
	ID              uuid.UUID          `json:"id"`
	Name            string             `json:"name"`
	TenantID        uuid.UUID          `json:"tenantId"`
	TenantName      string             `json:"tenantName"`
	Channel         string             `json:"channel"`
	FallbackChannel *string            `json:"fallbackChannel"`
	Country         string             `json:"country"`
	Status          string             `json:"status"`
	Sender          string             `json:"sender"`
	Template        string             `json:"template"`
	ListName        *string            `json:"listName"`
	Recipients      int                `json:"recipients"`
	Withheld        int                `json:"withheld"`
	CostMinorMin    int64              `json:"estimatedCostMinorMin"`
	CostMinorMax    int64              `json:"estimatedCostMinorMax"`
	Currency        string             `json:"currency"`
	RetryOf         *uuid.UUID         `json:"retryOf"`
	CreatedBy       *operatorAuthor    `json:"createdBy"`
	CreatedAt       time.Time          `json:"createdAt"`
	ScheduledAt     *time.Time         `json:"scheduledAt"`
	SendStartedAt   *time.Time         `json:"sendStartedAt"`
	PausedAt        *time.Time         `json:"pausedAt"`
	CancelledAt     *time.Time         `json:"cancelledAt"`
	Messages        operatorSendCounts `json:"messages"`
}

func (s *Server) listOperatorCampaigns(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	filter, ok := sendFilterFrom(w, r, campaignStatuses)
	if !ok {
		return
	}
	campaigns, total, err := store.ListOperatorCampaigns(r.Context(), s.operatorPool(), filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tenants, ids := make([]uuid.UUID, 0, len(campaigns)), make([]uuid.UUID, 0, len(campaigns))
	for _, c := range campaigns {
		tenants, ids = append(tenants, c.TenantID), append(ids, c.ID)
	}
	counts, ok := s.ownerCounts(w, r, "campaign_id", tenants, ids)
	if !ok {
		return
	}
	out := make([]operatorCampaign, 0, len(campaigns))
	for _, c := range campaigns {
		out = append(out, operatorCampaign{ID: c.ID, Name: c.Name, TenantID: c.TenantID,
			TenantName: c.TenantName, Channel: c.Channel, FallbackChannel: c.FallbackChannel,
			Country: c.Country, Status: c.Status, Sender: c.SenderHeader,
			Template: c.TemplateName, ListName: c.ListName, Recipients: c.Recipients,
			Withheld: c.Withheld, CostMinorMin: c.CostMinorMin, CostMinorMax: c.CostMinorMax,
			Currency: c.Currency, RetryOf: c.RetryOf, CreatedBy: authorOf(c.CreatedBy),
			CreatedAt: c.CreatedAt, ScheduledAt: c.ScheduledAt,
			SendStartedAt: c.SendStartedAt, PausedAt: c.PausedAt, CancelledAt: c.CancelledAt,
			Messages: toSendCounts(counts[c.ID])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": out, "total": total})
}

type operatorJourney struct {
	ID          uuid.UUID          `json:"id"`
	Name        string             `json:"name"`
	TenantID    uuid.UUID          `json:"tenantId"`
	TenantName  string             `json:"tenantName"`
	Status      string             `json:"status"`
	TriggerType string             `json:"triggerType"`
	ListName    *string            `json:"listName"`
	Channels    []string           `json:"channels"`
	Recipients  int                `json:"recipients"`
	Enrolled    int                `json:"enrolled"`
	Withheld    int                `json:"withheld"`
	CreatedBy   *operatorAuthor    `json:"createdBy"`
	ActivatedBy *operatorAuthor    `json:"activatedBy"`
	CreatedAt   time.Time          `json:"createdAt"`
	ActivatedAt *time.Time         `json:"activatedAt"`
	Messages    operatorSendCounts `json:"messages"`
}

func (s *Server) listOperatorJourneys(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	filter, ok := sendFilterFrom(w, r, journeyStatuses)
	if !ok {
		return
	}
	journeys, total, err := store.ListOperatorJourneys(r.Context(), s.operatorPool(), filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tenants, ids := make([]uuid.UUID, 0, len(journeys)), make([]uuid.UUID, 0, len(journeys))
	for _, j := range journeys {
		tenants, ids = append(tenants, j.TenantID), append(ids, j.ID)
	}
	counts, ok := s.ownerCounts(w, r, "journey_id", tenants, ids)
	if !ok {
		return
	}
	out := make([]operatorJourney, 0, len(journeys))
	for _, j := range journeys {
		channels := j.Channels
		if channels == nil {
			channels = []string{}
		}
		out = append(out, operatorJourney{ID: j.ID, Name: j.Name, TenantID: j.TenantID,
			TenantName: j.TenantName, Status: j.Status, TriggerType: j.TriggerType,
			ListName: j.ListName, Channels: channels, Recipients: j.Recipients,
			Enrolled: j.Enrolled, Withheld: j.Withheld, CreatedBy: authorOf(j.CreatedBy),
			ActivatedBy: authorOf(j.ActivatedBy), CreatedAt: j.CreatedAt,
			ActivatedAt: j.ActivatedAt, Messages: toSendCounts(counts[j.ID])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"journeys": out, "total": total})
}

func (s *Server) ownerCounts(w http.ResponseWriter, r *http.Request, column string,
	tenants, ids []uuid.UUID) (map[uuid.UUID]store.SendCounts, bool) {

	if len(ids) == 0 {
		return map[uuid.UUID]store.SendCounts{}, true
	}
	clickhouse, err := s.clickhouse(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	counts, err := store.CountMessagesByOwner(r.Context(), clickhouse, column, tenants, ids)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return counts, true
}

type operatorSentBy struct {
	// Kind is "user" (a dashboard user) or "api_key".
	Kind string `json:"kind"`
	// Via is how the message was sent: "campaign", "journey" or "direct" (a
	// single send from the dashboard or the API).
	Via       string     `json:"via"`
	ID        *uuid.UUID `json:"id"`
	Name      *string    `json:"name"`
	Email     *string    `json:"email"`
	KeyPrefix *string    `json:"keyPrefix"`
}

type operatorMessage struct {
	ID               uuid.UUID       `json:"id"`
	TenantID         uuid.UUID       `json:"tenantId"`
	TenantName       string          `json:"tenantName"`
	Source           string          `json:"source"`
	CampaignID       *uuid.UUID      `json:"campaignId"`
	CampaignName     *string         `json:"campaignName"`
	JourneyID        *uuid.UUID      `json:"journeyId"`
	JourneyName      *string         `json:"journeyName"`
	Channel          string          `json:"channel"`
	DeliveredChannel *string         `json:"deliveredChannel"`
	Country          string          `json:"country"`
	Sender           string          `json:"sender"`
	TemplateID       *uuid.UUID      `json:"templateId"`
	To               string          `json:"to"`
	Email            *string         `json:"email"`
	Status           string          `json:"status"`
	State            string          `json:"state"`
	ErrorCode        *string         `json:"errorCode"`
	ErrorClass       *string         `json:"errorClass"`
	Segments         int             `json:"segments"`
	CostMinor        int64           `json:"costMinor"`
	Currency         string          `json:"currency"`
	Carrier          *string         `json:"carrier"`
	RouteID          *string         `json:"routeId"`
	CarrierRef       *string         `json:"carrierRef"`
	SentBy           *operatorSentBy `json:"sentBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	SentAt           *time.Time      `json:"sentAt"`
	DeliveredAt      *time.Time      `json:"deliveredAt"`
	ReadAt           *time.Time      `json:"readAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

// messageSource says what put a message on the wire.
func messageSource(record store.MessageRecord) string {
	switch {
	case record.CampaignID != nil:
		return "campaign"
	case record.JourneyID != nil:
		return "journey"
	}
	return "api"
}

// messageStatus is the contract status, with "read" for a delivered message
// the handset has reported opening.
func messageStatus(record store.MessageRecord) string {
	status := messaging.ContractStatus(messaging.State(record.Status))
	if status == "delivered" && record.ReadAt != nil {
		return "read"
	}
	return status
}

// operatorMessages maps a page of messages, resolving the names the warehouse
// holds only as ids: the tenant, and whoever sent each message — the user or
// key on a direct send, the campaign's creator or the journey's activator
// otherwise.
func (s *Server) operatorMessages(ctx context.Context,
	records []store.MessageRecord) ([]operatorMessage, error) {

	pool := s.operatorPool()
	tenants, err := store.TenantNames(ctx, pool)
	if err != nil {
		return nil, err
	}
	var users, keys, campaigns, journeys []uuid.UUID
	for _, record := range records {
		switch {
		case record.SentByID != nil && record.SentByKind == "user":
			users = append(users, *record.SentByID)
		case record.SentByID != nil && record.SentByKind == "api_key":
			keys = append(keys, *record.SentByID)
		}
		if record.CampaignID != nil {
			campaigns = append(campaigns, *record.CampaignID)
		}
		if record.JourneyID != nil {
			journeys = append(journeys, *record.JourneyID)
		}
	}
	labels, err := store.SenderLabels(ctx, pool, users, keys)
	if err != nil {
		return nil, err
	}
	owners, err := store.SendOwners(ctx, pool, campaigns, journeys)
	if err != nil {
		return nil, err
	}

	out := make([]operatorMessage, 0, len(records))
	for _, record := range records {
		message := operatorMessage{ID: record.ID, TenantID: record.TenantID,
			TenantName: tenants[record.TenantID.String()], Source: messageSource(record),
			CampaignID: record.CampaignID, CampaignName: record.CampaignName,
			JourneyID: record.JourneyID, JourneyName: record.JourneyName,
			Channel: record.Channel, DeliveredChannel: record.DeliveredChannel,
			Country: record.Country, Sender: record.SenderHeader,
			TemplateID: record.TemplateID, To: record.Msisdn, Email: record.Email,
			Status: messageStatus(record), State: record.Status,
			ErrorCode: record.ErrorCode, ErrorClass: record.ErrorClass,
			Segments: int(record.Segments), CostMinor: record.CostMinor,
			Currency: record.Currency, Carrier: nonEmpty(record.Carrier),
			RouteID: record.RouteID, CarrierRef: record.CarrierRef,
			CreatedAt: record.CreatedAt, SentAt: record.SentAt,
			DeliveredAt: record.DeliveredAt, ReadAt: record.ReadAt,
			UpdatedAt: record.UpdatedAt}

		switch {
		case record.SentByID != nil:
			label, known := labels[*record.SentByID]
			sentBy := &operatorSentBy{Kind: record.SentByKind, Via: "direct", ID: record.SentByID}
			if known {
				sentBy.Name, sentBy.Email = nonEmpty(label.Name), nonEmpty(label.Email)
				sentBy.KeyPrefix = nonEmpty(label.KeyPrefix)
			}
			message.SentBy = sentBy
		case record.CampaignID != nil:
			message.SentBy = sentByAuthor(owners[*record.CampaignID].Author, "campaign")
		case record.JourneyID != nil:
			message.SentBy = sentByAuthor(owners[*record.JourneyID].Author, "journey")
		}
		// The send path writes a journey's name onto its messages and not a
		// campaign's, so the name comes from the owner row when the message
		// has none.
		if record.CampaignID != nil && message.CampaignName == nil {
			message.CampaignName = nonEmpty(owners[*record.CampaignID].Name)
		}
		if record.JourneyID != nil && message.JourneyName == nil {
			message.JourneyName = nonEmpty(owners[*record.JourneyID].Name)
		}
		out = append(out, message)
	}
	return out, nil
}

func sentByAuthor(author store.Author, via string) *operatorSentBy {
	if author.UserID == nil && author.Name == nil && author.Email == nil {
		return nil
	}
	return &operatorSentBy{Kind: "user", Via: via, ID: author.UserID,
		Name: author.Name, Email: author.Email}
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *Server) listOperatorMessages(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	filter, ok := messageFilterFrom(w, r)
	if !ok {
		return
	}
	clickhouse, err := s.clickhouse(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	records, total, err := store.ListOperatorMessages(r.Context(), clickhouse, filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := s.operatorMessages(r.Context(), records)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "total": total,
		"from": filter.From, "to": filter.To})
}

type operatorMessageEvent struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	ErrorCode  *string   `json:"errorCode"`
	Detail     string    `json:"detail"`
	OccurredAt time.Time `json:"occurredAt"`
}

func (s *Server) getOperatorMessage(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	messageID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such message.")
		return
	}
	clickhouse, err := s.clickhouse(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	record, err := store.GetOperatorMessage(r.Context(), clickhouse, messageID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such message.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	mapped, err := s.operatorMessages(r.Context(), []store.MessageRecord{record})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	events, err := store.MessageEvents(r.Context(), clickhouse, record.TenantID, record.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	timeline := make([]operatorMessageEvent, 0, len(events))
	for _, event := range events {
		timeline = append(timeline, operatorMessageEvent{From: event.FromState,
			To: event.ToState, ErrorCode: event.ErrorCode, Detail: event.Detail,
			OccurredAt: event.OccurredAt})
	}
	writeJSON(w, http.StatusOK, struct {
		operatorMessage
		Events []operatorMessageEvent `json:"events"`
	}{mapped[0], timeline})
}

type messageTotals struct {
	Messages  int              `json:"messages"`
	Queued    int              `json:"queued"`
	Sent      int              `json:"sent"`
	Delivered int              `json:"delivered"`
	Read      int              `json:"read"`
	Failed    int              `json:"failed"`
	Rejected  int              `json:"rejected"`
	Segments  int              `json:"segments"`
	Cost      map[string]int64 `json:"costMinorByCurrency"`
	// DeliveryRate is delivered over everything that reached a final carrier
	// outcome (delivered + failed), as a percentage. Messages still in flight
	// and our own refusals are left out: neither says anything about delivery.
	DeliveryRate *float64 `json:"deliveryRate"`
}

func (t *messageTotals) add(tally store.MessageTally) {
	t.Messages += tally.Messages
	t.Segments += tally.Segments
	t.Read += tally.Read
	if t.Cost == nil {
		t.Cost = map[string]int64{}
	}
	t.Cost[tally.Currency] += tally.CostMinor
	switch messaging.ContractStatus(messaging.State(tally.Status)) {
	case "queued":
		t.Queued += tally.Messages
	case "sent":
		t.Sent += tally.Messages
	case "delivered":
		t.Delivered += tally.Messages
	case "failed":
		t.Failed += tally.Messages
	case "rejected":
		t.Rejected += tally.Messages
	}
}

func (t *messageTotals) finish() {
	if settled := t.Delivered + t.Failed; settled > 0 {
		rate := float64(t.Delivered*10000/settled) / 100
		t.DeliveryRate = &rate
	}
	if t.Cost == nil {
		t.Cost = map[string]int64{}
	}
}

type channelTotals struct {
	Channel string `json:"channel"`
	messageTotals
}

type tenantTotals struct {
	TenantID   uuid.UUID `json:"tenantId"`
	TenantName string    `json:"tenantName"`
	messageTotals
}

func (s *Server) operatorMessageSummary(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	filter, ok := messageFilterFrom(w, r)
	if !ok {
		return
	}
	clickhouse, err := s.clickhouse(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tallies, err := store.TallyOperatorMessages(r.Context(), clickhouse, filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names, err := store.TenantNames(r.Context(), s.operatorPool())
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	var totals messageTotals
	byChannel := map[string]*channelTotals{}
	byTenant := map[uuid.UUID]*tenantTotals{}
	for _, tally := range tallies {
		totals.add(tally)
		if byChannel[tally.Channel] == nil {
			byChannel[tally.Channel] = &channelTotals{Channel: tally.Channel}
		}
		byChannel[tally.Channel].add(tally)
		if byTenant[tally.TenantID] == nil {
			byTenant[tally.TenantID] = &tenantTotals{TenantID: tally.TenantID,
				TenantName: names[tally.TenantID.String()]}
		}
		byTenant[tally.TenantID].add(tally)
	}
	totals.finish()
	channels := make([]channelTotals, 0, len(byChannel))
	for _, entry := range byChannel {
		entry.finish()
		channels = append(channels, *entry)
	}
	tenants := make([]tenantTotals, 0, len(byTenant))
	for _, entry := range byTenant {
		entry.finish()
		tenants = append(tenants, *entry)
	}
	// Biggest first, so the console's table starts with who is sending most.
	sort.Slice(channels, func(i, j int) bool { return channels[i].Messages > channels[j].Messages })
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].Messages > tenants[j].Messages })
	writeJSON(w, http.StatusOK, map[string]any{"from": filter.From, "to": filter.To,
		"totals": totals, "byChannel": channels, "byTenant": tenants})
}

func (s *Server) exportOperatorMessages(w http.ResponseWriter, r *http.Request) {
	if !s.operatorSignedIn(w, r) {
		return
	}
	filter, ok := messageFilterFrom(w, r)
	if !ok {
		return
	}
	clickhouse, err := s.clickhouse(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// A page at a time through the same mapping the list uses, so a name
	// resolved on screen is the name in the file.
	var records []store.MessageRecord
	if err := store.EachOperatorMessage(r.Context(), clickhouse, filter, maxExportRows,
		func(record store.MessageRecord) error {
			records = append(records, record)
			return nil
		}); err != nil {
		s.internalError(w, r, err)
		return
	}
	messages, err := s.operatorMessages(r.Context(), records)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="messages-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	// Tells the console the file was cut at the ceiling, so it can say so
	// instead of presenting a partial export as the whole.
	w.Header().Set("X-Export-Truncated", strconv.FormatBool(len(records) == maxExportRows))
	out := csv.NewWriter(w)
	_ = out.Write([]string{"createdAt", "tenantName", "tenantId", "source", "campaignName",
		"journeyName", "channel", "deliveredChannel", "sender", "to", "email", "status",
		"errorCode", "sentAt", "deliveredAt", "readAt", "segments", "costMinor",
		"currency", "carrier", "sentByName", "sentByEmail", "sentByKeyPrefix", "messageId"})
	for _, m := range messages {
		row := []string{stamp(&m.CreatedAt), m.TenantName, m.TenantID.String(), m.Source,
			deref(m.CampaignName), deref(m.JourneyName), m.Channel, deref(m.DeliveredChannel),
			m.Sender, m.To, deref(m.Email), m.Status, deref(m.ErrorCode), stamp(m.SentAt),
			stamp(m.DeliveredAt), stamp(m.ReadAt), strconv.Itoa(m.Segments),
			strconv.FormatInt(m.CostMinor, 10), m.Currency, deref(m.Carrier), "", "", "",
			m.ID.String()}
		if m.SentBy != nil {
			row[20], row[21], row[22] = deref(m.SentBy.Name), deref(m.SentBy.Email),
				deref(m.SentBy.KeyPrefix)
		}
		_ = out.Write(row)
	}
	out.Flush()
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func (s *Server) operatorSignedIn(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.requireOperator(r.Context()); err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated,
			"Sign in to the operator console.")
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if s.Logger != nil {
		s.Logger.Error("operator send view failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}

var (
	campaignStatuses = []string{"scheduled", "queued", "sending", "sent", "failed",
		"paused", "cancelled"}
	journeyStatuses = []string{"draft", "active", "paused", "archived"}
	messageStatuses = []string{"queued", "sent", "delivered", "read", "failed", "rejected"}
	messageSources  = []string{"campaign", "journey", "api"}
)

// sendFilterFrom reads the campaign and journey list filters. A bad value is
// a 422 naming the parameter, never a filter that silently does not apply.
func sendFilterFrom(w http.ResponseWriter, r *http.Request,
	statuses []string) (store.OperatorSendFilter, bool) {

	query := r.URL.Query()
	var filter store.OperatorSendFilter
	var ok bool
	if filter.TenantID, ok = uuidParam(w, query.Get("tenantId"), "tenantId"); !ok {
		return filter, false
	}
	if filter.Status, ok = enumParam(w, query.Get("status"), "status", statuses); !ok {
		return filter, false
	}
	if filter.Channel, ok = enumParam(w, strings.ToUpper(query.Get("channel")), "channel",
		channelIDs()); !ok {
		return filter, false
	}
	if q := strings.TrimSpace(query.Get("q")); q != "" {
		filter.Search = &q
	}
	from, to, ok := timeRange(w, query.Get("from"), query.Get("to"))
	if !ok {
		return filter, false
	}
	if !from.IsZero() {
		filter.From = &from
	}
	if !to.IsZero() {
		filter.To = &to
	}
	if filter.Page, filter.Limit, ok = pageParams(w, query.Get("page"), query.Get("limit")); !ok {
		return filter, false
	}
	return filter, true
}

func messageFilterFrom(w http.ResponseWriter, r *http.Request) (store.OperatorMessageFilter, bool) {
	query := r.URL.Query()
	var filter store.OperatorMessageFilter
	var ok bool
	if filter.TenantID, ok = uuidParam(w, query.Get("tenantId"), "tenantId"); !ok {
		return filter, false
	}
	if filter.CampaignID, ok = uuidParam(w, query.Get("campaignId"), "campaignId"); !ok {
		return filter, false
	}
	if filter.JourneyID, ok = uuidParam(w, query.Get("journeyId"), "journeyId"); !ok {
		return filter, false
	}
	if filter.Channel, ok = enumParam(w, strings.ToUpper(query.Get("channel")), "channel",
		channelIDs()); !ok {
		return filter, false
	}
	status, ok := enumParam(w, query.Get("status"), "status", messageStatuses)
	if !ok {
		return filter, false
	}
	if status != nil {
		if *status == "read" {
			filter.Statuses, filter.ReadOnly = contractStatusToStates("delivered"), true
		} else {
			filter.Statuses = contractStatusToStates(*status)
		}
	}
	if filter.Source, ok = enumParam(w, query.Get("source"), "source", messageSources); !ok {
		return filter, false
	}
	if to := strings.TrimSpace(query.Get("recipient")); to != "" {
		filter.Recipient = &to
	}

	from, to, ok := timeRange(w, query.Get("from"), query.Get("to"))
	if !ok {
		return filter, false
	}
	now := time.Now().UTC()
	if to.IsZero() {
		to = now.Add(time.Minute)
	}
	if from.IsZero() {
		from = to.Add(-defaultMessageWindow)
		// A campaign or journey is one send: every one of its messages, not
		// the last week's, unless the caller narrows it.
		if filter.CampaignID != nil || filter.JourneyID != nil {
			from = to.Add(-maxMessageWindow)
		}
	}
	if to.Sub(from) > maxMessageWindow {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"from and to may be at most 92 days apart.")
		return filter, false
	}
	filter.From, filter.To = from, to
	if filter.Page, filter.Limit, ok = pageParams(w, query.Get("page"), query.Get("limit")); !ok {
		return filter, false
	}
	return filter, true
}

func uuidParam(w http.ResponseWriter, value, name string) (*uuid.UUID, bool) {
	if value == "" {
		return nil, true
	}
	id, err := uuid.Parse(value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, name+" must be a UUID.")
		return nil, false
	}
	return &id, true
}

func enumParam(w http.ResponseWriter, value, name string, allowed []string) (*string, bool) {
	if value == "" {
		return nil, true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return &value, true
		}
	}
	writeError(w, http.StatusUnprocessableEntity, codeValidation,
		name+" must be one of: "+strings.Join(allowed, ", ")+".")
	return nil, false
}

// timeRange reads from and to as RFC 3339 instants or plain dates. A date
// means the whole day in IST, where every operator this console serves works:
// to=2026-09-25 includes the 25th.
func timeRange(w http.ResponseWriter, fromText, toText string) (time.Time, time.Time, bool) {
	parse := func(text string, endOfDay bool) (time.Time, bool) {
		if text == "" {
			return time.Time{}, true
		}
		if at, err := time.Parse(time.RFC3339, text); err == nil {
			return at.UTC(), true
		}
		day, err := time.ParseInLocation("2006-01-02", text, istZone)
		if err != nil {
			return time.Time{}, false
		}
		if endOfDay {
			day = day.AddDate(0, 0, 1)
		}
		return day.UTC(), true
	}
	from, fromOK := parse(fromText, false)
	to, toOK := parse(toText, true)
	if !fromOK || !toOK {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"from and to must be dates (2026-09-25) or RFC 3339 times.")
		return time.Time{}, time.Time{}, false
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"from must be before to.")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func pageParams(w http.ResponseWriter, pageText, limitText string) (int, int, bool) {
	var page, limit *int
	for _, param := range []struct {
		text   string
		target **int
	}{{pageText, &page}, {limitText, &limit}} {
		if param.text == "" {
			continue
		}
		value, err := strconv.Atoi(param.text)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, codeValidation,
				"page and limit must be whole numbers.")
			return 0, 0, false
		}
		*param.target = &value
	}
	pageValue, ok := pageNumber(page)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, pageTooLow)
		return 0, 0, false
	}
	limitValue, ok := pageSize(limit)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, limitOutOfRange)
		return 0, 0, false
	}
	return pageValue, limitValue, true
}
