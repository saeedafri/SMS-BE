package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func senderResponse(s store.SenderID) gen.SenderId {
	sender := gen.SenderId{
		Id:              s.ID,
		Header:          s.Header,
		Channel:         gen.ChannelId(s.Channel),
		Country:         gen.CountryCode(s.Country),
		Status:          gen.ApprovalStatus(s.Status),
		RejectionReason: s.RejectionReason,
		RegistrationId:  s.ExternalID,
		WabaId:          s.WabaID,
		DisplayName:     s.DisplayName,
		PhoneNumber:     s.PhoneNumber,
		EmailDomain:     s.EmailDomain,
		FromAddress:     s.FromAddress,
		FromName:        s.FromName,
		CallerIdNumber:  s.CallerIDNumber,
		CreatedAt:       s.CreatedAt,
	}
	// DNS records exist only for email senders. Absent rather than an empty
	// array for other channels: the contract marks the field optional, and an
	// empty list would render as "you have records to publish, none of them
	// verified" on a screen that should not show the section at all.
	if len(s.DNSRecords) > 0 {
		records := make([]gen.EmailDnsRecord, 0, len(s.DNSRecords))
		for _, record := range s.DNSRecords {
			records = append(records, gen.EmailDnsRecord{
				Type:   gen.EmailDnsRecordType(record.Type),
				Host:   record.Host,
				Value:  record.Value,
				Status: gen.EmailDnsRecordStatus(record.Status),
			})
		}
		sender.DnsRecords = &records
	}

	// WhatsApp health, when Meta has assigned it. Left absent rather than sent
	// as a zero value: "no rating yet" and "rated red" are different facts, and
	// the senders list must not present the first as the second.
	if s.QualityRating != nil {
		rating := gen.WaQualityRating(*s.QualityRating)
		// The database CHECK already restricts this column to the contract's
		// three values, so an unknown one means the constraint and the contract
		// have drifted apart. Dropping the field is the safe half of that: the
		// frontend resolves this against a fixed registry and throws on a value
		// it does not know, blanking the whole page rather than one cell.
		if rating.Valid() {
			var quality gen.SenderId_QualityRating
			_ = quality.FromWaQualityRating(rating)
			sender.QualityRating = &quality
		}
	}
	if s.MessagingTier != nil {
		var tier gen.SenderId_MessagingTier
		_ = tier.FromWaMessagingTier(gen.WaMessagingTier(*s.MessagingTier))
		sender.MessagingTier = &tier
	}

	// Voice verification state only makes sense for a Voice sender; for every
	// other channel the contract wants it absent, not a zero value.
	if s.Channel == string(gen.ChannelIdVOICE) {
		status := gen.VoiceVerificationStatusUnverified
		switch {
		case s.VoiceVerified:
			status = gen.VoiceVerificationStatusVerified
		case s.VoiceCode != nil:
			status = gen.VoiceVerificationStatusCodeSent
		}
		var verification gen.SenderId_VoiceVerification
		// The contract models this as a nullable oneOf, so it is a generated
		// union type rather than a plain struct; the error can only occur if
		// the value fails to marshal, which a fixed enum cannot.
		_ = verification.FromVoiceVerification(gen.VoiceVerification{Status: status})
		sender.VoiceVerification = &verification
	}
	return sender
}

func (s *Server) ListSenderIds(ctx context.Context, _ gen.ListSenderIdsRequestObject) (gen.ListSenderIdsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	senders, err := store.ListSenderIDs(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.SenderId, 0, len(senders))
	for _, sender := range senders {
		out = append(out, senderResponse(sender))
	}
	return gen.ListSenderIds200JSONResponse(out), nil
}

