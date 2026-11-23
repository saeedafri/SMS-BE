package api

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// A tenant's own domain for its short links. It is usable only once active:
// both DNS records verified, then a certificate serving the name. validate
// re-checks and moves one step per call, so the screen can show each stage.

var linkDomainPattern = regexp.MustCompile(
	`^(?i)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

const linkDomainVerifyPrefix = "_relay-verify."

func (s *Server) domainChecker() connector.DomainChecker {
	if s.Domains != nil {
		return s.Domains
	}
	return connector.NetDomainChecker{}
}

func (s *Server) linkDomainOut(domain store.LinkDomain) gen.LinkDomain {
	out := gen.LinkDomain{Id: domain.ID, Domain: domain.Domain,
		Status: gen.LinkDomainStatus(domain.Status), FailureReason: domain.FailureReason,
		CreatedAt: domain.CreatedAt}
	out.DnsRecords = []struct {
		Host   string                         `json:"host"`
		Status gen.LinkDomainDnsRecordsStatus `json:"status"`
		Type   gen.LinkDomainDnsRecordsType   `json:"type"`
		Value  string                         `json:"value"`
	}{
		{Host: domain.Domain, Type: "CNAME", Value: s.LinkDomainTarget,
			Status: gen.LinkDomainDnsRecordsStatus(domain.CNAMEStatus)},
		{Host: linkDomainVerifyPrefix + domain.Domain, Type: "TXT",
			Value:  "relay-verify=" + domain.VerifyToken,
			Status: gen.LinkDomainDnsRecordsStatus(domain.TXTStatus)},
	}
	return out
}

func (s *Server) ListLinkDomains(ctx context.Context, request gen.ListLinkDomainsRequestObject) (
	gen.ListLinkDomainsResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	page, valid := pageNumber(request.Params.Page)
	if !valid {
		return gen.ListLinkDomains422JSONResponse(errorBody(codeValidation, pageTooLow)), nil
	}
	limit := 20
	if request.Params.Limit != nil {
		if *request.Params.Limit < 1 || *request.Params.Limit > 200 {
			return gen.ListLinkDomains422JSONResponse(errorBody(codeValidation, limitOutOfRange)), nil
		}
		limit = *request.Params.Limit
	}
	domains, total, err := store.ListLinkDomains(ctx, s.DB, identity, page, limit)
	if err != nil {
		return nil, err
	}
	out := make([]gen.LinkDomain, 0, len(domains))
	for _, domain := range domains {
		out = append(out, s.linkDomainOut(domain))
	}
	return gen.ListLinkDomains200JSONResponse{Domains: out, Total: total}, nil
}

func (s *Server) CreateLinkDomain(ctx context.Context, request gen.CreateLinkDomainRequestObject) (
	gen.CreateLinkDomainResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.CreateLinkDomain403JSONResponse(errorBody(codeForbidden,
			"Your role cannot change link domains.")), nil
	}
	domain := ""
	if request.Body != nil {
		domain = strings.ToLower(strings.TrimSpace(request.Body.Domain))
	}
	if len(domain) > 253 || !linkDomainPattern.MatchString(domain) {
		return gen.CreateLinkDomain422JSONResponse(errorBody(codeValidation,
			"domain must be a bare host name such as go.acme.example, with no scheme, path or port.")), nil
	}
	created, err := store.CreateLinkDomain(ctx, s.DB, identity, domain)
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateLinkDomain409JSONResponse(errorBody(codeConflict,
			"That domain is already registered.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateLinkDomain201JSONResponse(s.linkDomainOut(created)), nil
}

func (s *Server) GetLinkDomain(ctx context.Context, request gen.GetLinkDomainRequestObject) (
	gen.GetLinkDomainResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	domain, err := store.GetLinkDomain(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetLinkDomain404JSONResponse(errorBody(codeNotFound, "No such link domain.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetLinkDomain200JSONResponse(s.linkDomainOut(domain)), nil
}

func (s *Server) DeleteLinkDomain(ctx context.Context, request gen.DeleteLinkDomainRequestObject) (
	gen.DeleteLinkDomainResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.DeleteLinkDomain403JSONResponse(errorBody(codeForbidden,
			"Your role cannot change link domains.")), nil
	}
	err := store.DeleteLinkDomain(ctx, s.DB, identity, request.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return gen.DeleteLinkDomain404JSONResponse(errorBody(codeNotFound, "No such link domain.")), nil
	case errors.Is(err, store.ErrConflict):
		return gen.DeleteLinkDomain409JSONResponse(errorBody(codeConflict,
			"Short links still use this domain.")), nil
	case err != nil:
		return nil, err
	}
	return gen.DeleteLinkDomain204Response{}, nil
}

// ValidateLinkDomain re-reads both DNS records. While either is missing the
// domain waits in pending_dns; one that does not match fails it. Once both
// verify it moves one step per call: validating, then issuing_certificate,
// then active once the name really serves HTTPS.
func (s *Server) ValidateLinkDomain(ctx context.Context, request gen.ValidateLinkDomainRequestObject) (
	gen.ValidateLinkDomainResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.ValidateLinkDomain403JSONResponse(errorBody(codeForbidden,
			"Your role cannot change link domains.")), nil
	}
	domain, err := store.GetLinkDomain(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ValidateLinkDomain404JSONResponse(errorBody(codeNotFound, "No such link domain.")), nil
	}
	if err != nil {
		return nil, err
	}
	if domain.Status != "active" {
		s.checkLinkDomain(ctx, &domain)
		if err := store.SaveLinkDomainCheck(ctx, s.DB, identity, domain); err != nil {
			return nil, err
		}
	}
	return gen.ValidateLinkDomain200JSONResponse(s.linkDomainOut(domain)), nil
}

func (s *Server) checkLinkDomain(ctx context.Context, domain *store.LinkDomain) {
	checker := s.domainChecker()
	domain.CNAMEStatus = "pending"
	if cname, err := checker.CNAME(ctx, domain.Domain); err == nil && cname != "" && cname != domain.Domain {
		domain.CNAMEStatus = "failed"
		if cname == strings.TrimSuffix(strings.ToLower(s.LinkDomainTarget), ".") {
			domain.CNAMEStatus = "verified"
		}
	}
	domain.TXTStatus = "pending"
	if records, err := checker.TXT(ctx, linkDomainVerifyPrefix+domain.Domain); err == nil && len(records) > 0 {
		domain.TXTStatus = "failed"
		for _, record := range records {
			if strings.TrimSpace(record) == "relay-verify="+domain.VerifyToken {
				domain.TXTStatus = "verified"
			}
		}
	}

	domain.FailureReason = nil
	switch {
	case domain.CNAMEStatus == "failed" || domain.TXTStatus == "failed":
		reason := "A DNS record does not match. Check it is exactly as shown, then check again."
		domain.Status, domain.FailureReason = "failed", &reason
	case domain.CNAMEStatus == "pending" || domain.TXTStatus == "pending":
		domain.Status = "pending_dns"
	case domain.Status == "pending_dns" || domain.Status == "failed":
		domain.Status = "validating"
	case domain.Status == "validating":
		domain.Status = "issuing_certificate"
	case domain.Status == "issuing_certificate" && checker.ServesHTTPS(ctx, domain.Domain):
		domain.Status = "active"
	}
}
