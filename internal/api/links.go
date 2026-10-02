package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"
)

// Short links and click logs. Mounted directly, outside the generated contract,
// until the frontend adds them; see docs/HANDOFF_TO_UI_2026-10-02-sigmo-gaps.md.
//
// The create and read routes are session-only. GET /l/{code} is the public
// redirect a recipient taps: the code authorises it, nothing else does.
func (s *Server) mountLinkRoutes(r chi.Router) {
	r.Post("/v1/links", s.createLink)
	r.Post("/v1/links/bulk", s.createLinksBulk)
	r.Get("/v1/links", s.listLinks)
	r.Get("/v1/links/clicks", s.listLinkClicks)
	r.Get("/v1/links/stats", s.linkStats)
	r.Get("/l/{code}", s.followLink)
	r.Head("/l/{code}", s.followLink)
}

const (
	maxBulkLinks     = 1000
	defaultLinkPage  = 50
	maxDestinationLn = 2048
)

type linkBody struct {
	Destination   string     `json:"destination"`
	CampaignID    *uuid.UUID `json:"campaignId"`
	Recipient     *string    `json:"recipient"`
	Label         *string    `json:"label"`
	AppendClickID bool       `json:"appendClickId"`
	ExpiresAt     *time.Time `json:"expiresAt"`
}

type linkOut struct {
	Code          string     `json:"code"`
	ShortURL      string     `json:"shortUrl"`
	Destination   string     `json:"destination"`
	CampaignID    *uuid.UUID `json:"campaignId"`
	Recipient     *string    `json:"recipient"`
	Label         *string    `json:"label"`
	AppendClickID bool       `json:"appendClickId"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	Clicks        int        `json:"clicks"`
	HumanClicks   int        `json:"humanClicks"`
}

func linkBase(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" && !strings.Contains(r.Host, ".") {
		scheme = "http"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func toLinkOut(r *http.Request, l store.ShortLink) linkOut {
	return linkOut{Code: l.Code, ShortURL: linkBase(r) + "/l/" + l.Code,
		Destination: l.Destination, CampaignID: l.CampaignID, Recipient: l.Recipient,
		Label: l.Label, AppendClickID: l.AppendClickID, ExpiresAt: l.ExpiresAt,
		CreatedAt: l.CreatedAt, Clicks: l.Clicks, HumanClicks: l.HumanClicks}
}

func (s *Server) linkIdentity(w http.ResponseWriter, r *http.Request) (store.Identity, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
	}
	return identity, ok
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		if isBodyTooLarge(err) {
			writeTooLarge(w, bodyLimitFor(r))
			return false
		}
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "can't decode JSON body: "+err.Error())
		return false
	}
	return true
}

// checkDestination refuses what a link must never point at.
func checkDestination(r *http.Request, identity store.Identity, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxDestinationLn {
		return "destination must be a URL of up to 2048 characters."
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil {
		return "destination must be an absolute http or https URL without credentials."
	}
	// A link to ourselves is a redirect loop a recipient cannot leave.
	if strings.EqualFold(parsed.Host, r.Host) {
		return "destination cannot point at Relay itself."
	}
	if regime, known := compliance.For(identity.Country); known {
		if result := regime.ValidateCtaURL(raw); !result.OK {
			return result.Reason
		}
	}
	return ""
}

func cleanLabel(label *string) (*string, string) {
	if label == nil {
		return nil, ""
	}
	trimmed := strings.TrimSpace(*label)
	if len(trimmed) > 120 {
		return nil, "label is at most 120 characters."
	}
	if trimmed == "" {
		return nil, ""
	}
	return &trimmed, ""
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.linkIdentity(w, r)
	if !ok {
		return
	}
	var body linkBody
	if !decodeStrict(w, r, &body) {
		return
	}
	link, problem := buildLink(r, identity, body)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	made, err := store.CreateShortLinks(r.Context(), s.DB, identity, []store.ShortLink{link})
	if err != nil {
		s.Logger.Error("create link", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusCreated, toLinkOut(r, made[0]))
}

func buildLink(r *http.Request, identity store.Identity, body linkBody) (store.ShortLink, string) {
	if problem := checkDestination(r, identity, body.Destination); problem != "" {
		return store.ShortLink{}, problem
	}
	label, problem := cleanLabel(body.Label)
	if problem != "" {
		return store.ShortLink{}, problem
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now()) {
		return store.ShortLink{}, "expiresAt must be in the future."
	}
	var recipient *string
	if body.Recipient != nil && strings.TrimSpace(*body.Recipient) != "" {
		number, valid := audience.NormaliseE164(*body.Recipient)
		if !valid {
			return store.ShortLink{}, *body.Recipient + " is not a phone number with a country code."
		}
		recipient = &number
	}
	return store.ShortLink{Destination: strings.TrimSpace(body.Destination),
		CampaignID: body.CampaignID, Recipient: recipient, Label: label,
		AppendClickID: body.AppendClickID, ExpiresAt: body.ExpiresAt}, ""
}

// createLinksBulk makes one link per recipient for the same destination, so a
// campaign can send each person their own trackable link.
func (s *Server) createLinksBulk(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.linkIdentity(w, r)
	if !ok {
		return
	}
	var body struct {
		linkBody
		Recipients []string `json:"recipients"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if len(body.Recipients) == 0 || len(body.Recipients) > maxBulkLinks {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"recipients must hold between 1 and 1000 numbers.")
		return
	}
	links := make([]store.ShortLink, 0, len(body.Recipients))
	seen := map[string]bool{}
	for _, raw := range body.Recipients {
		each := body.linkBody
		each.Recipient = &raw
		link, problem := buildLink(r, identity, each)
		if problem != "" {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
			return
		}
		if seen[*link.Recipient] {
			continue
		}
		seen[*link.Recipient] = true
		links = append(links, link)
	}
	made, err := store.CreateShortLinks(r.Context(), s.DB, identity, links)
	if err != nil {
		s.Logger.Error("create links", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	out := make([]linkOut, len(made))
	for i, l := range made {
		out[i] = toLinkOut(r, l)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"links": out})
}

