package api

import (
	"context"
	"errors"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/verify"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// toVerifyService maps a stored service. Status is derived from whether every
// configured channel has an approved sender: a service whose sender is still
// in review cannot actually send, and reporting it as "live" would mean the
// first real OTP silently fails.
func (s *Server) toVerifyService(ctx context.Context, identity store.Identity,
	service store.VerifyService) gen.VerifyService {

	channels := make([]gen.VerifyChannelConfig, 0, len(service.Channels))
	live := len(service.Channels) > 0
	for _, channel := range service.Channels {
		senderID, valid := parsePathID(channel.SenderID)
		if !valid {
			live = false
			continue
		}
		channels = append(channels, gen.VerifyChannelConfig{
			Channel: gen.ChannelId(channel.Channel), SenderId: senderID, Body: channel.Body,
		})
		sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
		if err != nil || sender.Status != "approved" {
			live = false
		}
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

	code, err := verify.GenerateCode(service.CodeLength)
	if err != nil {
		return nil, err
	}
	channel := "SMS"
	if len(service.Channels) > 0 {
		channel = service.Channels[0].Channel
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

	// The code is logged, never returned. Returning it would let anyone who can
	// call the API verify any number without seeing the handset — which is the
	// entire security property an OTP provides. A local operator can read it
	// from the log to exercise the flow.
	s.Logger.Info("verification code issued (sandbox)",
		"verificationId", created.ID, "msisdn", request.Body.Msisdn, "code", code)

	return gen.CreateVerification201JSONResponse(gen.Verification{
		Id: created.ID, ServiceId: serviceID, Msisdn: request.Body.Msisdn,
		Channel: gen.ChannelId(channel), Status: gen.VerificationStatus("pending"),
		AttemptsRemaining: service.MaxAttempts, ExpiresAt: created.ExpiresAt,
	}), nil
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
	limit := 50
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	verifications, total, err := store.ListVerifications(ctx, s.DB, identity, serviceID, limit)
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
	verifications, _, err := store.ListVerifications(ctx, s.DB, identity, serviceID, 200)
	if err != nil {
		return nil, err
	}

	requested, verified := len(verifications), 0
	var cost int64
	byDay := map[time.Time]*gen.VerifyAnalyticsBucket{}
	for _, verification := range verifications {
		cost += verification.CostMinor
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

	return gen.GetVerifyAnalytics200JSONResponse(gen.VerifyAnalytics{
		Summary: gen.VerifyAnalyticsSummary{
			Requested: requested, Sent: requested, Delivered: requested,
			Verified: verified, SuccessRate: float32(successRate),
			FraudCounts: gen.VerifyFraudCounts{Velocity: 0, GeoAnomaly: 0, Blocked: 0},
			CostMinor:   int(cost), CostPerConversionMinor: int(costPerConversion),
			Currency: gen.CurrencyCode("INR"),
		},
		Buckets: buckets,
	}), nil
}
