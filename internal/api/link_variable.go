package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The {{link}} variable at the API: a campaign (or a test send) says where its
// per-recipient tracked links go. BACKEND_REQUEST_link-variable.md, L2, L4, L8.

const (
	linkNeeded = "This message uses {{link}}, so the campaign needs a link. Add one under link."
	linkUnused = "This campaign has a link, but its message does not use {{link}}. " +
		"Add {{link}} to the template or remove the link."
)

type requestHostKey struct{}

// withRequestHost hands a generated handler the Host it was called on, which
// the "not Relay itself" rule compares a destination with. Generated handlers
// are given a context, not the request.
func withRequestHost(next gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		return next(context.WithValue(ctx, requestHostKey{}, r.Host), w, r, request)
	}
}

// linkBaseFor is where short links point: LINK_BASE_URL, or, when it is unset,
// the origin the request came in on, which is what every link was built on
// before the setting existed.
func (s *Server) linkBaseFor(r *http.Request) string {
	if s.LinkBaseURL != "" {
		return s.LinkBaseURL
	}
	return linkBase(r)
}

// selfHosts are the hosts that are Relay itself, so a destination on one is a
// redirect loop: the host the caller used, and the one links are built on.
func (s *Server) selfHosts(requestHost string) []string {
	return []string{requestHost, sending.LinkHost(s.LinkBaseURL)}
}

// linkConfig is GET /v1/links/config: the host a customer whitelists at their
// DLT account, and the code length that makes a finished link's size exact.
func (s *Server) linkConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.linkIdentity(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, gen.ShortLinkConfig{BaseUrl: s.linkBaseFor(r),
		CodeLength: sending.LinkCodeLength})
}

// linkFor applies the link rules, in the order the frontend's mock runs them:
// a message that needs a link and has none; a link that is not a link-shaped
// object; one carrying a field a link does not have; a link nobody's message
// uses; then the destination, label and expiry exactly as POST /v1/links judges
// them, through the same function, so the two refuse in the same words.
//
// raw is the link as sent, nil when absent or null. templates are every string
// the message is made of — both legs of a campaign.
func (s *Server) linkFor(identity store.Identity, requestHost string, raw json.RawMessage,
	templates ...store.Template) (*store.LinkSpec, string) {

	wanted := false
	for _, template := range templates {
		wanted = wanted || sending.UsesLink(template)
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		if wanted {
			return nil, linkNeeded
		}
		return nil, ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, "link must be an object with a destination."
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch name {
		case "destination", "appendClickId", "expiresAt", "label":
		default:
			return nil, `link has no field "` + name + `".`
		}
	}
	if !wanted {
		return nil, linkUnused
	}
	var body linkBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "link: " + err.Error()
	}
	made, problem := buildLink(s.selfHosts(requestHost), identity, body)
	if problem != "" {
		return nil, problem
	}
	return &store.LinkSpec{Destination: made.Destination, AppendClickID: made.AppendClickID,
		ExpiresAt: made.ExpiresAt, Label: made.Label}, ""
}

// campaignLink is linkFor over a campaign being created: both legs' templates,
// and a retry's parent's link when the retry names none of its own.
func (s *Server) campaignLink(ctx context.Context, identity store.Identity,
	body *gen.CreateCampaignJSONBody_Link, campaign store.Campaign) (*store.LinkSpec, string, error) {

	var raw json.RawMessage
	if body != nil {
		raw, _ = body.MarshalJSON()
	}
	if len(raw) == 0 && campaign.RetryOf != nil {
		parent, err := store.GetCampaign(ctx, s.DB, identity, *campaign.RetryOf)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, "", err
		}
		if parent.Link != nil {
			raw, _ = json.Marshal(parent.Link)
		}
	}
	var templates []store.Template
	for _, id := range []*uuid.UUID{&campaign.TemplateID, campaign.FallbackTemplateID} {
		if id == nil {
			continue
		}
		template, err := store.GetTemplate(ctx, s.DB, identity, *id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		templates = append(templates, template)
	}
	host, _ := ctx.Value(requestHostKey{}).(string)
	spec, problem := s.linkFor(identity, host, raw, templates...)
	return spec, problem, nil
}

// estimateLink is the link an estimate is asked about. The estimate refuses
// nothing about it: it only needs to know whether there is one.
func estimateLink(body *gen.EstimateCampaignJSONBody_Link) *store.LinkSpec {
	if body == nil {
		return nil
	}
	spec, err := body.AsShortLinkSpec()
	if err != nil || strings.TrimSpace(spec.Destination) == "" {
		return nil
	}
	return &store.LinkSpec{Destination: spec.Destination}
}

// campaignLinkOut is a stored link on the contract's Campaign.
func campaignLinkOut(spec *store.LinkSpec) *gen.Campaign_Link {
	if spec == nil {
		return nil
	}
	var out gen.Campaign_Link
	appendClickID := spec.AppendClickID
	_ = out.FromShortLinkSpec(gen.ShortLinkSpec{Destination: spec.Destination,
		AppendClickId: &appendClickID, ExpiresAt: spec.ExpiresAt, Label: spec.Label})
	return &out
}
