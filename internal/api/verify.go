package api

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/domain/verify"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// toVerifyService maps a stored service, with each channel's readiness.
//
// A service is live only when every channel's sender is approved and every
// channel whose destination requires a registered template has one that its
// copy instantiates. Reporting "live" on sender approval alone meant a service
// whose copy matched nothing read live while every code it tried was refused.
func (s *Server) toVerifyService(ctx context.Context, identity store.Identity,
	service store.VerifyService) gen.VerifyService {

	channels := make([]gen.VerifyChannelState, 0, len(service.Channels))
	live := len(service.Channels) > 0
	for _, channel := range service.Channels {
		senderID, valid := parsePathID(channel.SenderID)
		if !valid {
			live = false
			continue
		}
		state := gen.VerifyChannelState{
			Channel: gen.ChannelId(channel.Channel), SenderId: senderID, Body: channel.Body,
		}
		sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
		if err != nil || sender.Status != "approved" {
			live = false
		}
		if err == nil {
			state.TemplateRequired = sending.RegisteredTemplateRequired(sender.Country)
			template, err := s.matchOTPTemplate(ctx, identity, senderID, channel.Body, service.CodeLength)
			if err == nil && template != nil {
				state.MatchedTemplate = &gen.VerifyTemplateMatch{Id: template.ID, Name: template.Name}
			}
			if state.TemplateRequired && state.MatchedTemplate == nil {
				live = false
			}
		}
		channels = append(channels, state)
	}

	fallback := make([]gen.ChannelId, 0, len(service.FallbackOrder))
	for _, channel := range service.FallbackOrder {
		fallback = append(fallback, gen.ChannelId(channel))
	}

	status := gen.VerifyServiceStatus("setup_needed")
	if live {
		status = gen.VerifyServiceStatus("live")
	}
	return gen.VerifyService{
		Id: service.ID, Name: service.Name, Channels: channels,
		FallbackOrder: fallback, CodeLength: service.CodeLength,
		CodeTtlSeconds: service.CodeTTLSeconds, MaxAttempts: service.MaxAttempts,
		RateLimit: gen.VerifyRateLimit{
			MaxPerPhone: service.MaxPerPhone, WindowSeconds: service.WindowSeconds,
			CooldownSeconds: service.CooldownSeconds,
		},
		RegionAllowlist: service.RegionAllowlist, Status: status,
		CreatedAt: service.CreatedAt,
	}
}

func (s *Server) ListVerifyServices(ctx context.Context, _ gen.ListVerifyServicesRequestObject) (gen.ListVerifyServicesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	services, err := store.ListVerifyServices(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.VerifyService, 0, len(services))
	for _, service := range services {
		out = append(out, s.toVerifyService(ctx, identity, service))
	}
	return gen.ListVerifyServices200JSONResponse{Services: out}, nil
}

