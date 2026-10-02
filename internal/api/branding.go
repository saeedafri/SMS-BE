package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/store"
)

// White-label branding. Mounted directly until the contract declares it.
// GET /v1/branding/public is unauthenticated: it is what a custom sign-in
// domain's page calls to theme itself, and returns only what is already public
// on that page.
func (s *Server) mountBrandingRoutes(r chi.Router) {
	r.Get("/v1/branding", s.getBranding)
	r.Put("/v1/branding", s.putBranding)
	r.Delete("/v1/branding", s.deleteBranding)
	r.Post("/v1/branding/verify-domain", s.verifyBrandingDomain)
	r.Get("/v1/branding/public", s.publicBranding)
}

var (
	hexColour  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	domainName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}$`)
)

type brandingBody struct {
	DisplayName    *string `json:"displayName"`
	LogoURL        *string `json:"logoUrl"`
	PrimaryColor   *string `json:"primaryColor"`
	SecondaryColor *string `json:"secondaryColor"`
	SupportEmail   *string `json:"supportEmail"`
	CustomDomain   *string `json:"customDomain"`
}

func (s *Server) lookupTXT(ctx context.Context, name string) ([]string, error) {
	if s.TXT != nil {
		return s.TXT(ctx, name)
	}
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func brandingOut(b store.Branding, found bool) map[string]any {
	verified := b.DomainVerifiedAt != nil
	out := map[string]any{"configured": found, "displayName": b.DisplayName, "logoUrl": b.LogoURL,
		"primaryColor": b.PrimaryColor, "secondaryColor": b.SecondaryColor, "supportEmail": b.SupportEmail,
		"customDomain": b.CustomDomain, "domainVerified": verified, "domainVerifiedAt": b.DomainVerifiedAt}
	if found && b.CustomDomain != nil && !verified {
		out["dnsRecord"] = map[string]string{"type": "TXT", "name": "_relay-verify." + *b.CustomDomain,
			"value": "relay-verify=" + b.DomainToken}
	}
	return out
}

func (s *Server) brandingIdentity(w http.ResponseWriter, r *http.Request, write bool) (store.Identity, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return identity, false
	}
	if write && !canManageSettings(identity.Role) {
		writeError(w, http.StatusForbidden, codeForbidden, "Member role cannot change branding.")
		return identity, false
	}
	return identity, true
}

func (s *Server) getBranding(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.brandingIdentity(w, r, false)
	if !ok {
		return
	}
	b, found, err := store.GetBranding(r.Context(), s.DB, identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, brandingOut(b, found))
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func (s *Server) putBranding(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.brandingIdentity(w, r, true)
	if !ok {
		return
	}
	var body brandingBody
	if !decodeStrict(w, r, &body) {
		return
	}
	b := store.Branding{DisplayName: trimmed(body.DisplayName), LogoURL: trimmed(body.LogoURL),
		PrimaryColor: trimmed(body.PrimaryColor), SecondaryColor: trimmed(body.SecondaryColor),
		SupportEmail: trimmed(body.SupportEmail), CustomDomain: trimmed(body.CustomDomain)}
	if problem := checkBranding(&b); problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	saved, err := store.SaveBranding(r.Context(), s.DB, identity, b)
	if errors.Is(err, store.ErrDomainTaken) {
		writeError(w, http.StatusConflict, "conflict", "Another account already uses that domain.")
		return
	}
	if err != nil {
		s.Logger.Error("save branding", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, brandingOut(saved, true))
}

func checkBranding(b *store.Branding) string {
	if b.DisplayName != nil && len(*b.DisplayName) > 80 {
		return "displayName is at most 80 characters."
	}
	if b.LogoURL != nil {
		parsed, err := url.Parse(*b.LogoURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
			len(*b.LogoURL) > 2048 {
			return "logoUrl must be an https URL."
		}
	}
	for name, colour := range map[string]*string{"primaryColor": b.PrimaryColor, "secondaryColor": b.SecondaryColor} {
		if colour != nil && !hexColour.MatchString(*colour) {
			return name + " must be a colour like #1A73E8."
		}
	}
	if b.SupportEmail != nil {
		if addr, err := mail.ParseAddress(*b.SupportEmail); err != nil || addr.Address != *b.SupportEmail {
			return "supportEmail must be an email address."
		}
	}
	if b.CustomDomain != nil {
		domain := strings.ToLower(*b.CustomDomain)
		if !domainName.MatchString(domain) || len(domain) > 253 || net.ParseIP(domain) != nil {
			return "customDomain must be a host name such as login.example.com."
		}
		b.CustomDomain = &domain
	}
	return ""
}

func (s *Server) deleteBranding(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.brandingIdentity(w, r, true)
	if !ok {
		return
	}
	if err := store.DeleteBranding(r.Context(), s.DB, identity); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyBrandingDomain checks the DNS record. Until it passes the domain is
// stored but never served.
func (s *Server) verifyBrandingDomain(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.brandingIdentity(w, r, true)
	if !ok {
		return
	}
	b, found, err := store.GetBranding(r.Context(), s.DB, identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	if !found || b.CustomDomain == nil {
		writeError(w, http.StatusConflict, "conflict", "Set a customDomain first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	records, _ := s.lookupTXT(ctx, "_relay-verify."+*b.CustomDomain)
	want := "relay-verify=" + b.DomainToken
	for _, record := range records {
		if strings.TrimSpace(record) == want {
			if err := store.MarkDomainVerified(r.Context(), s.DB, identity); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
				return
			}
			now := time.Now()
			b.DomainVerifiedAt = &now
			writeJSON(w, http.StatusOK, brandingOut(b, true))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domainVerified": false,
		"message":   "We could not find the TXT record yet. DNS changes can take a while to spread.",
		"dnsRecord": map[string]string{"type": "TXT", "name": "_relay-verify." + *b.CustomDomain, "value": want}})
}

// publicBranding answers for a verified custom domain only. Unknown, unverified
// and malformed hosts all get the same 404, so the endpoint cannot be used to
// find out which domains customers have claimed.
func (s *Server) publicBranding(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("host")))
	if !domainName.MatchString(host) {
		writeError(w, http.StatusNotFound, codeNotFound, "no branding for that host")
		return
	}
	b, found, err := store.BrandingForHost(r.Context(), s.operatorPool(), host)
	if err != nil || !found {
		writeError(w, http.StatusNotFound, codeNotFound, "no branding for that host")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"displayName": b.DisplayName, "logoUrl": b.LogoURL,
		"primaryColor": b.PrimaryColor, "secondaryColor": b.SecondaryColor, "supportEmail": b.SupportEmail})
}
