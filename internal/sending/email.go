package sending

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// EmailConnector carries EMAIL messages through an email provider: one
// recipient per call, subject, HTML and text. The provider's acceptance is
// "handed over", never delivery; delivery and every failure arrive later as
// events (ApplyEmailEvent).
type EmailConnector struct {
	Provider connector.EmailProvider
}

func (EmailConnector) Name() string { return "email" }

func (EmailConnector) Health(context.Context) connector.Health {
	return connector.Health{Healthy: true, Detail: "email provider"}
}

func (c EmailConnector) Submit(ctx context.Context, submissions []connector.Submission) ([]connector.Receipt, error) {
	receipts := make([]connector.Receipt, 0, len(submissions))
	for _, s := range submissions {
		var content struct {
			Subject  string  `json:"subject"`
			BodyHTML string  `json:"bodyHtml"`
			BodyText *string `json:"bodyText"`
		}
		_ = json.Unmarshal(s.Content, &content)
		text := htmlToText(content.BodyHTML)
		if content.BodyText != nil && strings.TrimSpace(*content.BodyText) != "" {
			text = *content.BodyText
		}
		body := content.BodyHTML
		if s.UnsubscribeURL != "" {
			body += `<p style="font-size:12px"><a href="` + html.EscapeString(s.UnsubscribeURL) +
				`">Unsubscribe</a></p>`
			text += "\n\nUnsubscribe: " + s.UnsubscribeURL
		}
		tenant := ""
		if s.Headers != nil {
			tenant = s.Headers[tenantHeader]
		}
		headers := map[string]string{}
		for name, value := range s.Headers {
			if name != tenantHeader {
				headers[name] = value
			}
		}
		providerID, err := c.Provider.Send(ctx, connector.EmailMessage{MessageID: s.MessageID,
			TenantID: tenant, From: s.From, FromName: s.FromName, To: s.Email,
			Subject: content.Subject, HTML: body, Text: text, Headers: headers})
		if err != nil {
			receipts = append(receipts, connector.Receipt{MessageID: s.MessageID,
				ErrorCode: "MessageRejected"})
			continue
		}
		receipts = append(receipts, connector.Receipt{MessageID: s.MessageID, Accepted: true,
			CarrierRef: providerID})
	}
	return receipts, nil
}

// tenantHeader carries the tenant from the send path to the provider's tags.
// It is a tag, never a header on the email.
const tenantHeader = "X-Relay-Tenant"

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)

// htmlToText is the text part derived from HTML when the template has none.
func htmlToText(body string) string {
	text := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n").
		Replace(body)
	return strings.TrimSpace(html.UnescapeString(htmlTag.ReplaceAllString(text, "")))
}

// emailSubmission fills an EMAIL submission: recipient, filled content,
// sender, and for marketing mail the one-click unsubscribe (RFC 2369 and
// 8058) and the footer link.
func (s *Service) emailSubmission(submission *connector.Submission, identity store.Identity,
	sender store.SenderID, template store.Template, email string, content []byte,
	tokens map[string]string) {

	submission.Email, submission.Content = email, content
	if sender.FromAddress != nil {
		submission.From = *sender.FromAddress
	}
	if sender.FromName != nil {
		submission.FromName = *sender.FromName
	}
	submission.Headers = map[string]string{tenantHeader: identity.TenantID.String()}
	token, marketing := tokens[email], isMarketingEmail(template)
	if marketing && token != "" {
		submission.Headers["List-Unsubscribe"] = "<" + s.APIBaseURL + "/v1/public/unsubscribe/" + token + ">"
		submission.Headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
		submission.UnsubscribeURL = s.AppBaseURL + "/unsubscribe/" + token
	}
}

func isMarketingEmail(template store.Template) bool {
	return template.Channel == "EMAIL" && template.Category != nil && *template.Category == "MARKETING"
}

// unsubscribeTokens mints the tokens a page of marketing email needs.
func (s *Service) unsubscribeTokens(ctx context.Context, identity store.Identity,
	template store.Template, emails []string) (map[string]string, error) {

	if !isMarketingEmail(template) || len(emails) == 0 {
		return nil, nil
	}
	return store.UnsubscribeTokens(ctx, s.DB, identity, emails, 365*24*time.Hour)
}

// emailIdentityStatus is the status a send judges an email sender by: its
// approval, and then its domain. A sender whose domain is not verified sends
// nothing.
func emailSenderStatus(sender store.SenderID) string {
	if sender.Channel != "EMAIL" || sender.Status != "approved" {
		return sender.Status
	}
	if status, _, _ := sender.EmailIdentity(); status != "verified" {
		domain := ""
		if sender.EmailDomain != nil {
			domain = *sender.EmailDomain
		}
		return "domain " + domain + " not verified"
	}
	return sender.Status
}

