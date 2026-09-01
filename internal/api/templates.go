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

func (s *Server) ListTemplates(ctx context.Context, _ gen.ListTemplatesRequestObject) (gen.ListTemplatesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	templates, err := store.ListTemplates(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Template, 0, len(templates))
	for _, template := range templates {
		out = append(out, templateResponse(template))
	}
	return gen.ListTemplates200JSONResponse(out), nil
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
	var dltCategory *string
	if request.Body.DltCategory != nil {
		if !oneOf(string(*request.Body.DltCategory), validDltCategories) {
			return gen.CreateTemplate422JSONResponse(errorBody(codeValidation,
				enumMessage("dltCategory", validDltCategories))), nil
		}
		value := string(*request.Body.DltCategory)
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
