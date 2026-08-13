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
	country := string(request.Body.Country)
	if _, known := compliance.For(country); !known {
		return gen.CreateSenderId422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("We do not operate in %q yet.", country))), nil
	}

	created, err := store.CreateSenderID(ctx, s.DB, identity, store.SenderID{
		Header:      header,
		Channel:     string(request.Body.Channel),
		Country:     country,
		WabaID:      request.Body.WabaId,
		DisplayName: request.Body.DisplayName,
		PhoneNumber: request.Body.PhoneNumber,
		EmailDomain: request.Body.EmailDomain,
		FromAddress: request.Body.FromAddress,
		FromName:    request.Body.FromName,
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
		return gen.ConfirmVoiceCode422JSONResponse(
			errorBody(codeValidation, "That code is not correct.")), nil
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