// ApplyEmailEvent turns one provider event into message state, suppression,
// consent and webhooks. Each event applies once: a redelivered event is
// recognised by its id and changes nothing.
func (s *Service) ApplyEmailEvent(ctx context.Context, event connector.EmailEvent) error {
	tenantID, err := uuid.Parse(event.TenantID)
	if err != nil {
		return fmt.Errorf("sending: email event without a tenant: %w", err)
	}
	messageID, err := uuid.Parse(event.MessageID)
	if err != nil {
		return fmt.Errorf("sending: email event without a message: %w", err)
	}
	identity := store.Identity{TenantID: tenantID}
	claimed, err := store.ClaimEmailEvent(ctx, s.DB, identity, event.EventID)
	if err != nil || !claimed {
		return err
	}
	record := store.EmailEventRecord{TenantID: tenantID, MessageID: messageID,
		EventID: event.EventID, Type: event.Type, At: event.At, BotLikely: event.BotLikely}
	if event.BounceType != "" {
		bounceType, subType := event.BounceType, event.BounceSubType
		record.BounceType, record.BounceSubType = &bounceType, &subType
	}
	if err := store.InsertEmailEvents(ctx, s.ClickHouse, []store.EmailEventRecord{record}); err != nil {
		return err
	}

	current, err := store.LoadMessageState(ctx, s.ClickHouse, tenantID, messageID)
	if err != nil {
		return err
	}
	email := ""
	if current.Email != nil {
		email = *current.Email
	}
	switch event.Type {
	case "delivery":
		return s.ApplyDeliveryReport(ctx, identity, connector.DeliveryReport{
			MessageID: event.MessageID, Delivered: true, OccurredAt: event.At})
	case "bounce":
		code := "Bounce:" + strings.ToUpper(event.BounceType[:1]) + event.BounceType[1:]
		if event.BounceSubType != "" && event.BounceType != "undetermined" {
			code += ":" + event.BounceSubType
		}
		if event.BounceType == "permanent" && !connector.SuppressedBounceSubTypes[event.BounceSubType] && email != "" {
			if _, err := store.AddSuppression(ctx, s.DB, identity, store.Suppression{
				Identity: email, Email: &email, Reason: "hard_bounce",
				Note: "Permanent bounce: " + event.BounceSubType}); err != nil {
				return err
			}
		}
		if err := s.ApplyDeliveryReport(ctx, identity, connector.DeliveryReport{
			MessageID: event.MessageID, ErrorCode: code, OccurredAt: event.At}); err != nil {
			return err
		}
		s.notifyEmail(ctx, identity, messageID, "message.bounced", map[string]any{
			"bounce": map[string]any{"type": event.BounceType, "subType": event.BounceSubType}})
	case "complaint":
		if email != "" {
			if _, err := store.AddSuppression(ctx, s.DB, identity, store.Suppression{
				Identity: email, Email: &email, Reason: "complaint",
				Note: "Complaint: " + event.FeedbackType}); err != nil {
				return err
			}
			if _, err := store.OptOutEmail(ctx, s.DB, identity, email); err != nil {
				return err
			}
		}
		s.notifyEmail(ctx, identity, messageID, "message.complained", map[string]any{
			"complaint": map[string]any{"feedbackType": event.FeedbackType}, "optedOut": true})
	case "reject", "rendering_failure":
		code := "Reject:BadContent"
		if event.Type == "rendering_failure" {
			code = "RenderingFailure"
		}
		return s.ApplyDeliveryReport(ctx, identity, connector.DeliveryReport{
			MessageID: event.MessageID, ErrorCode: code, OccurredAt: event.At})
	}
	return nil
}

// notifyEmail emits an email webhook through the server, which knows the
// endpoints. Fields name the message, never the address.
func (s *Service) notifyEmail(ctx context.Context, identity store.Identity, messageID uuid.UUID,
	event string, extra map[string]any) {

	if s.EmailEvents == nil {
		return
	}
	current, err := store.LoadMessageState(ctx, s.ClickHouse, identity.TenantID, messageID)
	if err != nil {
		return
	}
	s.EmailEvents(ctx, identity, event, current, extra)
}

// DrainFakeEmailEvents applies the fake provider's queued events, as a real
// deployment would receive them from the provider's event destination.
func (s *Service) DrainFakeEmailEvents(ctx context.Context, fake *connector.FakeEmail) (int, error) {
	applied := 0
	for _, event := range fake.DrainEvents() {
		if err := s.ApplyEmailEvent(ctx, event); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// emailPaused says whether a tenant's email sending is stopped: an operator
// pause, or the provider's enforcement. Asked only for EMAIL.
func (s *Service) emailPaused(ctx context.Context, identity store.Identity, channel string) (bool, error) {
	if channel != "EMAIL" {
		return false, nil
	}
	status, err := store.EmailSendingStatus(ctx, s.DB, identity.TenantID)
	if err != nil {
		return false, err
	}
	return status == "paused" || status == "enforced", nil
}
