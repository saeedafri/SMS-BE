package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/whatsapp"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// WhatsApp on Meta's Cloud API, Relay as a Tech Provider (decision L-3): a
// tenant connects its own number through Embedded Signup, Relay finishes the
// setup server-side, and the number's standing is derived from its facts on
// every read. Every Meta call goes through connector.WhatsAppProvider.

var (
	defaultWhatsAppOnce sync.Once
	defaultWhatsApp     *connector.FakeWhatsApp
)

func (s *Server) whatsAppProvider() connector.WhatsAppProvider {
	if s.WhatsApp != nil {
		return s.WhatsApp
	}
	defaultWhatsAppOnce.Do(func() { defaultWhatsApp = connector.NewFakeWhatsApp() })
	return defaultWhatsApp
}

func setupStep(key string, status gen.SetupStepStatus, detail string) gen.SetupStep {
	step := gen.SetupStep{Step: key, Status: status}
	if detail != "" {
		step.Detail = &detail
	}
	return step
}

// waSetup is the number's setup as the contract states it.
func waSetup(f *store.WhatsAppFacts) []gen.SetupStep {
	steps := []gen.SetupStep{}
	for _, step := range f.Steps() {
		steps = append(steps, setupStep(step.Key, gen.SetupStepStatus(step.Status), step.Detail))
	}
	return steps
}

// waLimitContacts is how many distinct people a tier allows per 24 hours;
// nil for unlimited or unknown.
func waLimitContacts(limit *string) *int {
	if limit == nil {
		return nil
	}
	contacts := map[string]int{"TIER_250": 250, "TIER_2K": 2000, "TIER_10K": 10000,
		"TIER_100K": 100000}[*limit]
	if contacts == 0 {
		return nil
	}
	return &contacts
}

// withWhatsApp adds a WhatsApp sender's standing to its response. Nothing
// here is a token, a PIN or a secret.
func withWhatsApp(out *gen.SenderId, sender store.SenderID) {
	f := sender.WhatsApp
	if f == nil {
		return
	}
	setup := waSetup(f)
	out.WaSetup = &setup
	billing := gen.SenderIdBilling("meta_direct")
	out.Billing = &billing
	if f.TokenStatus != nil {
		status := gen.SenderIdTokenStatus(*f.TokenStatus)
		out.TokenStatus = &status
	}
	if f.DisplayNameStatus != nil {
		status := gen.SenderIdDisplayNameStatus(*f.DisplayNameStatus)
		out.DisplayNameStatus = &status
	}
	if f.PaymentMethod != nil {
		method := gen.SenderIdPaymentMethod(*f.PaymentMethod)
		out.PaymentMethod = &method
	}
	out.BillingCurrency = f.BillingCurrency
	if f.Limit != nil {
		limit := gen.WaMessagingLimit(*f.Limit)
		out.MessagingLimit = &limit
	}
	out.MaxUniqueContacts24h = waLimitContacts(f.Limit)
	left := f.AttemptsLeft()
	out.RegistrationAttemptsLeft = &left
	if f.Restricted != nil {
		restricted := *f.Restricted && (f.RestrictedUntil == nil || f.RestrictedUntil.After(time.Now()))
		out.Restricted = &restricted
	}
}

// withContactsUsed adds how many distinct people a WhatsApp number reached in
// the last 24 hours, beside its limit. Left absent when ClickHouse cannot say.
func (s *Server) withContactsUsed(ctx context.Context, identity store.Identity, out gen.SenderId) gen.SenderId {
	if out.WaSetup == nil {
		return out
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return out
	}
	used, err := store.WhatsAppContactsLast24h(ctx, conn, identity.TenantID, out.Header,
		s.now().Add(-24*time.Hour))
	if err == nil {
		out.UniqueContactsUsed24h = &used
	}
	return out
}

// ---- Embedded Signup --------------------------------------------------------------

