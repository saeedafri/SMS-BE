package api

import (
	"context"
	"time"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/store"
)

// receiveInboundSMS files a handset's reply in the inbox of the tenant holding
// the header it was sent to. ReceiveInboundMessage suppresses the number when
// the reply is a stop keyword, before anything else reads it.
//
// ponytail: a header two tenants both hold is logged and dropped rather than
// guessed; resolve it from the last message sent to that number if it happens.
func (s *Server) receiveInboundSMS(in connector.InboundSMS) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := s.Logger.With("carrier", in.Carrier, "to", in.To)

	holders, err := store.HoldersOfSMSHeader(ctx, s.operatorPool(), in.To)
	if err != nil {
		log.Error("inbound SMS dropped: sender lookup failed", "error", err)
		return
	}
	if len(holders) != 1 {
		log.Warn("inbound SMS not attributed to a tenant", "holders", len(holders))
		return
	}
	holder := holders[0]
	identity := store.Identity{TenantID: holder.TenantID}

	msisdn, ok := audience.NormaliseMsisdn(in.From, holder.Country)
	if !ok {
		if msisdn, ok = audience.NormaliseE164(in.From); !ok {
			log.Warn("inbound SMS dropped: unreadable source address", "tenant", holder.TenantID)
			return
		}
	}
	s.fileReply(ctx, identity, msisdn, holder.Country, "SMS", in.Text)
}

// fileReply puts a contact's reply in the tenant's inbox and tells the tenant's
// webhooks. ReceiveInboundMessage suppresses the number first when the reply is
// a stop keyword.
func (s *Server) fileReply(ctx context.Context, identity store.Identity,
	msisdn, country, channel, text string) {

	log := s.Logger.With("tenant", identity.TenantID, "channel", channel)
	contactID, err := store.EnsureContact(ctx, s.DB, identity, msisdn, country)
	if err != nil {
		log.Error("inbound reply dropped: contact", "error", err)
		return
	}
	message, err := store.ReceiveInboundMessage(ctx, s.DB, identity, contactID, channel, text)
	if err != nil {
		log.Error("inbound reply dropped: inbox", "error", err)
		return
	}
	s.emitWebhookEvent(ctx, identity, "message.inbound", map[string]any{
		"messageId":      message.ID,
		"conversationId": message.ConversationID,
		"contactId":      contactID,
		"channel":        channel,
		"body":           message.Body,
		"keywordMatched": message.KeywordMatched,
		"receivedAt":     message.CreatedAt,
	})
}
