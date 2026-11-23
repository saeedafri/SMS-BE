package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// Email on Amazon SES (decision L-4): a domain identity per sender, the
// provider's events turned into message state, a public unsubscribe, a health
// read and the operator's pause. Every provider call goes through
// connector.EmailProvider; the fake stands in while no credentials exist.

var (
	defaultEmailOnce sync.Once
	defaultEmail     *connector.FakeEmail
)

// emailProvider is the configured provider, or the shared fake.
func (s *Server) emailProvider() connector.EmailProvider {
	if s.Email != nil {
		return s.Email
	}
	defaultEmailOnce.Do(func() { defaultEmail = connector.NewFakeEmail() })
	return defaultEmail
}

func (s *Server) emailRegion() string {
	if s.EmailRegion != "" {
		return s.EmailRegion
	}
	return "ap-south-1"
}

// emailDNSRecords are the records a domain publishes, in the order the wizard
// shows them: three DKIM CNAMEs, the MAIL FROM MX and SPF, then DMARC, which
// is advised and never required.
func emailDNSRecords(domain, mailFrom, region string, tokens []string) []store.SenderDNSRecord {
	cname, txt, mx := "CNAME", "TXT", "MX"
	priority := 10
	var records []store.SenderDNSRecord
	for _, token := range tokens {
		records = append(records, store.SenderDNSRecord{Type: "DKIM", DNSType: &cname, Required: true,
			Host: token + "._domainkey." + domain, Value: token + ".dkim.amazonses.com", Status: "pending"})
	}
	return append(records,
		store.SenderDNSRecord{Type: "MAIL_FROM_MX", DNSType: &mx, Priority: &priority, Required: true,
			Host: mailFrom, Value: "feedback-smtp." + region + ".amazonses.com", Status: "pending"},
		store.SenderDNSRecord{Type: "MAIL_FROM_SPF", DNSType: &txt, Required: true,
			Host: mailFrom, Value: "v=spf1 include:amazonses.com ~all", Status: "pending"},
		store.SenderDNSRecord{Type: "DMARC", DNSType: &txt, Required: false,
			Host: "_dmarc." + domain, Value: "v=DMARC1; p=none;", Status: "pending"})
}

// emailIdentityFor registers a new email sender's domain with the provider
// and returns the records it must publish.
func (s *Server) emailIdentityFor(ctx context.Context, domain string) ([]store.SenderDNSRecord, string, error) {
	mailFrom := "mail." + domain
	tokens, err := s.emailProvider().CreateIdentity(ctx, domain, mailFrom)
	if err != nil {
		return nil, "", err
	}
	return emailDNSRecords(domain, mailFrom, s.emailRegion(), tokens), mailFrom, nil
}

func emailRecordsOut(records []store.SenderDNSRecord) []gen.EmailDnsRecord {
	out := make([]gen.EmailDnsRecord, 0, len(records))
	for _, record := range records {
		required := record.Required
		row := gen.EmailDnsRecord{Type: gen.EmailDnsRecordType(record.Type), Host: record.Host,
			Value: record.Value, Status: gen.EmailDnsRecordStatus(record.Status),
			Priority: record.Priority, Required: &required, LastCheckedAt: record.LastCheckedAt}
		if record.DNSType != nil {
			recordType := gen.EmailDnsRecordRecordType(*record.DNSType)
			row.RecordType = &recordType
		}
		out = append(out, row)
	}
	return out
}

// emailIdentityOut is an email sender's domain state, derived from its records.
func emailIdentityOut(sender store.SenderID) *gen.EmailIdentity {
	if sender.Channel != "EMAIL" {
		return nil
	}
	status, dkim, mailFrom := sender.EmailIdentity()
	out := &gen.EmailIdentity{Status: gen.EmailIdentityStatus(status),
		DkimStatus: gen.EmailIdentityStatus(dkim), MailFromStatus: gen.EmailIdentityStatus(mailFrom),
		MailFromDomain: sender.MailFromDomain, LastCheckedAt: sender.EmailCheckedAt}
	for _, record := range sender.DNSRecords {
		if record.Type == "DKIM" && record.DNSType != nil {
			length := gen.EmailIdentitySigningKeyLength(2048)
			out.SigningKeyLength = &length
			break
		}
	}
	return out
}