func (s *Server) GetWaEmbeddedSignupConfig(ctx context.Context, _ gen.GetWaEmbeddedSignupConfigRequestObject) (
	gen.GetWaEmbeddedSignupConfigResponseObject, error) {

	if _, ok := identityFrom(ctx); !ok {
		return gen.GetWaEmbeddedSignupConfig401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	// Public identifiers only: the browser's Facebook SDK needs them. Never
	// the app secret or a token, and never a 404.
	if s.MetaAppID == "" || s.MetaConfigID == "" {
		reason := "WhatsApp is not set up on this platform yet."
		return gen.GetWaEmbeddedSignupConfig200JSONResponse{Enabled: false, Reason: &reason}, nil
	}
	appID, configID, version := s.MetaAppID, s.MetaConfigID, s.MetaGraphVersion
	return gen.GetWaEmbeddedSignupConfig200JSONResponse{Enabled: true, AppId: &appID,
		ConfigurationId: &configID, GraphApiVersion: &version}, nil
}

func (s *Server) onboardingCap() int {
	if s.WhatsAppOnboardingCap > 0 {
		return s.WhatsAppOnboardingCap
	}
	return 10
}

func (s *Server) GetOperatorWaOnboarding(ctx context.Context, _ gen.GetOperatorWaOnboardingRequestObject) (
	gen.GetOperatorWaOnboardingResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorWaOnboarding401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	count, err := store.WhatsAppOnboardingsSince(ctx, s.operatorPool(), s.now().AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	return gen.GetOperatorWaOnboarding200JSONResponse{OnboardedLast7Days: count,
		Cap: s.onboardingCap(), CapRaised: s.onboardingCap() > 10}, nil
}

func sixDigitPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// CompleteWaEmbeddedSignup finishes a number's setup on the server, in Meta's
// order. The weekly cap is judged first, before any call to Meta, so a
// refused request burns nothing and its 30-second code is still good.
func (s *Server) CompleteWaEmbeddedSignup(ctx context.Context, request gen.CompleteWaEmbeddedSignupRequestObject) (
	gen.CompleteWaEmbeddedSignupResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.CompleteWaEmbeddedSignup401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	body := request.Body
	if body == nil || strings.TrimSpace(body.Code) == "" || strings.TrimSpace(body.WabaId) == "" ||
		!body.Event.Valid() {
		return gen.CompleteWaEmbeddedSignup422JSONResponse(errorBody(codeValidation,
			"code, wabaId and event are required.")), nil
	}
	if body.Event == "FINISH" && (body.PhoneNumberId == nil || *body.PhoneNumberId == "") {
		return gen.CompleteWaEmbeddedSignup422JSONResponse(errorBody(codeValidation,
			"phoneNumberId is required when the signup finished with a number.")), nil
	}
	onboarded, err := store.WhatsAppOnboardingsSince(ctx, s.operatorPool(), s.now().AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	if onboarded >= s.onboardingCap() {
		return gen.CompleteWaEmbeddedSignup409JSONResponse(errorBody("onboarding_cap_reached",
			"Relay has connected as many WhatsApp numbers this week as Meta allows. Try again in a few days.")), nil
	}
	if s.Secrets == nil {
		return nil, errors.New("api: no CONNECTION_ENCRYPTION_KEY, so a WhatsApp token cannot be stored")
	}

	meta := s.whatsAppProvider()
	token, err := meta.ExchangeCode(ctx, body.Code)
	if errors.Is(err, connector.ErrInvalidCode) {
		return gen.CompleteWaEmbeddedSignup422JSONResponse(errorBody("invalid_code",
			"That signup expired or was already used. Start the connection again.")), nil
	}
	if err != nil {
		return nil, err
	}
	subscribed := meta.SubscribeApp(ctx, token, body.WabaId) == nil
	sealedToken, err := s.Secrets.Encrypt(token)
	if err != nil {
		return nil, err
	}
	created := store.NewWhatsAppSender{Header: body.WabaId, WabaID: body.WabaId,
		PhoneNumberID: body.PhoneNumberId, BusinessID: body.BusinessId, TokenSealed: sealedToken,
		Subscribed: subscribed, PaymentMethod: "none", DisplayNameStatus: "none"}
	if body.PhoneNumberId != nil && *body.PhoneNumberId != "" {
		created.Header = *body.PhoneNumberId
		pin, err := sixDigitPIN()
		if err != nil {
			return nil, err
		}
		if created.PinSealed, err = s.Secrets.Encrypt(pin); err != nil {
			return nil, err
		}
		switch err := meta.RegisterNumber(ctx, token, *body.PhoneNumberId, pin); {
		case errors.Is(err, connector.ErrRegistrationLimit):
			created.RegistrationAttempts = store.WhatsAppRegistrationAttempts
		case err != nil:
			created.RegistrationAttempts = 1
		default:
			created.Registered, created.RegistrationAttempts = true, 1
		}
		if account, err := meta.Account(ctx, token, body.WabaId, *body.PhoneNumberId); err == nil {
			created.PaymentMethod, created.DisplayNameStatus = account.PaymentMethod, account.DisplayNameStatus
			if account.MessagingLimit != "" {
				created.Limit = &account.MessagingLimit
			}
		}
	}
	senderID, err := store.CreateWhatsAppSender(ctx, s.DB, identity, created)
	if errors.Is(err, store.ErrConflict) {
		return gen.CompleteWaEmbeddedSignup422JSONResponse(errorBody(codeConflict,
			"That WhatsApp number is already connected to an account.")), nil
	}
	if err != nil {
		return nil, err
	}
	sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
	if err != nil {
		return nil, err
	}
	return gen.CompleteWaEmbeddedSignup200JSONResponse(senderResponse(sender)), nil
}

// VerifyWaSetup re-reads payment, currency, display name and limit from Meta
// and promotes the sender once every step is done. It never advances payment
// by itself. 404 for an unknown sender, 409 for one that is not WhatsApp.
func (s *Server) VerifyWaSetup(ctx context.Context, request gen.VerifyWaSetupRequestObject) (
	gen.VerifyWaSetupResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.VerifyWaSetup401JSONResponse(errorBody(codeUnauthenticated,
			"Missing or invalid bearer token")), nil
	}
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.VerifyWaSetup404JSONResponse(errorBody(codeNotFound, "No such sender.")), nil
	}
	if err != nil {
		return nil, err
	}
	if sender.Channel != "WHATSAPP" || sender.WhatsApp == nil {
		return gen.VerifyWaSetup409JSONResponse(errorBody(codeConflict,
			"Only a WhatsApp sender has a setup to check.")), nil
	}
	f := sender.WhatsApp
	if f.HasToken && f.PhoneNumberID != nil && s.Secrets != nil {
		sealed, err := store.WhatsAppToken(ctx, s.DB, identity, sender.ID)
		if err != nil {
			return nil, err
		}
		token, err := s.Secrets.Decrypt(sealed)
		if err != nil {
			return nil, err
		}
		tokenStatus := "ok"
		account, err := s.whatsAppProvider().Account(ctx, token, *sender.WabaID, *f.PhoneNumberID)
		switch {
		case errors.Is(err, connector.ErrTokenInvalid):
			tokenStatus = "invalid"
		case err != nil:
			return nil, err
		default:
			f.PaymentMethod, f.DisplayNameStatus = &account.PaymentMethod, &account.DisplayNameStatus
			if account.BillingCurrency != "" {
				f.BillingCurrency = &account.BillingCurrency
			}
			if account.MessagingLimit != "" {
				f.Limit = &account.MessagingLimit
			}
		}
		f.TokenStatus = &tokenStatus
		if err := store.SaveWhatsAppAccount(ctx, s.DB, identity, sender.ID, f.PaymentMethod,
			f.BillingCurrency, f.DisplayNameStatus, f.Limit, tokenStatus, f.Ready()); err != nil {
			return nil, err
		}
		s.Hot.Forget(store.SenderKey(identity.TenantID, sender.ID))
	}
	refreshed, err := store.GetSenderID(ctx, s.DB, identity, sender.ID)
	if err != nil {
		return nil, err
	}
	return gen.VerifyWaSetup200JSONResponse(senderResponse(refreshed)), nil
}

// ---- Meta's webhooks ----------------------------------------------------------

func (s *Server) mountWhatsAppWebhookRoutes(r chi.Router) {
	r.Get("/v1/webhooks/whatsapp", s.verifyWhatsAppWebhook)
	r.Post("/v1/webhooks/whatsapp", s.receiveWhatsAppWebhook)
}

// verifyWhatsAppWebhook is Meta's subscription handshake.
func (s *Server) verifyWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if s.MetaVerifyToken == "" || query.Get("hub.mode") != "subscribe" ||
		!hmac.Equal([]byte(query.Get("hub.verify_token")), []byte(s.MetaVerifyToken)) {
		writeError(w, http.StatusForbidden, codeForbidden, "Verification failed.")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, query.Get("hub.challenge"))
}

