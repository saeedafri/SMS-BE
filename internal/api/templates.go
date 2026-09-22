package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func templateResponse(t store.Template) gen.Template {
	template := gen.Template{
		Id:              t.ID,
		SenderId:        t.SenderID,
		Name:            t.Name,
		Channel:         gen.ChannelId(t.Channel),
		Country:         gen.CountryCode(t.Country),
		Body:            t.Body,
		Variables:       t.Variables,
		CtaUrl:          t.CtaURL,
		Status:          gen.ApprovalStatus(t.Status),
		RejectionReason: t.RejectionReason,
		CreatedAt:       t.CreatedAt,
		// The carrier's separate approval, on RCS only. A template can be
		// approved here and unknown to the carrier, and this is the field that
		// says which of the two is blocking a send.
		CarrierRegistration: carrierRegistrationResponse(t),
	}
	if t.Category != nil {
		// Nullable oneOf in the contract, so a generated union type rather than
		// a plain enum. The error can only arise if the value fails to marshal,
		// which a fixed string cannot.
		var category gen.Template_Category
		_ = category.FromTemplateCategory(gen.TemplateCategory(*t.Category))
		template.Category = &category
	}
	// The customer's own DLT content-template id, handed back exactly as they
	// supplied it. A blank one stays blank: an approved template with no id is
	// the honest answer when DLT has not issued one.
	template.RegistrationId = t.ExternalID
	if t.DltCategory != nil {
		var dltCategory gen.Template_DltCategory
		_ = dltCategory.FromDltCategory(gen.DltCategory(*t.DltCategory))
		template.DltCategory = &dltCategory
	}
	if template.Variables == nil {
		template.Variables = []string{}
	}

	// Channel-specific content, stored as the contract's own JSON and handed
	// straight back. The generated wrappers hold an unexported raw message and
	// are populated by unmarshalling into them, which is also what validates
	// the discriminator: content whose `kind` is not one of the union's
	// variants fails here rather than reaching the screen.
	//
	// A decode failure drops the field instead of failing the request. The rest
	// of the template — its name, status and approval state — is still true and
	// still worth showing; refusing the whole list because one template has a
	// malformed card would take out the page that lets someone fix it.
	if len(t.RCSContent) > 0 {
		var content gen.Template_RcsContent
		if err := json.Unmarshal(t.RCSContent, &content); err == nil {
			template.RcsContent = &content
		}
	}
	if len(t.WAContent) > 0 {
		var content gen.Template_WaContent
		if err := json.Unmarshal(t.WAContent, &content); err == nil {
			template.WaContent = &content
		}
	}
	if len(t.EmailContent) > 0 {
		var content gen.Template_EmailContent
		if err := json.Unmarshal(t.EmailContent, &content); err == nil {
			template.EmailContent = &content
		}
	}
	return template
}

func (s *Server) ListTemplates(ctx context.Context, request gen.ListTemplatesRequestObject) (gen.ListTemplatesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	page, ok2 := pageNumber(request.Params.Page)
	if !ok2 {
		return gen.ListTemplates422JSONResponse(errorBody(codeValidation, pageTooLow)), nil
	}
	filter := store.CatalogueFilter{Page: page, Search: searchTerm(request.Params.Q)}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListTemplates422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	filter.Limit = limit
	filter.Status = optionalEnum(request.Params.Status)
	filter.Channel = optionalEnum(request.Params.Channel)
	filter.Country = optionalEnum(request.Params.Country)
	items, total, err := store.ListTemplates(ctx, s.DB, identity, filter)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Template, 0, len(items))
	for _, item := range items {
		out = append(out, templateResponse(item))
	}
	return gen.ListTemplates200JSONResponse(gen.TemplatePage{Templates: out, Total: total}), nil
}