// VerifyEmailDns re-reads the domain: the provider's DKIM and MAIL FROM state,
// and DMARC resolved here because the provider does not check it. It stamps
// when it looked and never creates a record. 401, then 404, then 409.
func (s *Server) VerifyEmailDns(ctx context.Context, request gen.VerifyEmailDnsRequestObject) (
	gen.VerifyEmailDnsResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.VerifyEmailDns401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.VerifyEmailDns404JSONResponse(errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	if sender.Channel != "EMAIL" || sender.EmailDomain == nil {
		return gen.VerifyEmailDns409JSONResponse(errorBody(codeConflict,
			"Only an email sender has DNS records to check.")), nil
	}
	domain := *sender.EmailDomain
	state, err := s.emailProvider().Identity(ctx, domain)
	if err != nil {
		return nil, err
	}
	recordStatus := func(provider string) string {
		switch provider {
		case "verified", "failed":
			return provider
		}
		return "pending"
	}
	dmarc := "pending"
	if records, err := s.domainChecker().TXT(ctx, "_dmarc."+domain); err == nil {
		for _, record := range records {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(record)), "V=DMARC1") {
				dmarc = "verified"
			}
		}
	}
	statuses := map[string]string{"DKIM": recordStatus(state.DKIM),
		"MAIL_FROM_MX": recordStatus(state.MailFrom), "MAIL_FROM_SPF": recordStatus(state.MailFrom),
		"DMARC": dmarc}
	if err := store.SaveEmailCheck(ctx, s.DB, identity, sender.ID, statuses, s.now().UTC()); err != nil {
		return nil, err
	}
	checked, err := store.GetSenderID(ctx, s.DB, identity, sender.ID)
	if err != nil {
		return nil, err
	}
	return gen.VerifyEmailDns200JSONResponse(senderResponse(checked)), nil
}

// emailHealthWindow is the window the bounce and complaint rates cover.
const emailHealthWindow = 7

// emailHealth is EmailSendingHealth with its rates stated as null when there
// is nothing to divide: never a healthy zero, and never left out.
type emailHealth struct {
	SendingStatus string                       `json:"sendingStatus"`
	BounceRate    *float64                     `json:"bounceRate"`
	ComplaintRate *float64                     `json:"complaintRate"`
	WindowDays    int                          `json:"windowDays"`
	Findings      []gen.EmailReputationFinding `json:"findings"`
}

func (s *Server) emailHealthFor(ctx context.Context, tenantID uuid.UUID) (emailHealth, error) {
	status, err := store.EmailSendingStatus(ctx, s.DB, tenantID)
	if err != nil {
		return emailHealth{}, err
	}
	health := emailHealth{SendingStatus: status, WindowDays: emailHealthWindow,
		Findings: []gen.EmailReputationFinding{}}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return health, nil
	}
	suppressed := make([]string, 0, len(connector.SuppressedBounceSubTypes))
	for subType := range connector.SuppressedBounceSubTypes {
		suppressed = append(suppressed, subType)
	}
	counts, err := store.QueryEmailHealth(ctx, conn, tenantID,
		s.now().AddDate(0, 0, -emailHealthWindow), suppressed)
	if err != nil {
		return emailHealth{}, err
	}
	if divisor := counts.Deliveries + counts.Bounces; divisor > 0 {
		rate := float64(counts.Bounces) / float64(divisor)
		health.BounceRate = &rate
	}
	if counts.Deliveries > 0 {
		rate := float64(counts.Complaints) / float64(counts.Deliveries)
		health.ComplaintRate = &rate
	}
	return health, nil
}

type healthResponse emailHealth

func (h healthResponse) VisitGetEmailSendingHealthResponse(w http.ResponseWriter) error {
	writeJSON(w, http.StatusOK, emailHealth(h))
	return nil
}

func (s *Server) GetEmailSendingHealth(ctx context.Context, _ gen.GetEmailSendingHealthRequestObject) (
	gen.GetEmailSendingHealthResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetEmailSendingHealth401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	if _, err := store.FirstEmailSender(ctx, s.DB, identity); errors.Is(err, store.ErrNotFound) {
		return gen.GetEmailSendingHealth404JSONResponse(errorBody(codeNotFound,
			"This tenant has no email sender.")), nil
	} else if err != nil {
		return nil, err
	}
	health, err := s.emailHealthFor(ctx, identity.TenantID)
	if err != nil {
		return nil, err
	}
	return healthResponse(health), nil
}

// operatorEmail is OperatorTenantEmail with its health's null rates kept.
type operatorEmail struct {
	Identity *gen.EmailIdentity `json:"identity"`
	Health   emailHealth        `json:"health"`
	Quota    *connector.EmailQuota
}

func (o operatorEmail) MarshalJSON() ([]byte, error) {
	var quota any
	if o.Quota != nil {
		quota = map[string]any{"sendQuota24h": o.Quota.SendQuota24h,
			"sendRatePerSecond": o.Quota.SendRatePerSecond, "sentLast24h": o.Quota.SentLast24h}
	}
	return json.Marshal(map[string]any{"identity": o.Identity, "health": o.Health, "quota": quota})
}