// receiveWhatsAppWebhook takes Meta's batched updates, signed with the app
// secret. It answers fast and applies each change once.
func (s *Server) receiveWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidation, "The body could not be read.")
		return
	}
	mac := hmac.New(sha256.New, []byte(s.MetaAppSecret))
	mac.Write(raw)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if s.MetaAppSecret == "" || !hmac.Equal([]byte(r.Header.Get("X-Hub-Signature-256")), []byte(want)) {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Bad signature.")
		return
	}
	var payload whatsAppWebhook
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeError(w, http.StatusBadRequest, codeValidation, "The body is not a Meta webhook.")
		return
	}
	if err := s.applyWhatsAppWebhook(r.Context(), payload); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type whatsAppWebhook struct {
	Entry []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Statuses []struct {
					ID        string `json:"id"`
					Status    string `json:"status"`
					Timestamp string `json:"timestamp"`
					BizOpaque string `json:"biz_opaque_callback_data"`
					Errors    []struct {
						Code int `json:"code"`
					} `json:"errors"`
					Pricing *struct {
						Billable bool   `json:"billable"`
						Model    string `json:"pricing_model"`
						Type     string `json:"type"`
						Category string `json:"category"`
					} `json:"pricing"`
				} `json:"statuses"`
				Event             string      `json:"event"`
				MessageTemplateID json.Number `json:"message_template_id"`
				Reason            string      `json:"reason"`
				CurrentLimit      string      `json:"current_limit"`
				PreviousCategory  string      `json:"previous_category"`
				NewCategory       string      `json:"new_category"`
				NewQualityScore   string      `json:"new_quality_score"`
				RestrictionInfo   []struct {
					Expiration json.Number `json:"expiration"`
				} `json:"restriction_info"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (s *Server) applyWhatsAppWebhook(ctx context.Context, payload whatsAppWebhook) error {
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			value := change.Value
			switch change.Field {
			case "messages":
				for _, status := range value.Statuses {
					if err := s.applyWhatsAppStatus(ctx, status.ID, status.Status, status.BizOpaque,
						status.Errors, status.Pricing); err != nil {
						return err
					}
				}
			case "account_update":
				if err := s.applyWhatsAppAccountUpdate(ctx, entry.ID, value.Event,
					value.RestrictionInfo); err != nil {
					return err
				}
			case "message_template_status_update":
				if err := s.applyWhatsAppTemplateStatus(ctx, value.MessageTemplateID.String(),
					value.Event, value.Reason); err != nil {
					return err
				}
			case "template_category_update":
				if err := s.applyWhatsAppTemplateChange(ctx, value.MessageTemplateID.String(),
					map[string]any{"category": strings.ToLower(value.NewCategory),
						"categoryChangedByMeta": true}); err != nil {
					return err
				}
			case "message_template_quality_update":
				if err := s.applyWhatsAppTemplateChange(ctx, value.MessageTemplateID.String(),
					map[string]any{"qualityRating": strings.ToLower(value.NewQualityScore)}); err != nil {
					return err
				}
			case "phone_number_quality_update":
				if err := s.applyWhatsAppNumberQuality(ctx, entry.ID, value.Event,
					value.CurrentLimit); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// applyWhatsAppStatus settles a message from Meta's status. Each wamid and
// status applies once; read without delivered counts as both.
func (s *Server) applyWhatsAppStatus(ctx context.Context, wamid, status, relayID string,
	errs []struct {
		Code int `json:"code"`
	}, pricing *struct {
		Billable bool   `json:"billable"`
		Model    string `json:"pricing_model"`
		Type     string `json:"type"`
		Category string `json:"category"`
	}) error {

	messageID, err := uuid.Parse(relayID)
	if err != nil || (status != "delivered" && status != "read" && status != "failed") {
		return nil
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return err
	}
	tenantID, err := store.FindMessageTenant(ctx, conn, messageID)
	if err != nil {
		return nil
	}
	identity := store.Identity{TenantID: tenantID}
	if claimed, err := store.ClaimProviderEvent(ctx, s.DB, identity, "wa:"+wamid+":"+status); err != nil || !claimed {
		return err
	}
	service := s.sendingService(ctx)
	if service == nil {
		return errClickHouseUnavailable
	}
	report := connector.DeliveryReport{MessageID: messageID.String(), CarrierRef: wamid,
		OccurredAt: s.now().UTC(), Delivered: status != "failed", Read: status == "read"}
	if pricing != nil {
		billing, _ := json.Marshal(map[string]any{"billable": pricing.Billable, "model": pricing.Model,
			"type": pricing.Type, "category": pricing.Category})
		report.ProviderBilling = string(billing)
	}
	if status == "failed" && len(errs) > 0 {
		report.ErrorCode = fmt.Sprint(errs[0].Code)
	}
	return service.ApplyDeliveryReport(ctx, identity, report)
}

// applyWhatsAppAccountUpdate records what Meta says about an account: removed,
// disconnected or disabled means the number cannot send; a restriction marks
// it restricted until its expiry.
func (s *Server) applyWhatsAppAccountUpdate(ctx context.Context, wabaID, event string,
	restrictions []struct {
		Expiration json.Number `json:"expiration"`
	}) error {

	var refs []store.SenderRef
	var err error
	webhook := ""
	switch event {
	case "PARTNER_REMOVED", "ACCOUNT_DISCONNECTED", "DISABLED_UPDATE":
		disconnected := true
		refs, err = store.SetWhatsAppStanding(ctx, s.operatorPool(), wabaID, &disconnected, nil, false)
		webhook = "sender.disconnected"
	case "ACCOUNT_RESTRICTION":
		until := s.now().Add(24 * time.Hour)
		if len(restrictions) > 0 {
			if seconds, parseErr := restrictions[0].Expiration.Int64(); parseErr == nil && seconds > 0 {
				until = time.Unix(seconds, 0).UTC()
			}
		}
		refs, err = store.SetWhatsAppStanding(ctx, s.operatorPool(), wabaID, nil, &until, false)
		webhook = "sender.restricted"
	default:
		return nil
	}
	if err != nil {
		return err
	}
	for _, ref := range refs {
		s.Hot.Forget(store.SenderKey(ref.TenantID, ref.SenderID))
		data := map[string]any{"senderId": ref.SenderID, "channel": "WHATSAPP"}
		if webhook == "sender.disconnected" {
			data["reason"] = strings.ToLower(event)
		} else {
			data["restricted"] = true
		}
		s.emitWebhookEventAsync(store.Identity{TenantID: ref.TenantID}, webhook, data)
	}
	return nil
}

// applyWhatsAppNumberQuality records a number's quality (flagged is red,
// unflagged green) and its daily limit, and tells each tenant what moved.
func (s *Server) applyWhatsAppNumberQuality(ctx context.Context, wabaID, event, currentLimit string) error {
	var quality, limit *string
	if value, known := map[string]string{"FLAGGED": "red", "UNFLAGGED": "green"}[event]; known {
		quality = &value
	}
	if waLimitContacts(&currentLimit) != nil || currentLimit == "TIER_UNLIMITED" {
		limit = &currentLimit
	}
	if quality == nil && limit == nil {
		return nil
	}
	changes, err := store.SetWhatsAppQuality(ctx, s.operatorPool(), wabaID, quality, limit)
	if err != nil {
		return err
	}
	for _, change := range changes {
		s.Hot.Forget(store.SenderKey(change.TenantID, change.SenderID))
		tenant := store.Identity{TenantID: change.TenantID}
		if quality != nil && (change.Quality == nil || *change.Quality != *quality) {
			s.emitWebhookEventAsync(tenant, "sender.quality_changed", map[string]any{
				"senderId": change.SenderID, "channel": "WHATSAPP",
				"previous": change.Quality, "current": *quality})
		}
		if limit != nil && (change.Limit == nil || *change.Limit != *limit) {
			s.emitWebhookEventAsync(tenant, "sender.limit_changed", map[string]any{
				"senderId": change.SenderID, "channel": "WHATSAPP",
				"previous": change.Limit, "current": *limit,
				"maxUniqueContacts24h": waLimitContacts(limit)})
		}
	}
	return nil
}

// applyWhatsAppTemplateStatus records Meta's verdict on a template. Flagged
// keeps the status and marks the template at risk; locked marks it locked.
func (s *Server) applyWhatsAppTemplateStatus(ctx context.Context, metaTemplateID, event, reason string) error {
	status := map[string]string{"APPROVED": "approved", "REJECTED": "rejected", "DISABLED": "disabled",
		"PAUSED": "paused", "PENDING": "pending", "IN_APPEAL": "in_appeal", "LIMIT_EXCEEDED": "limit_exceeded",
		"ARCHIVED": "archived", "PENDING_DELETION": "pending_deletion", "DELETED": "deleted",
		"REINSTATED": "approved"}[event]
	change := map[string]any{"decidedAt": s.now().UTC()}
	switch {
	case status != "":
		change["status"] = status
	case event == "FLAGGED":
		change["atRisk"] = true
	case event == "LOCKED":
		change["locked"] = true
	default:
		return nil
	}
	if reason != "" && reason != "NONE" {
		change["rejectionReason"] = strings.ToLower(reason)
	}
	return s.applyWhatsAppTemplateChange(ctx, metaTemplateID, change)
}

// applyWhatsAppTemplateChange merges what Meta said into a template's stored
// state, which keeps everything else, and tells the tenant when the template
// was paused, disabled or moved to another category.
func (s *Server) applyWhatsAppTemplateChange(ctx context.Context, metaTemplateID string,
	change map[string]any) error {

	if metaTemplateID == "" {
		return nil
	}
	template, tenantID, err := store.WhatsAppTemplateByMetaID(ctx, s.operatorPool(), metaTemplateID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current := map[string]any{}
	_ = json.Unmarshal(template.WAState, &current)
	before := map[string]any{"status": current["status"], "category": current["category"]}
	for key, value := range change {
		current[key] = value
	}
	state, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if _, _, err := store.SetTemplateWAState(ctx, s.operatorPool(), metaTemplateID, state); err != nil {
		return err
	}
	definition, _ := whatsapp.Parse(template.WADefinition)
	data := map[string]any{"templateId": template.ID, "name": definition.Name, "language": definition.Language}
	tenant := store.Identity{TenantID: tenantID}
	switch {
	case current["status"] != before["status"] && (current["status"] == "paused" || current["status"] == "disabled"):
		data["status"] = current["status"]
		if current["status"] == "paused" {
			data["pausedUntil"] = current["pausedUntil"]
		}
		s.emitWebhookEventAsync(tenant, "template."+current["status"].(string), data)
	case change["categoryChangedByMeta"] == true && current["category"] != before["category"]:
		data["previous"], data["current"], data["changedByMeta"] = before["category"], current["category"], true
		s.emitWebhookEventAsync(tenant, "template.category_changed", data)
	}
	return nil
}