func (s *Server) GetVerifyService(ctx context.Context, request gen.GetVerifyServiceRequestObject) (gen.GetVerifyServiceResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	serviceID := request.Id
	service, err := store.GetVerifyService(ctx, s.DB, identity, serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetVerifyService404JSONResponse(errorBody("not_found", "No such service.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetVerifyService200JSONResponse(s.toVerifyService(ctx, identity, service)), nil
}

func (s *Server) CreateVerifyService(ctx context.Context, request gen.CreateVerifyServiceRequestObject) (gen.CreateVerifyServiceResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.CreateVerifyService403JSONResponse(
			errorBody(codeForbidden, "Member role cannot create verify services.")), nil
	}
	body := request.Body

	channels := make([]store.VerifyChannelConfig, 0, len(body.Channels))
	for _, channel := range body.Channels {
		// Copy with no {{code}} means the recipient gets a message with no code
		// in it; two means carriers reject it as a template mismatch. Catching
		// it here beats discovering it on the first production OTP.
		if !verify.BodyHasCodeVariable(channel.Body) {
			return gen.CreateVerifyService422JSONResponse(errorBody(codeValidation,
				"Each channel's message must contain exactly one {{code}} variable.")), nil
		}
		channels = append(channels, store.VerifyChannelConfig{
			Channel: string(channel.Channel), SenderID: channel.SenderId.String(),
			Body: channel.Body,
		})
	}
	if len(channels) == 0 {
		return gen.CreateVerifyService422JSONResponse(errorBody(codeValidation,
			"At least one channel is required.")), nil
	}
	switch body.CodeLength {
	case 4, 6, 8:
	default:
		return gen.CreateVerifyService422JSONResponse(errorBody(codeValidation,
			"Code length must be 4, 6 or 8.")), nil
	}

	fallback := make([]string, 0, len(body.FallbackOrder))
	for _, channel := range body.FallbackOrder {
		fallback = append(fallback, string(channel))
	}

	created, err := store.CreateVerifyService(ctx, s.DB, identity, store.VerifyService{
		Name: body.Name, Channels: channels, FallbackOrder: fallback,
		CodeLength: body.CodeLength, CodeTTLSeconds: body.CodeTtlSeconds,
		MaxAttempts: body.MaxAttempts, MaxPerPhone: body.RateLimit.MaxPerPhone,
		WindowSeconds:   body.RateLimit.WindowSeconds,
		CooldownSeconds: body.RateLimit.CooldownSeconds,
		RegionAllowlist: body.RegionAllowlist,
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateVerifyService201JSONResponse(s.toVerifyService(ctx, identity, created)), nil
}

// CreateVerification starts an OTP challenge.
func (s *Server) CreateVerification(ctx context.Context, request gen.CreateVerificationRequestObject) (gen.CreateVerificationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	serviceID := request.Id
	service, err := store.GetVerifyService(ctx, s.DB, identity, serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.CreateVerification404JSONResponse(errorBody("not_found", "No such service.")), nil
	}
	if err != nil {
		return nil, err
	}

	// Rate limit per phone number, not per tenant: otherwise one attacker
	// hammering a single number exhausts the budget and locks out every other
	// user of the same service.
	window := time.Duration(service.WindowSeconds) * time.Second
	recent, err := store.CountRecentVerifications(ctx, s.DB, identity, request.Body.Msisdn, window)
	if err != nil {
		return nil, err
	}
	if recent >= service.MaxPerPhone {
		// 409, not 429: the contract declares 403/404/409/422 on this operation
		// and nothing else. Inventing a 429 would be a status the frontend's
		// error states were never written against, so a real rate-limit would
		// render as an unhandled failure.
		return gen.CreateVerification409JSONResponse(errorBody("rate_limited",
			"Too many codes requested for this number. Try again later.")), nil
	}

	sendService := s.sendingService(ctx)
	if sendService == nil {
		return gen.CreateVerification422JSONResponse(errorBody(codeValidation,
			"Sending is not available on this deployment.")), nil
	}

	code, err := verify.GenerateCode(service.CodeLength, s.EnableDevEndpoints)
	if err != nil {
		return nil, err
	}
	channels := verifyChannelOrder(service)
	channel := "SMS"
	if len(channels) > 0 {
		channel = channels[0].Channel
	}

	created, err := store.CreateVerification(ctx, s.DB, identity, store.Verification{
		ServiceID: serviceID, Msisdn: request.Body.Msisdn, Channel: channel,
		// Only the hash is stored. A database leak must not hand an attacker
		// live codes for every pending login in the system.
		CodeHash: verify.HashCode(code), MaxAttempts: service.MaxAttempts,
		Currency:  "INR",
		ExpiresAt: time.Now().UTC().Add(time.Duration(service.CodeTTLSeconds) * time.Second),
	})
	if err != nil {
		return nil, err
	}

	// The code goes to the handset and nowhere else: not in the response, which
	// would let any API caller verify any number, and not in the log, where
	// everyone with log access could read every customer's live OTPs.
	//
	// The row exists before the send so a user who types the code the moment it
	// lands never races a verification that is not there yet. Channels are tried
	// in the service's fallback order, moving on only when one refuses outright.
	var refusal string
	delivered := false
	for _, config := range channels {
		senderID, valid := parsePathID(config.SenderID)
		if !valid {
			refusal = "sender_not_found"
			continue
		}
		// The template is chosen from the copy, never from the real code, by
		// the same function the service's readiness uses, so the screen cannot
		// say "matched" while the send is refused. Only then is the real code
		// substituted. With no match the send goes without a template, and the
		// gate refuses it registered_template_required exactly where the
		// destination requires one, and sends it where it does not.
		template, err := s.matchOTPTemplate(ctx, identity, senderID, config.Body, service.CodeLength)
		if err != nil {
			return nil, err
		}
		var templateID *uuid.UUID
		if template != nil {
			templateID = &template.ID
		}
		body := strings.Replace(config.Body, "{{code}}", code, 1)
		result, err := sendService.Send(ctx, identity, sending.SendRequest{
			SenderID: senderID, TemplateID: templateID,
			Msisdn: request.Body.Msisdn, Body: body, Priority: true,
		})
		if err != nil && !messaging.IsRefusal(err) && result.FailureCode == "" {
			return nil, err
		}
		if err == nil && result.Status != "rejected" && result.Status != "failed" {
			channel, delivered = config.Channel, true
			created.CostMinor = result.CostMinor
			break
		}
		refusal = result.FailureCode
		if refusal == "" {
			refusal = "carrier_rejected"
		}
	}
	if !delivered {
		// Dead on arrival: nobody received a code, so nobody may verify with one.
		if err := store.ExpireVerification(ctx, s.DB, identity, created.ID); err != nil {
			return nil, err
		}
		s.Logger.Warn("verification code not sent",
			"verificationId", created.ID, "reason", refusal)
		// The reason is data, so an API caller learns whether to top up or
		// register a template without parsing English.
		body := errorBody("verification_not_sent", "The code could not be sent on any channel. "+
			"Check this service's sender, its approved OTP template and the wallet balance.")
		body.Error.Reason = &refusal
		return gen.CreateVerification422JSONResponse(body), nil
	}
	if err := store.SetVerificationDelivery(ctx, s.DB, identity, created.ID,
		channel, created.CostMinor); err != nil {
		return nil, err
	}

	return gen.CreateVerification201JSONResponse(gen.Verification{
		Id: created.ID, ServiceId: serviceID, Msisdn: request.Body.Msisdn,
		Channel: gen.ChannelId(channel), Status: gen.VerificationStatus("pending"),
		AttemptsRemaining: service.MaxAttempts, ExpiresAt: created.ExpiresAt,
	}), nil
}

// verifyChannelOrder is the service's channels in the order a code should be
// attempted: the fallback order where one is set, then any channel it left out.
func verifyChannelOrder(service store.VerifyService) []store.VerifyChannelConfig {
	ordered := make([]store.VerifyChannelConfig, 0, len(service.Channels))
	used := make(map[int]bool)
	for _, name := range service.FallbackOrder {
		for i, config := range service.Channels {
			if !used[i] && config.Channel == name {
				ordered, used[i] = append(ordered, config), true
				break
			}
		}
	}
	for i, config := range service.Channels {
		if !used[i] {
			ordered = append(ordered, config)
		}
	}
	return ordered
}

// matchOTPTemplate is the approved template for this sender whose registered
// text the OTP copy instantiates, using the send path's own matcher. Used by
// BOTH CreateVerification and toVerifyService, so the service can never report
// a match the send path would not use.
//
// Every approved template on the sender is read, with no page limit. The copy is
// rendered twice, with codes of the service's length that share no digit, and
// both renderings must match: a fixed digit beside the variable ("Your code:
// 1{{code}}") satisfies one code in ten, and would otherwise match now and then.
func (s *Server) matchOTPTemplate(ctx context.Context, identity store.Identity,
	senderID uuid.UUID, copy string, codeLength int) (*store.Template, error) {

	templates, err := store.ApprovedTemplatesForSender(ctx, s.DB, identity, senderID)
	if err != nil {
		return nil, err
	}
	zeros := strings.Replace(copy, "{{code}}", strings.Repeat("0", codeLength), 1)
	nines := strings.Replace(copy, "{{code}}", strings.Repeat("9", codeLength), 1)
	for i := range templates {
		text, ok := registeredText(templates[i])
		if ok && messaging.MatchesTemplate(text, zeros) && messaging.MatchesTemplate(text, nines) {
			return &templates[i], nil
		}
	}
	return nil, nil
}

// registeredText is the text a template registers: its body, or for an RCS text
// template the text inside rcsContent. A card has no single text and matches
// nothing.
func registeredText(t store.Template) (string, bool) {
	if t.Body != nil && *t.Body != "" {
		return *t.Body, true
	}
	return rcsTemplateText(t)
}

// CheckVerification applies one guess.
//
// Every branch runs inside a locked transaction so two parallel guesses cannot
// both read the same attempt count and both be allowed — without the lock the
// attempt limit is advisory and an attacker who fires guesses concurrently
// gets far more than max_attempts of them.
func (s *Server) CheckVerification(ctx context.Context, request gen.CheckVerificationRequestObject) (gen.CheckVerificationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	verificationID := request.Vid

	var outcome error
	result, err := store.CheckVerification(ctx, s.DB, identity, verificationID,
		func(current store.Verification) (string, int, error) {
			// A SETTLED verification never moves again — but "incorrect" is not
			// settled, it is a wrong guess with attempts left. Treating it as
			// terminal would freeze the attempt counter after the first miss,
			// so a user could never retry AND the limit would never be reached.
			switch current.Status {
			case "verified", "locked", "expired":
				return current.Status, current.AttemptsUsed, verify.ErrLocked
			}
			if time.Now().UTC().After(current.ExpiresAt) {
				return "expired", current.AttemptsUsed, verify.ErrExpired
			}
			attempts := current.AttemptsUsed + 1
			if verify.CodeMatches(current.CodeHash, request.Body.Code) {
				return "verified", attempts, nil
			}
			// The budget is spent even though this guess was wrong: locking on
			// the last attempt is what makes the limit real.
			if attempts >= current.MaxAttempts {
				return "locked", attempts, verify.ErrLocked
			}
			return "incorrect", attempts, verify.ErrIncorrect
		})
	if err != nil && !errors.Is(err, verify.ErrIncorrect) && !errors.Is(err, verify.ErrLocked) &&
		!errors.Is(err, verify.ErrExpired) {
		if errors.Is(err, store.ErrNotFound) {
			return gen.CheckVerification404JSONResponse(errorBody("not_found", "No such verification.")), nil
		}
		return nil, err
	}
	outcome = err

	remaining := result.MaxAttempts - result.AttemptsUsed
	if remaining < 0 {
		remaining = 0
	}
	response := gen.Verification{
		Id: result.ID, ServiceId: result.ServiceID, Msisdn: result.Msisdn,
		Channel:           gen.ChannelId(result.Channel),
		Status:            gen.VerificationStatus(result.Status),
		AttemptsRemaining: remaining, ExpiresAt: result.ExpiresAt,
	}
	// A wrong or expired code is reported as 200 with the status on the body,
	// not as an error: the request succeeded, the code did not match. The UI
	// renders from status, and the distinct statuses are what let it say
	// "expired, request another" instead of "wrong code".
	_ = outcome
	return gen.CheckVerification200JSONResponse(response), nil
}

func (s *Server) ListVerificationAttempts(ctx context.Context, request gen.ListVerificationAttemptsRequestObject) (gen.ListVerificationAttemptsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	serviceID := request.Id
	page, ok2 := pageNumber(request.Params.Page)
	if !ok2 {
		return gen.ListVerificationAttempts422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListVerificationAttempts422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	if limit == 0 {
		limit = 50
	}
	verifications, total, err := store.ListVerifications(ctx, s.DB, identity, serviceID, page, limit)
	if err != nil {
		return nil, err
	}

	attempts := make([]gen.VerificationAttempt, 0, len(verifications))
	for _, verification := range verifications {
		attempts = append(attempts, gen.VerificationAttempt{
			Id: verification.ID.String(), ServiceId: verification.ServiceID,
			Msisdn: verification.Msisdn, Country: gen.CountryCode(verification.Country),
			Channel:     gen.ChannelId(verification.Channel),
			Status:      gen.VerificationStatus(verification.Status),
			FraudFlag:   gen.VerificationFraudFlag(verification.FraudFlag),
			FunnelStage: gen.VerifyFunnelStage(funnelStage(verification.Status)),
			CostMinor:   int(verification.CostMinor),
			Currency:    gen.CurrencyCode(verification.Currency),
			CreatedAt:   verification.CreatedAt,
		})
	}
	return gen.ListVerificationAttempts200JSONResponse(gen.VerificationAttemptPage{
		Attempts: attempts, Total: total,
	}), nil
}

// funnelStage maps a verification's status to how far it got. Anything that
// reached a handset counts as delivered; only a correct code counts as
// verified.
func funnelStage(status string) string {
	switch status {
	case "verified":
		return "verified"
	case "pending", "incorrect", "locked":
		return "delivered"
	default:
		return "sent"
	}
}

func (s *Server) GetVerifyAnalytics(ctx context.Context, request gen.GetVerifyAnalyticsRequestObject) (gen.GetVerifyAnalyticsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	serviceID := request.Id
	verifications, _, err := store.ListVerifications(ctx, s.DB, identity, serviceID, 1, 200)
	if err != nil {
		return nil, err
	}

	requested, verified := len(verifications), 0
	var cost int64
	// Counted from each verification's own fraud_flag. These were three
	// hardcoded zeros, which is the worst possible answer for a fraud panel: it
	// is indistinguishable from "we checked and found nothing", so a customer
	// under attack sees an all-clear.
	var fraud gen.VerifyFraudCounts
	byDay := map[time.Time]*gen.VerifyAnalyticsBucket{}
	for _, verification := range verifications {
		cost += verification.CostMinor
		switch verification.FraudFlag {
		case "velocity":
			fraud.Velocity++
		case "geo_anomaly":
			fraud.GeoAnomaly++
		case "blocked":
			fraud.Blocked++
		}
		day := verification.CreatedAt.UTC().Truncate(24 * time.Hour)
		bucket, seen := byDay[day]
		if !seen {
			bucket = &gen.VerifyAnalyticsBucket{BucketStart: day}
			byDay[day] = bucket
		}
		bucket.Requested++
		bucket.Sent++
		if verification.Status == "verified" {
			verified++
			bucket.Verified++
			bucket.Delivered++
		} else if verification.Status != "pending" {
			bucket.Delivered++
		}
	}

	// Zero requests means a zero rate, not a division by zero rendering as
	// "NaN%" on an empty dashboard.
	successRate := 0.0
	if requested > 0 {
		successRate = float64(verified) / float64(requested)
	}
	costPerConversion := int64(0)
	if verified > 0 {
		costPerConversion = cost / int64(verified)
	}

	buckets := make([]gen.VerifyAnalyticsBucket, 0, len(byDay))
	for _, bucket := range byDay {
		buckets = append(buckets, *bucket)
	}
	// Sorted, because Go randomises map iteration order deliberately. Without
	// this the buckets came out shuffled differently on every request and the
	// trend chart drew a different zigzag each time it was refreshed, from the
	// same unchanged data.
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].BucketStart.Before(buckets[j].BucketStart)
	})

	return gen.GetVerifyAnalytics200JSONResponse(gen.VerifyAnalytics{
		Summary: gen.VerifyAnalyticsSummary{
			Requested: requested, Sent: requested, Delivered: requested,
			Verified: verified, SuccessRate: float32(successRate),
			FraudCounts: fraud,
			CostMinor:   int(cost), CostPerConversionMinor: int(costPerConversion),
			Currency: gen.CurrencyCode("INR"),
		},
		Buckets: buckets,
	}), nil
}

// UpdateVerifyService replaces a service's configuration.
//
// The same validation as create, deliberately duplicated rather than shared
// through a helper: the two operations return different response types, and a
// shared validator would have to be generic over both for no gain. The rules
// themselves are one line each and read plainly here.
func (s *Server) UpdateVerifyService(ctx context.Context, request gen.UpdateVerifyServiceRequestObject) (gen.UpdateVerifyServiceResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateVerifyService403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change verify services.")), nil
	}
	body := request.Body
	if body == nil {
		return gen.UpdateVerifyService422JSONResponse(errorBody(codeValidation,
			"Nothing to update.")), nil
	}

	channels := make([]store.VerifyChannelConfig, 0, len(body.Channels))
	for _, channel := range body.Channels {
		// Copy with no {{code}} means the recipient gets a message with no code
		// in it; two means carriers reject it as a template mismatch.
		if !verify.BodyHasCodeVariable(channel.Body) {
			return gen.UpdateVerifyService422JSONResponse(errorBody(codeValidation,
				"Each channel's message must contain exactly one {{code}} variable.")), nil
		}
		channels = append(channels, store.VerifyChannelConfig{
			Channel: string(channel.Channel), SenderID: channel.SenderId.String(),
			Body: channel.Body,
		})
	}
	if len(channels) == 0 {
		return gen.UpdateVerifyService422JSONResponse(errorBody(codeValidation,
			"At least one channel is required.")), nil
	}
	switch body.CodeLength {
	case 4, 6, 8:
	default:
		return gen.UpdateVerifyService422JSONResponse(errorBody(codeValidation,
			"Code length must be 4, 6 or 8.")), nil
	}

	fallback := make([]string, 0, len(body.FallbackOrder))
	for _, channel := range body.FallbackOrder {
		fallback = append(fallback, string(channel))
	}

	updated, err := store.UpdateVerifyService(ctx, s.DB, identity, request.Id,
		store.VerifyService{
			Name: body.Name, Channels: channels, FallbackOrder: fallback,
			CodeLength: body.CodeLength, CodeTTLSeconds: body.CodeTtlSeconds,
			MaxAttempts: body.MaxAttempts, MaxPerPhone: body.RateLimit.MaxPerPhone,
			WindowSeconds:   body.RateLimit.WindowSeconds,
			CooldownSeconds: body.RateLimit.CooldownSeconds,
			RegionAllowlist: body.RegionAllowlist,
		})
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateVerifyService404JSONResponse(
			errorBody(codeNotFound, "No such service.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateVerifyService200JSONResponse(
		s.toVerifyService(ctx, identity, updated)), nil
}