func linkFilterFrom(w http.ResponseWriter, r *http.Request) (store.LinkFilter, int, int, bool) {
	query := r.URL.Query()
	var filter store.LinkFilter
	if text := query.Get("campaignId"); text != "" {
		id, err := uuid.Parse(text)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, "campaignId must be a uuid.")
			return filter, 0, 0, false
		}
		filter.CampaignID = &id
	}
	filter.Code = query.Get("code")
	filter.IncludeBots = query.Get("includeBots") == "true"
	from, to, ok := timeRange(w, query.Get("from"), query.Get("to"))
	if !ok {
		return filter, 0, 0, false
	}
	filter.From, filter.To = from, to
	page, limit, ok := pageParams(w, query.Get("page"), query.Get("limit"))
	if limit == 0 {
		limit = defaultLinkPage
	}
	return filter, page, limit, ok
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.linkIdentity(w, r)
	if !ok {
		return
	}
	filter, page, limit, ok := linkFilterFrom(w, r)
	if !ok {
		return
	}
	links, total, err := store.ListShortLinks(r.Context(), s.DB, identity, filter, page, limit)
	if err != nil {
		s.Logger.Error("list links", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	out := make([]linkOut, len(links))
	for i, l := range links {
		out[i] = toLinkOut(r, l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out, "total": total})
}

func (s *Server) listLinkClicks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.linkIdentity(w, r)
	if !ok {
		return
	}
	filter, page, limit, ok := linkFilterFrom(w, r)
	if !ok {
		return
	}
	clicks, total, err := store.ListLinkClicks(r.Context(), s.DB, identity, filter, page, limit)
	if err != nil {
		s.Logger.Error("list link clicks", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	type clickOut struct {
		ID          uuid.UUID  `json:"id"`
		Code        string     `json:"code"`
		ClickedAt   time.Time  `json:"clickedAt"`
		IsBot       bool       `json:"isBot"`
		Device      string     `json:"device"`
		Os          string     `json:"os"`
		Recipient   *string    `json:"recipient"`
		CampaignID  *uuid.UUID `json:"campaignId"`
		Destination string     `json:"destination"`
	}
	out := make([]clickOut, len(clicks))
	for i, c := range clicks {
		out[i] = clickOut{c.ID, c.Code, c.ClickedAt, c.IsBot, c.Device, c.OS,
			c.Recipient, c.CampaignID, c.Destination}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clicks": out, "total": total})
}

func (s *Server) linkStats(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.linkIdentity(w, r)
	if !ok {
		return
	}
	filter, _, _, ok := linkFilterFrom(w, r)
	if !ok {
		return
	}
	stats, err := store.LinkStatistics(r.Context(), s.DB, identity, filter)
	if err != nil {
		s.Logger.Error("link stats", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	days := make([]map[string]any, len(stats.ByDay))
	for i, d := range stats.ByDay {
		days[i] = map[string]any{"day": d.Day.Format("2006-01-02"), "clicks": d.Clicks}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"links": stats.Links, "clicks": stats.Clicks, "humanClicks": stats.HumanClicks,
		"botClicks": stats.Clicks - stats.HumanClicks, "uniqueClickers": stats.UniqueClicks,
		"byDevice": stats.ByDevice, "byDay": days})
}

// followLink is the public redirect.
//
// HEAD never counts: it is what a messenger sends to build a preview, and a
// click that nobody made would inflate the number a customer pays attention to.
// A GET from a known preview fetcher is recorded but flagged as a bot.
func (s *Server) followLink(w http.ResponseWriter, r *http.Request) {
	pool := s.operatorPool()
	if pool == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "links are not available")
		return
	}
	link, tenant, err := store.ResolveLink(r.Context(), pool, chi.URLParam(r, "code"), time.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "This link does not exist.", http.StatusNotFound)
		return
	case errors.Is(err, store.ErrLinkExpired):
		http.Error(w, "This link has expired.", http.StatusGone)
		return
	case err != nil:
		s.Logger.Error("resolve link", "error", err.Error())
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	destination := link.Destination
	if link.AppendClickID {
		if parsed, perr := url.Parse(destination); perr == nil {
			query := parsed.Query()
			query.Set("click_id", link.Code)
			parsed.RawQuery = query.Encode()
			destination = parsed.String()
		}
	}
	if r.Method == http.MethodGet {
		agent := r.UserAgent()
		device, osName, isBot := classifyAgent(agent)
		sum := sha256.Sum256([]byte(r.RemoteAddr))
		if err := store.RecordClick(r.Context(), pool, tenant, link.Code, isBot,
			device, osName, hex.EncodeToString(sum[:8]), agent); err != nil {
			s.Logger.Warn("record click", "error", err.Error())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, destination, http.StatusFound)
}

var botMarkers = []string{"bot", "crawler", "spider", "preview", "facebookexternalhit",
	"whatsapp", "telegram", "slack", "curl/", "wget", "python-", "go-http-client",
	"headless", "monitor", "uptime", "lighthouse", "google-", "bing", "yandex",
	"skypeuripreview", "discord", "okhttp"}

// classifyAgent reads what it can from a User-Agent. It is a heuristic and says
// so in the API: a click flagged a bot is kept, not deleted.
func classifyAgent(agent string) (device, osName string, isBot bool) {
	lower := strings.ToLower(agent)
	if lower == "" {
		return "unknown", "unknown", true
	}
	for _, marker := range botMarkers {
		if strings.Contains(lower, marker) {
			isBot = true
			break
		}
	}
	switch {
	case strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet"):
		device = "tablet"
	case strings.Contains(lower, "mobi") || strings.Contains(lower, "iphone") ||
		strings.Contains(lower, "android"):
		device = "mobile"
	case lower != "":
		device = "desktop"
	}
	switch {
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") ||
		strings.Contains(lower, "ios"):
		osName = "ios"
	case strings.Contains(lower, "android"):
		osName = "android"
	case strings.Contains(lower, "windows"):
		osName = "windows"
	case strings.Contains(lower, "mac os") || strings.Contains(lower, "macintosh"):
		osName = "macos"
	case strings.Contains(lower, "linux"):
		osName = "linux"
	default:
		osName = "unknown"
	}
	return device, osName, isBot
}