type operatorEmailResponse struct{ body operatorEmail }

func (r operatorEmailResponse) VisitGetOperatorTenantEmailResponse(w http.ResponseWriter) error {
	writeJSON(w, http.StatusOK, r.body)
	return nil
}

func (r operatorEmailResponse) VisitPauseTenantEmailResponse(w http.ResponseWriter) error {
	writeJSON(w, http.StatusOK, r.body)
	return nil
}

func (r operatorEmailResponse) VisitResumeTenantEmailResponse(w http.ResponseWriter) error {
	writeJSON(w, http.StatusOK, r.body)
	return nil
}

func (s *Server) operatorEmailFor(ctx context.Context, tenantID uuid.UUID) (operatorEmail, error) {
	health, err := s.emailHealthFor(ctx, tenantID)
	if err != nil {
		return operatorEmail{}, err
	}
	out := operatorEmail{Health: health}
	if sender, err := store.FirstEmailSender(ctx, s.DB, store.Identity{TenantID: tenantID}); err == nil {
		out.Identity = emailIdentityOut(sender)
	} else if !errors.Is(err, store.ErrNotFound) {
		return operatorEmail{}, err
	}
	if quota, err := s.emailProvider().Quota(ctx); err == nil {
		out.Quota = &quota
	}
	return out, nil
}

func (s *Server) GetOperatorTenantEmail(ctx context.Context, request gen.GetOperatorTenantEmailRequestObject) (
	gen.GetOperatorTenantEmailResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorTenantEmail401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	if _, err := store.GetTenant(ctx, s.operatorPool(), request.Id); errors.Is(err, store.ErrNotFound) {
		return gen.GetOperatorTenantEmail404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	} else if err != nil {
		return nil, err
	}
	body, err := s.operatorEmailFor(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return operatorEmailResponse{body}, nil
}

// changeEmailStanding pauses or resumes a tenant's email. A repeat of either,
// or either on a tenant the provider stopped, is a conflict.
func (s *Server) changeEmailStanding(ctx context.Context, tenantID uuid.UUID, pause bool) (
	operatorEmail, int, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return operatorEmail{}, http.StatusUnauthorized, nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		return operatorEmail{}, http.StatusNotFound, nil
	}
	if err != nil {
		return operatorEmail{}, 0, err
	}
	from, to, action, verb := []string{"enabled", "reinstated"}, "paused", "tenant.email_pause", "Paused"
	if !pause {
		from, to, action, verb = []string{"paused"}, "reinstated", "tenant.email_resume", "Resumed"
	}
	if err := store.SetEmailSendingStatus(ctx, s.operatorPool(), tenantID, from, to); errors.Is(err, store.ErrConflict) {
		return operatorEmail{}, http.StatusConflict, nil
	} else if err != nil {
		return operatorEmail{}, 0, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action, &tenantID, tenant.Name,
		tenant.Name, fmt.Sprintf("%s email sending for %s", verb, tenant.Name)); err != nil {
		return operatorEmail{}, 0, err
	}
	body, err := s.operatorEmailFor(ctx, tenantID)
	return body, http.StatusOK, err
}

func (s *Server) PauseTenantEmail(ctx context.Context, request gen.PauseTenantEmailRequestObject) (
	gen.PauseTenantEmailResponseObject, error) {

	body, status, err := s.changeEmailStanding(ctx, request.Id, true)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusUnauthorized:
		return gen.PauseTenantEmail401JSONResponse(errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	case status == http.StatusNotFound:
		return gen.PauseTenantEmail404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	case status == http.StatusConflict:
		return gen.PauseTenantEmail409JSONResponse(errorBody(codeConflict,
			"Email sending is not running for this tenant, so it cannot be paused.")), nil
	}
	return operatorEmailResponse{body}, nil
}

func (s *Server) ResumeTenantEmail(ctx context.Context, request gen.ResumeTenantEmailRequestObject) (
	gen.ResumeTenantEmailResponseObject, error) {

	body, status, err := s.changeEmailStanding(ctx, request.Id, false)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusUnauthorized:
		return gen.ResumeTenantEmail401JSONResponse(errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	case status == http.StatusNotFound:
		return gen.ResumeTenantEmail404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	case status == http.StatusConflict:
		return gen.ResumeTenantEmail409JSONResponse(errorBody(codeConflict,
			"Email sending is not paused for this tenant, so it cannot be resumed.")), nil
	}
	return operatorEmailResponse{body}, nil
}