func (s *Server) GetTemplate(ctx context.Context, request gen.GetTemplateRequestObject) (gen.GetTemplateResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	template, err := store.GetTemplate(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetTemplate404JSONResponse(errorBody(codeNotFound, "No such template.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetTemplate200JSONResponse(templateResponse(template)), nil
}

// templateContent picks the one content payload that belongs to this channel
// and refuses the ones that do not. It returns the payloads as raw JSON plus a
// customer-facing reason, empty when the request is acceptable.
//
// The rule is deliberately strict rather than lenient. Silently dropping a
// WhatsApp payload sent to an SMS sender would save the customer an error and
// cost them a template that looks saved but has lost its buttons — a failure
// they would only discover when a campaign went out plain.
func templateContent(channel string, body *gen.CreateTemplateJSONRequestBody) (rcs, wa, email []byte, reason string) {
	// Each entry: the channel that owns this content, what to call it, and
	// whether the request carried it. Table-driven so the check and the error
	// message cannot drift apart as channels are added.
	supplied := []struct {
		channel string
		label   string
		present bool
		encode  func() ([]byte, error)
	}{
		{"RCS", "RCS content", body.RcsContent != nil,
			func() ([]byte, error) { return body.RcsContent.MarshalJSON() }},
		{"WHATSAPP", "WhatsApp content", body.WaContent != nil,
			func() ([]byte, error) { return body.WaContent.MarshalJSON() }},
		{"EMAIL", "Email content", body.EmailContent != nil,
			func() ([]byte, error) { return body.EmailContent.MarshalJSON() }},
	}
	for _, item := range supplied {
		if !item.present {
			continue
		}
		if item.channel != channel {
			return nil, nil, nil, fmt.Sprintf(
				"%s cannot be used with a %s sender.", item.label, channel)
		}
		encoded, err := item.encode()
		if err != nil {
			return nil, nil, nil, fmt.Sprintf("%s could not be read.", item.label)
		}
		switch item.channel {
		case "RCS":
			rcs = encoded
		case "WHATSAPP":
			wa = encoded
		case "EMAIL":
			email = encoded
		}
	}
	return rcs, wa, email, ""
}

func (s *Server) CreateTemplate(ctx context.Context, request gen.CreateTemplateRequestObject) (gen.CreateTemplateResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}

	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return gen.CreateTemplate422JSONResponse(
			errorBody(codeValidation, "A template name is required.")), nil
	}

	// The template inherits channel and country from its sender rather than
	// taking them from the request: a template that claimed a different country
	// from the sender it sends through would be unenforceable at send time.
	//
	// A sender belonging to another tenant is invisible under RLS, so this is a
	// 422 rather than a 404 — answering "not found" for an id that does exist
	// elsewhere would confirm its existence to a prober.
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.Body.SenderId)
	if errors.Is(err, store.ErrNotFound) {
		return gen.CreateTemplate422JSONResponse(
			errorBody(codeValidation, "That sender does not exist.")), nil
	}
	if err != nil {
		return nil, err
	}

	regime, known := compliance.For(sender.Country)
	if !known {
		return gen.CreateTemplate422JSONResponse(errorBody(codeValidation,
			"That sender's country has no compliance regime.")), nil
	}

	var body *string
	variables := []string{}
	if request.Body.Body != nil {
		text := *request.Body.Body
		if result := compliance.ValidateBody(text); !result.OK {
			return gen.CreateTemplate422JSONResponse(
				errorBody(codeValidation, result.Reason)), nil
		}
		variables = compliance.ParseVariables(text)
		body = &text
	}

	// The regime owns this rule. India rejects public shorteners under DLT;
	// 10DLC has no such restriction. The frontend validates too, but a
	// client-side rule is a hint — this is the control.
	if request.Body.CtaUrl != nil && *request.Body.CtaUrl != "" {
		if result := regime.ValidateCtaURL(*request.Body.CtaUrl); !result.OK {
			return gen.CreateTemplate422JSONResponse(
				errorBody(codeValidation, result.Reason)), nil
		}
	}

	var category *string
	if request.Body.Category != nil {
		if decoded, err := request.Body.Category.AsTemplateCategory(); err == nil {
			value := string(decoded)
			category = &value
		}
	}

	// India's taxonomy, kept apart from Meta's above.
	//
	// Both enums spell TRANSACTIONAL and mean different things: Meta's is an
	// ordinary category, DLT's is restricted to banking and OTP traffic. A
	// value outside the four is refused rather than stored, because a template
	// mis-filed under DLT is not rejected by us — it is scrubbed by the carrier,
	// silently, after the customer believes they are live.
	//
	// The contract declares this as `nullable` with a `oneOf`, which the
	// generator turns into a union wrapper rather than a plain enum, so the
	// value comes out through an accessor. A malformed one is a validation
	// failure, not a 500 — the same answer as a value outside the four.
	var dltCategory *string
	if request.Body.DltCategory != nil {
		category, err := request.Body.DltCategory.AsDltCategory()
		if err != nil || !oneOf(string(category), validDltCategories) {
			return gen.CreateTemplate422JSONResponse(errorBody(codeValidation,
				enumMessage("dltCategory", validDltCategories))), nil
		}
		value := string(category)
		dltCategory = &value
	}

	// Rich content is accepted only for the channel that has it. The sender
	// decides the channel, so a request that sends WhatsApp buttons through an
	// SMS sender is rejected here rather than stored and rejected later by the
	// database — the customer gets a sentence they can act on instead of a 500.
	//
	// Marshalling back to JSON is not a round trip for its own sake: the
	// generated union already validated the discriminator when the request was
	// decoded, so what comes out is the contract's canonical form.
	rcsContent, waContent, emailContent, contentErr := templateContent(
		sender.Channel, request.Body)
	if contentErr != "" {
		return gen.CreateTemplate422JSONResponse(
			errorBody(codeValidation, contentErr)), nil
	}

	// Variables appear in rich content too — an RCS card's title or a WhatsApp
	// button body can carry {{first_name}} just as a plain body can. Parsing
	// only `body` meant an RCS template reported no variables at all, so the
	// send-time substitution had nothing to fill in.
	if len(variables) == 0 {
		for _, raw := range [][]byte{rcsContent, waContent, emailContent} {
			if len(raw) > 0 {
				variables = compliance.ParseVariables(string(raw))
				break
			}
		}
	}

	created, err := store.CreateTemplate(ctx, s.DB, identity, store.Template{
		SenderID: sender.ID,
		Name:     name,
		Channel:  sender.Channel,
		Country:  sender.Country,
		Body:     body,
		Category: category,
		// Both come from the customer's own DLT registration and are stored
		// verbatim. Nothing here mints one.
		ExternalID:   request.Body.RegistrationId,
		DltCategory:  dltCategory,
		Variables:    variables,
		CtaURL:       request.Body.CtaUrl,
		RCSContent:   rcsContent,
		WAContent:    waContent,
		EmailContent: emailContent,
	})
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateTemplate409JSONResponse(errorBody(codeConflict,
			"A template with that name already exists.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateTemplate201JSONResponse(templateResponse(created)), nil
}

// templateSubstanceFields are the parts of a template a review is ABOUT: the
// words a recipient reads and the registry ids those words are registered
// under. name is deliberately absent — it is the platform's own label, which no
// regulator and no carrier ever saw.
var templateSubstanceFields = []string{"body", "rcsContent", "ctaUrl", "category",
	"registrationId", "dltCategory"}

// templateCategories is the category taxonomy each channel declares.
//
// SMS and RCS declare none on purpose: in India their taxonomy is DLT's, and it
// lives in dltCategory. Accepting Meta's MARKETING on an Indian SMS template
// would record a classification no operator reads, beside the one they do.
var templateCategories = map[string][]string{
	"WHATSAPP": {"MARKETING", "UTILITY", "AUTHENTICATION"},
	"EMAIL":    {"MARKETING", "TRANSACTIONAL", "AUTHENTICATION"},
	"VOICE":    {"MARKETING", "TRANSACTIONAL", "AUTHENTICATION"},
}

// UpdateTemplate renames a template, or corrects one no registry has approved.
//
// Two tiers with opposite rules, and the split is the whole design. The name is
// editable in every status. The substance is editable only while the template
// is still working through approval: an approved body is what DLT approved and,
// on RCS, what the carrier approved, and in India the content-template id is
// registered against those exact words — so changing either would leave the
// platform sending copy no registry has seen, under an id pointing at
// different words.
func (s *Server) UpdateTemplate(ctx context.Context, request gen.UpdateTemplateRequestObject) (
	gen.UpdateTemplateResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}
	template, err := store.GetTemplate(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateTemplate404JSONResponse(
			errorBody(codeNotFound, "No such template.")), nil
	}
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return gen.UpdateTemplate200JSONResponse(templateResponse(template)), nil
	}

	// What the request ASKS to change, not what would differ afterwards. A
	// hand-built body that re-sends the identical words is still asking to set
	// them, and an edit drawer that diffs first simply never sends the key.
	asked := []string{}
	for _, field := range templateSubstanceFields {
		if bodyMentions(ctx, field) {
			asked = append(asked, field)
		}
	}
	touchesSubstance := len(asked) > 0

	// The one way into a frozen template, and it is narrow on purpose: filling
	// in a registry id that is still blank.
	//
	// Blank-to-value cannot contradict what DLT approved, because there was
	// nothing there to contradict — while changing an id that is already set
	// would point registered words at a different registration. Without this a
	// template approved before DLT returned its content-template id could never
	// be given one, and it can never send either: SMPPRouter.Submit refuses an
	// Indian SMS with no DLT ids rather than letting the operator scrub it.
	// Alone in the request, too: a body travelling beside it is still a body.
	fillingBlankRegistrationID := len(asked) == 1 && asked[0] == "registrationId" &&
		(template.ExternalID == nil || strings.TrimSpace(*template.ExternalID) == "")

	switch {
	case !touchesSubstance, fillingBlankRegistrationID:
	case template.Status == "approved", template.Status == "blocked",
		template.Status == "expired":
		// Atomic: a name in the same body is refused with the rest rather than
		// applied on its own, so the caller never half-succeeds.
		//
		// No article in front of the status: "a approved template" reads wrong
		// for the two statuses this fires on most.
		return gen.UpdateTemplate409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("The words of this template cannot be changed — it is %s, and they "+
				"are what the registry approved. Rename it, or create a new template.",
				template.Status))), nil
	}

	regime, known := compliance.For(template.Country)
	if !known {
		return gen.UpdateTemplate422JSONResponse(errorBody(codeValidation,
			"That template's country has no compliance regime.")), nil
	}

	edited := template
	if request.Body.Name != nil {
		name := strings.TrimSpace(*request.Body.Name)
		if name == "" {
			return gen.UpdateTemplate422JSONResponse(
				errorBody(codeValidation, "A template name is required.")), nil
		}
		edited.Name = name
	}
	if problem := applyTemplateSubstance(ctx, &edited, regime, request.Body); problem != "" {
		return gen.UpdateTemplate422JSONResponse(errorBody(codeValidation, problem)), nil
	}

	// Re-derived from whatever words the template now carries, never taken from
	// the caller — the same rule as create, applied to the record as it will
	// end up rather than to the fields this request happened to send.
	edited.Variables = templateVariables(edited)

	// The rejection was a decision about the old copy, so once the copy changes
	// it no longer applies to anything. Without this the customer fixes exactly
	// what the reason told them to fix and the template reads rejected for
	// ever. A rename alone must not resubmit it.
	if touchesSubstance && edited.Status == "rejected" {
		edited.Status = "pending_review"
		edited.RejectionReason = nil
	}

	updated, err := store.UpdateTemplate(ctx, s.DB, identity, edited)
	if errors.Is(err, store.ErrConflict) {
		return gen.UpdateTemplate409JSONResponse(errorBody(codeConflict,
			"A template with that name already exists.")), nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateTemplate404JSONResponse(
			errorBody(codeNotFound, "No such template.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateTemplate200JSONResponse(templateResponse(updated)), nil
}

// applyTemplateSubstance writes the reviewed fields onto the record and returns
// a customer-facing reason, empty when the request is acceptable.
//
// Every rule here is about the CHANNEL declaring the field at all. Content
// belonging to a channel that has none of it is refused rather than stored: a
// silently dropped payload costs the customer a template that looks saved and
// has lost the thing they edited.
func applyTemplateSubstance(ctx context.Context, edited *store.Template,
	regime compliance.Regime, body *gen.UpdateTemplateJSONRequestBody) string {

	if body.Body != nil {
		if edited.Channel != "SMS" && edited.Channel != "VOICE" {
			return "A " + edited.Channel + " template's words live in its content, not in body."
		}
		text := *body.Body
		if strings.TrimSpace(text) == "" {
			return "A template with no words is not a template."
		}
		if result := compliance.ValidateBody(text); !result.OK {
			return result.Reason
		}
		edited.Body = &text
	}
	if body.RcsContent != nil {
		if edited.Channel != "RCS" {
			return "rcsContent applies to RCS templates only."
		}
		raw, err := json.Marshal(*body.RcsContent)
		if err != nil {
			return "That RCS content could not be read."
		}
		// The country's link rule reaches inside the card. A card's every
		// string is walked, because a shortener in a suggestion's URL is the
		// same violation as one in the body and DLT drops the message either
		// way.
		for _, url := range compliance.ExtractURLs(string(raw)) {
			if result := regime.ValidateCtaURL(url); !result.OK {
				return result.Reason
			}
		}
		edited.RCSContent = raw
	}

	// Sent-and-null is a different request from omitted, and both arrive as a
	// nil pointer, so the raw body decides which one this is.
	if body.CtaUrl != nil || bodyMentions(ctx, "ctaUrl") {
		if edited.Channel != "SMS" {
			return "ctaUrl applies to SMS templates only."
		}
		switch {
		case body.CtaUrl == nil:
			edited.CtaURL = nil
		default:
			if result := regime.ValidateCtaURL(*body.CtaUrl); !result.OK {
				return result.Reason
			}
			edited.CtaURL = body.CtaUrl
		}
	}
	if body.Category != nil {
		allowed, declared := templateCategories[edited.Channel]
		if !declared {
			return edited.Channel + " templates carry no category."
		}
		value := string(*body.Category)
		if !oneOf(value, allowed) {
			return enumMessage("category", allowed)
		}
		edited.Category = &value
	}

	// India's registry ids, on the channels India registers. Elsewhere there is
	// no such identifier to carry, so storing one would record an answer to a
	// question the regulator never asked.
	registers := regime.RequiresRegistrationID(compliance.TierTemplate) &&
		(edited.Channel == "SMS" || edited.Channel == "RCS")
	if body.RegistrationId != nil || bodyMentions(ctx, "registrationId") {
		if !registers {
			return "This country and channel carry no template registration id."
		}
		if body.RegistrationId == nil || strings.TrimSpace(*body.RegistrationId) == "" {
			return "This country's regulator issues a registration id for a template, " +
				"so it cannot be cleared."
		}
		edited.ExternalID = body.RegistrationId
	}
	if body.DltCategory != nil {
		if !registers {
			return "This country and channel carry no DLT category."
		}
		value := string(*body.DltCategory)
		if !oneOf(value, validDltCategories) {
			return enumMessage("dltCategory", validDltCategories)
		}
		edited.DltCategory = &value
	}
	return ""
}

// templateVariables is the create path's rule, applied to a whole record: the
// body's tokens, or — for a template whose words live in rich content — every
// token anywhere in that content.
func templateVariables(template store.Template) []string {
	if template.Body != nil && *template.Body != "" {
		return compliance.ParseVariables(*template.Body)
	}
	for _, raw := range [][]byte{template.RCSContent, template.WAContent, template.EmailContent} {
		if len(raw) > 0 {
			return compliance.ParseVariables(string(raw))
		}
	}
	return []string{}
}

// DeleteTemplate removes a template nothing is using.
//
// Allowed in every status, approved included: retiring a template is a
// legitimate thing to do, and what gates it is USE rather than standing — the
// opposite of the rule on editing. This removes the platform's record only; a
// DLT registration or a carrier approval is held by them and withdrawn with
// them.
func (s *Server) DeleteTemplate(ctx context.Context, request gen.DeleteTemplateRequestObject) (
	gen.DeleteTemplateResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return nil, errForbidden
	}
	template, err := store.GetTemplate(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DeleteTemplate404JSONResponse(
			errorBody(codeNotFound, "No such template.")), nil
	}
	if err != nil {
		return nil, err
	}

	refs, err := store.CountTemplateReferences(ctx, s.DB, identity, request.Id)
	if err != nil {
		return nil, err
	}
	refs.VerifyServices, err = s.countVerifyServicesHeldBy(ctx, identity, template)
	if err != nil {
		return nil, err
	}
	if refs.Total() > 0 {
		return gen.DeleteTemplate409JSONResponse(errorBody(codeConflict,
			"This template is still used by "+describeTemplateUse(refs)+
				". Remove those first.")), nil
	}

	if err := store.DeleteTemplate(ctx, s.DB, identity, request.Id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.DeleteTemplate404JSONResponse(
				errorBody(codeNotFound, "No such template.")), nil
		}
		return nil, err
	}
	return gen.DeleteTemplate204Response{}, nil
}

