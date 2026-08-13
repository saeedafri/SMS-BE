package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func registrationResponse(r store.Registration) gen.Registration {
	fields := r.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	return gen.Registration{
		Id:              r.ID,
		Country:         gen.CountryCode(r.Country),
		ObjectKey:       r.ObjectKey,
		Status:          gen.ApprovalStatus(r.Status),
		RejectionReason: r.RejectionReason,
		RegistrationId:  r.ExternalID,
		Fields:          fields,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func (s *Server) ListRegistrations(ctx context.Context, _ gen.ListRegistrationsRequestObject) (gen.ListRegistrationsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	registrations, err := store.ListRegistrations(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Registration, 0, len(registrations))
	for _, registration := range registrations {
		out = append(out, registrationResponse(registration))
	}
	return gen.ListRegistrations200JSONResponse(out), nil
}

func (s *Server) GetRegistration(ctx context.Context, request gen.GetRegistrationRequestObject) (gen.GetRegistrationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	registration, err := store.GetRegistration(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetRegistration404JSONResponse(
			errorBody(codeNotFound, "No such registration.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetRegistration200JSONResponse(registrationResponse(registration)), nil
}

func (s *Server) CreateRegistration(ctx context.Context, request gen.CreateRegistrationRequestObject) (gen.CreateRegistrationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.CreateRegistration403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to compliance.")), nil
	}

	country := string(request.Body.Country)
	regime, known := compliance.For(country)
	if !known {
		return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("We do not operate in %q yet.", country))), nil
	}
	// A stub regime and an unknown country are different facts, and the user
	// can act on the difference: "nothing to register here yet" versus "we do
	// not serve that country".
	if regime.Stub() {
		return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("%s does not require registrations yet.", regime.Label()))), nil
	}

	objectKey := strings.TrimSpace(request.Body.ObjectKey)
	object, exists := regime.Object(objectKey)
	if !exists {
		return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("%q is not something %s registers.", objectKey, regime.Label()))), nil
	}

	fields := map[string]any(request.Body.Fields)
	if missing := compliance.MissingRequired(object, fields); len(missing) > 0 {
		return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
			"Missing required: "+strings.Join(missing, ", "))), nil
	}

	// Filing order is data on the object, not a branch here: the US campaign
	// cannot be filed until its brand is approved, and the next country that
	// needs an ordering rule gets it for free.
	if object.DependsOn != "" {
		status, err := store.RegistrationStatus(ctx, s.DB, identity, country, object.DependsOn)
		switch {
		case errors.Is(err, store.ErrNotFound):
			dependency, _ := regime.Object(object.DependsOn)
			return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
				fmt.Sprintf("Register %s first.", dependency.Label))), nil
		case err != nil:
			return nil, err
		case status != "approved":
			dependency, _ := regime.Object(object.DependsOn)
			return gen.CreateRegistration422JSONResponse(errorBody(codeValidation,
				fmt.Sprintf("%s must be approved first — it is currently %s.",
					dependency.Label, status))), nil
		}
	}

	created, err := store.CreateRegistration(ctx, s.DB, identity, store.Registration{
		Country: country, ObjectKey: objectKey, Fields: fields,
	})
	if errors.Is(err, store.ErrConflict) {
		return gen.CreateRegistration409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("%s is already registered for %s.", object.Label, regime.Label()))), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateRegistration201JSONResponse(registrationResponse(created)), nil
}