// ---- the public unsubscribe -------------------------------------------------------

func unsubscribeNotFound() gen.Error {
	return errorBody(codeNotFound, "This unsubscribe link is not valid.")
}

// maskEmail hides everything but the first character of the local part.
func maskEmail(email string) string {
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" {
		return "***"
	}
	return local[:1] + "***@" + domain
}

func (s *Server) unsubscribeInfo(ctx context.Context, token string) (gen.PublicUnsubscribeInfo, store.UnsubscribeTarget, bool, error) {
	target, err := store.FindUnsubscribeToken(ctx, s.operatorPool(), token)
	if errors.Is(err, store.ErrNotFound) {
		return gen.PublicUnsubscribeInfo{}, target, false, nil
	}
	if err != nil {
		return gen.PublicUnsubscribeInfo{}, target, false, err
	}
	optedOut, err := store.EmailOptedOut(ctx, s.DB, store.Identity{TenantID: target.TenantID}, target.Email)
	if err != nil {
		return gen.PublicUnsubscribeInfo{}, target, false, err
	}
	return gen.PublicUnsubscribeInfo{TenantName: target.TenantName, MaskedEmail: maskEmail(target.Email),
		AlreadyUnsubscribed: optedOut}, target, true, nil
}

// GetPublicUnsubscribe answers what an unsubscribe link is for. Unknown and
// expired tokens get one identical 404, so a token cannot be probed, and the
// full address is never returned.
func (s *Server) GetPublicUnsubscribe(ctx context.Context, request gen.GetPublicUnsubscribeRequestObject) (
	gen.GetPublicUnsubscribeResponseObject, error) {

	info, _, found, err := s.unsubscribeInfo(ctx, request.Token)
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.GetPublicUnsubscribe404JSONResponse(unsubscribeNotFound()), nil
	}
	return gen.GetPublicUnsubscribe200JSONResponse(info), nil
}

// ConfirmPublicUnsubscribe is the one-click target: no body, idempotent. It
// opts the address out of email and says so to the tenant's endpoints.
func (s *Server) ConfirmPublicUnsubscribe(ctx context.Context, request gen.ConfirmPublicUnsubscribeRequestObject) (
	gen.ConfirmPublicUnsubscribeResponseObject, error) {

	info, target, found, err := s.unsubscribeInfo(ctx, request.Token)
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.ConfirmPublicUnsubscribe404JSONResponse(unsubscribeNotFound()), nil
	}
	identity := store.Identity{TenantID: target.TenantID}
	changed, err := store.OptOutEmail(ctx, s.DB, identity, target.Email)
	if err != nil {
		return nil, err
	}
	if changed > 0 {
		s.emitWebhookEventAsync(identity, "message.unsubscribed", map[string]any{
			"channel": "EMAIL", "reason": "one_click", "optedOut": true})
	}
	info.AlreadyUnsubscribed = true
	return gen.ConfirmPublicUnsubscribe200JSONResponse(info), nil
}

// emitWebhookEventAsync delivers an event without holding the request open.
func (s *Server) emitWebhookEventAsync(identity store.Identity, event string, data map[string]any) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.emitWebhookEvent(ctx, identity, event, data)
	}()
}

// EmailEventWebhook is the sending service's hook for message.bounced and
// message.complained: the message, its channel and status, what happened,
// and for a bounce the explained failure. Never the address.
func (s *Server) EmailEventWebhook(_ context.Context, identity store.Identity, event string,
	record store.MessageRecord, extra map[string]any) {

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data := map[string]any{"messageId": record.ID, "channel": record.Channel,
			"status": record.Status}
		for key, value := range extra {
			data[key] = value
		}
		if event == "message.bounced" {
			data["failure"] = messageFailure(s.failureRules(ctx), record)
		}
		s.emitWebhookEvent(ctx, identity, event, data)
	}()
}

// emailEventsOut are the provider's events on a message, in order.
func emailEventsOut(events []store.EmailEventRecord) *[]gen.EmailEvent {
	out := make([]gen.EmailEvent, 0, len(events))
	for _, e := range events {
		row := gen.EmailEvent{Type: gen.EmailEventType(e.Type), At: e.At, Detail: e.Detail,
			BounceSubType: e.BounceSubType}
		if e.BounceType != nil {
			bounceType := gen.EmailEventBounceType(*e.BounceType)
			row.BounceType = &bounceType
		}
		if e.Type == "open" || e.Type == "click" {
			bot := e.BotLikely
			row.BotLikely = &bot
		}
		out = append(out, row)
	}
	return &out
}