// countVerifyServicesHeldBy counts the Verify services this template is holding
// live.
//
// A service holds no template id at all: its channel carries OTP copy, and the
// service is live only while an approved template on the same sender matches
// that copy. So the reference is a copy match, and it is read back through the
// very function that decides the live badge — counting it any other way would
// let the delete guard and the badge disagree about the same template.
func (s *Server) countVerifyServicesHeldBy(ctx context.Context, identity store.Identity,
	template store.Template) (int, error) {

	services, err := store.ListVerifyServices(ctx, s.DB, identity)
	if err != nil {
		return 0, err
	}
	held := 0
	for _, service := range services {
		for _, channel := range service.Channels {
			senderID, valid := parsePathID(channel.SenderID)
			if !valid || senderID != template.SenderID {
				continue
			}
			matched, err := s.matchOTPTemplate(ctx, identity, senderID, channel.Body,
				service.CodeLength)
			if err != nil {
				return 0, err
			}
			if matched != nil && matched.ID == template.ID {
				held++
				break
			}
		}
	}
	return held, nil
}

// describeTemplateUse turns the counts into the sentence the caller acts on:
// "1 campaign, 1 verify service".
func describeTemplateUse(refs store.TemplateReferences) string {
	parts := []string{}
	for _, kind := range []struct {
		count     int
		one, many string
	}{
		{refs.Campaigns, "campaign", "campaigns"},
		{refs.CampaignFallback, "campaign fallback", "campaign fallbacks"},
		{refs.Journeys, "journey", "journeys"},
		{refs.VerifyServices, "verify service", "verify services"},
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
