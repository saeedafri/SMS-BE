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

	// Lift a supplied registrationId out of the free-form bag into its typed
	// column, and take it out of the bag.
	//
	// The bag's shape is owned by the regime, so that is where the customer
	// types their DLT id. Leaving a copy behind would give the same value two
	// homes that a later edit could pull apart, and the typed column is the one
	// every reader uses. Keyed on the field being present rather than on the
	// country: this is "did they give us an id", not a per-country special case.
	//
	// The contract also carries registrationId at the top level of the body,
	// and that is what the console sends. It wins: a caller that sends both
	// means the typed one. The bag is still emptied either way, so the two can
	// never disagree later.
	registrationID := liftRegistrationID(fields)
	if request.Body.RegistrationId != nil &&
		strings.TrimSpace(*request.Body.RegistrationId) != "" {
		supplied := strings.TrimSpace(*request.Body.RegistrationId)
		registrationID = &supplied
	}

	created, err := store.CreateRegistration(ctx, s.DB, identity, store.Registration{
		Country: country, ObjectKey: objectKey, Fields: fields,
		ExternalID: registrationID,
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

// liftRegistrationID removes a registrationId from a submitted fields bag and
// returns it, so the value lives in exactly one place.
//
// Blank or whitespace is treated as absent: a customer who tabs through the
// field without typing has not given us a DLT id, and storing "" would make an
// empty string look like an answer to every reader downstream.
func liftRegistrationID(fields map[string]any) *string {
	raw, present := fields["registrationId"]
	if !present {
		return nil
	}
	delete(fields, "registrationId")
	value := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if value == "" {
		return nil
	}
	return &value
}
