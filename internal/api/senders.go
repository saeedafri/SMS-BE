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
	// Shared with the update path so an edit cannot reach a state a create
	// would have refused.
	if problem := senderIdentityProblem(regime, string(request.Body.Channel), header,
		request.Body.RegistrationId); problem != "" {
		return gen.CreateSenderId422JSONResponse(errorBody(codeValidation, problem)), nil
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

// senderIdentityProblem validates the values a sender record would END UP with,
// and is called by both the create and the update path.
//
// One function on purpose. Validating a patch in isolation lets a clear slip
// through that the same empty value would be refused for at create — the record
// ends up in a state the create path would never have produced, reached by
// editing rather than by creating. The two rules cannot drift while there is
// only one of them.
//
// Returns the sentence to show the customer, or "" when the record is valid.
func senderIdentityProblem(regime compliance.Regime, channel, header string,
	registrationID *string) string {

	if strings.TrimSpace(header) == "" {
		return "A sender header is required."
	}
	// Alphanumeric channels only. A Voice or Email sender's identity is a
	// number or a domain, and neither is a six-character DLT header.
	if channel == "SMS" || channel == "RCS" {
		if result := regime.ValidateHeader(header); !result.OK {
			return result.Reason
		}
	}
	// A registry id that is present but empty is not an id. Where the regime
	// issues one, storing "" would record an answer to a question the customer
	// has not answered, and the header would go to the carrier unbacked.
	//
	// Absent is a different thing from empty and is left alone here: a customer
	// may register a header before their DLT id comes back.
	if registrationID != nil && strings.TrimSpace(*registrationID) == "" &&
		regime.RequiresRegistrationID(compliance.TierSender) {
		return "This country's regulator issues a registration id for a sender, " +
			"so it cannot be cleared."
	}
	return ""
}

// UpdateSenderId corrects a sender that no registry has bound yet.
//
// A typo in a header used to be permanent for the life of the account. What
// gates this is the sender's STANDING: once approved, the header is bound to the
// registry entry that granted it — a DLT header registration in India, a TCR
// campaign in the US — and changing it would leave the platform sending under a
// header no registry approved.
func (s *Server) UpdateSenderId(ctx context.Context, request gen.UpdateSenderIdRequestObject) (
	gen.UpdateSenderIdResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateSenderId404JSONResponse(
			errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	switch sender.Status {
	case "approved", "blocked", "expired":
		return gen.UpdateSenderId409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("A %s sender cannot be edited — its header is bound to the "+
				"registration that granted it. Retire it and register the correction.",
				sender.Status))), nil
	}
	if request.Body == nil {
		return gen.UpdateSenderId200JSONResponse(senderResponse(sender)), nil
	}

	// An empty object is a no-op rather than an error: a form that submits
	// without changing anything has not asked for anything invalid.
	header := sender.Header
	if request.Body.Header != nil {
		header = strings.TrimSpace(*request.Body.Header)
	}
	// displayName is a WhatsApp Business concept. Storing it on an SMS sender
	// records a field that channel has no meaning for, so it is refused rather
	// than quietly kept.
	if request.Body.DisplayName != nil && sender.Channel != "WHATSAPP" {
		return gen.UpdateSenderId422JSONResponse(errorBody(codeValidation,
			"displayName applies to WhatsApp senders only.")), nil
	}

	// Omitting registrationId leaves the stored value alone; sending null asks
	// to clear it. Those are different requests, and the generated struct
	// cannot tell them apart on its own — both arrive as a nil pointer — so the
	// raw body decides which one this is.
	clearRegistration := false
	if request.Body.RegistrationId == nil && bodyMentions(ctx, "registrationId") {
		clearRegistration = true
	}
	registrationID := sender.ExternalID
	switch {
	case clearRegistration:
		registrationID = nil
	case request.Body.RegistrationId != nil:
		registrationID = request.Body.RegistrationId
	}

	// Validated as the record would END UP, through the create path's own rule.
	regime, known := compliance.For(sender.Country)
	if !known {
		return gen.UpdateSenderId422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("We do not operate in %q yet.", sender.Country))), nil
	}
	checked := registrationID
	if clearRegistration {
		blank := ""
		checked = &blank
	}
	if problem := senderIdentityProblem(regime, sender.Channel, header, checked); problem != "" {
		return gen.UpdateSenderId422JSONResponse(errorBody(codeValidation, problem)), nil
	}

	updated, err := store.UpdateSenderID(ctx, s.DB, identity, request.Id,
		request.Body.Header, request.Body.DisplayName, request.Body.RegistrationId,
		clearRegistration)
	if errors.Is(err, store.ErrConflict) {
		return gen.UpdateSenderId409JSONResponse(errorBody(codeConflict,
			"That header is already registered for this channel and country.")), nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateSenderId404JSONResponse(
			errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateSenderId200JSONResponse(senderResponse(updated)), nil
}

// DeleteSenderId retires a sender nothing depends on.
//
// What gates delete is USE, not standing: retiring a verified sender is
// legitimate once nothing references it, which is the opposite of the rule on
// editing. The refusal names the counts per kind, because "in use" without
// saying by what leaves the caller with nothing to act on.
func (s *Server) DeleteSenderId(ctx context.Context, request gen.DeleteSenderIdRequestObject) (
	gen.DeleteSenderIdResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}
	if _, err := store.GetSenderID(ctx, s.DB, identity, request.Id); errors.Is(err, store.ErrNotFound) {
		return gen.DeleteSenderId404JSONResponse(
			errorBody(codeNotFound, "No such sender.")), nil
	} else if err != nil {
		return nil, err
	}

	refs, err := store.CountSenderReferences(ctx, s.DB, identity, request.Id)
	if err != nil {
		return nil, err
	}
	if refs.Total() > 0 {
		return gen.DeleteSenderId409JSONResponse(errorBody(codeConflict,
			"This sender is still used by "+describeSenderUse(refs)+
				". Repoint or remove those first.")), nil
	}

	if err := store.DeleteSenderID(ctx, s.DB, identity, request.Id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.DeleteSenderId404JSONResponse(
				errorBody(codeNotFound, "No such sender.")), nil
		}
		return nil, err
	}
	return gen.DeleteSenderId204Response{}, nil
}

// describeSenderUse turns the counts into the sentence the caller acts on:
// "2 templates, 1 campaign".
func describeSenderUse(refs store.SenderReferences) string {
	parts := []string{}
	for _, kind := range []struct {
		count     int
		one, many string
	}{
		{refs.Templates, "template", "templates"},
		{refs.Campaigns, "campaign", "campaigns"},
		{refs.CampaignFallback, "campaign fallback", "campaign fallbacks"},
		{refs.Journeys, "journey", "journeys"},
		{refs.VerifyServices, "Verify service", "Verify services"},
	} {
		if kind.count == 0 {
			continue
		}
		noun := kind.many
		if kind.count == 1 {
			noun = kind.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", kind.count, noun))
	}
	return strings.Join(parts, ", ")
}
