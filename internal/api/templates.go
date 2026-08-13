package api

import (
	"context"
	"errors"
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
	}
	if t.Category != nil {
		// Nullable oneOf in the contract, so a generated union type rather than
		// a plain enum. The error can only arise if the value fails to marshal,
		// which a fixed string cannot.
		var category gen.Template_Category
		_ = category.FromTemplateCategory(gen.TemplateCategory(*t.Category))
		template.Category = &category
	}
	if template.Variables == nil {
		template.Variables = []string{}
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

	created, err := store.CreateTemplate(ctx, s.DB, identity, store.Template{
		SenderID:  sender.ID,
		Name:      name,
		Channel:   sender.Channel,
		Country:   sender.Country,
		Body:      body,
		Category:  category,
		Variables: variables,
		CtaURL:    request.Body.CtaUrl,
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