func (s *Server) CreateSenderId(ctx context.Context, request gen.CreateSenderIdRequestObject) (gen.CreateSenderIdResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}

	header := strings.TrimSpace(request.Body.Header)
	if header == "" {
		return gen.CreateSenderId422JSONResponse(
			errorBody(codeValidation, "A sender header is required.")), nil
	}
	// sender_ids.channel has no CHECK constraint behind it, so an unchecked
	// value here is written verbatim — a probe created a sender on channel
	// "TELEPATHY" and it sat in the approvals queue.
	if !oneOf(string(request.Body.Channel), validChannels) {
		return gen.CreateSenderId422JSONResponse(errorBody(codeValidation,
			enumMessage("Channel", validChannels))), nil
	}
	country := string(request.Body.Country)
	regime, known := compliance.For(country)
	if !known {
		return gen.CreateSenderId422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("We do not operate in %q yet.", country))), nil
	}

	// The regime owns the shape of a header, the same way it owns CTA rules.
	// India's was documented in the regime's own remediation text and enforced
	// nowhere, so "a b!@#$%^&*()_+1234567890" was accepted as a DLT header and
	// sat in review looking like a real submission.
	//
	// Alphanumeric channels only. A Voice or Email sender's identity is a
	// number or a domain, and neither is a six-character DLT header.
	if channel := string(request.Body.Channel); channel == "SMS" || channel == "RCS" {
		if result := regime.ValidateHeader(header); !result.OK {
			return gen.CreateSenderId422JSONResponse(
				errorBody(codeValidation, result.Reason)), nil
		}
	}

	created, err := store.CreateSenderID(ctx, s.DB, identity, store.SenderID{
		// Stored exactly as typed. This is the customer's DLT header id, issued
		// to them on their operator portal — Relay is the system of record for
		// it, never its issuer, so there is no derive-or-default branch here.
		ExternalID:  request.Body.RegistrationId,
		Header:      header,
		Channel:     string(request.Body.Channel),
		Country:     country,
		WabaID:      request.Body.WabaId,
		DisplayName: request.Body.DisplayName,
		PhoneNumber: request.Body.PhoneNumber,
		EmailDomain: request.Body.EmailDomain,
		FromAddress: request.Body.FromAddress,
		FromName:    request.Body.FromName,
		// A Voice sender is worth nothing without the number it calls from,
		// and this was being dropped on the floor: the field was missing from
		// the contract, so the generated body had nowhere to put it even though
		// the register form has always sent it and the column has always
		// existed. The sender was created with a NULL caller-ID, the review
		// dialog had nothing to show the operator, and the verification step
		// that is supposed to gate approval had no number to verify.
		CallerIDNumber: request.Body.CallerIdNumber,
	})
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateSenderId409JSONResponse(errorBody(codeConflict,
			"That header is already registered for this channel and country.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateSenderId201JSONResponse(senderResponse(created)), nil
}

func (s *Server) GetSenderId(ctx context.Context, request gen.GetSenderIdRequestObject) (gen.GetSenderIdResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	senderID := request.Id
	// A sender belonging to another tenant is filtered out by RLS, so it
	// surfaces here as not-found — which is also the right answer to give:
	// confirming the id exists elsewhere would itself be a leak.
	sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetSenderId404JSONResponse(errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetSenderId200JSONResponse(senderResponse(sender)), nil
}

// RequestVoiceCall issues the code a verification call would speak. The
// contract returns it directly and says why: there is no real telephony yet,
// so the UI displays it — the same shape as Email showing DNS records for the
// user to add. When a real Voice connector lands, this stops returning the
// code and the contract changes with it.
func (s *Server) RequestVoiceCall(ctx context.Context, request gen.RequestVoiceCallRequestObject) (gen.RequestVoiceCallResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	senderID := request.Id
	code, err := sixDigitCode()
	if err != nil {
		return nil, err
	}
	err = store.SetSenderVoiceCode(ctx, s.DB, identity, senderID, code)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RequestVoiceCall404JSONResponse(errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RequestVoiceCall200JSONResponse(gen.VoiceCallResult{Code: code}), nil
}

func (s *Server) ConfirmVoiceCode(ctx context.Context, request gen.ConfirmVoiceCodeRequestObject) (gen.ConfirmVoiceCodeResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	senderID := request.Id
	sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ConfirmVoiceCode404JSONResponse(errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	if sender.VoiceCode == nil {
		return gen.ConfirmVoiceCode422JSONResponse(errorBody(codeValidation,
			"Request a verification call before entering a code.")), nil
	}
	if strings.TrimSpace(request.Body.Code) != *sender.VoiceCode {
		// One wrong guess burns the code. Six digits left standing after a
		// failed attempt is a brute-force target for anyone with a session, and
		// what it buys them is the right to place calls as a number they do not
		// own. The cost to an honest user is one more verification call.
		if err := store.ClearSenderVoiceCode(ctx, s.DB, identity, senderID); err != nil {
			return nil, err
		}
		return gen.ConfirmVoiceCode422JSONResponse(
			errorBody(codeValidation, "That code is not correct. Request a new call.")), nil
	}
	if err := store.MarkSenderVoiceVerified(ctx, s.DB, identity, senderID); err != nil {
		return nil, err
	}
	return gen.ConfirmVoiceCode204Response{}, nil
}

func sixDigitCode() (string, error) {
	limit := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("api: generate voice code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
