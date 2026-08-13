package api

import (
	"context"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func meResponse(id store.Identity) gen.Me {
	return gen.Me{
		UserId:        id.UserID,
		TenantId:      id.TenantID,
		TenantName:    id.TenantName,
		Name:          id.Name,
		Email:         openapi_types.Email(id.Email),
		Capabilities:  id.Capabilities,
		EmailVerified: id.EmailVerd,
		MfaEnabled:    id.MFAEnabled,
		Country:       gen.CountryCode(id.Country),
		Role:          gen.TeamRole(id.Role),
	}
}

func (s *Server) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetMe401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	return gen.GetMe200JSONResponse(meResponse(identity)), nil
}

func (s *Server) UpdateMe(ctx context.Context, request gen.UpdateMeRequestObject) (gen.UpdateMeResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UpdateMe401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateMe403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to settings.")), nil
	}
	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return gen.UpdateMe422JSONResponse(
			errorBody(codeValidation, "Name must not be empty.")), nil
	}
	if err := store.UpdateUserName(ctx, s.DB, identity, name); err != nil {
		return nil, err
	}
	identity.Name = name
	return gen.UpdateMe200JSONResponse(meResponse(identity)), nil
}

// UpdateTenant renames the organisation. The contract returns Me, not a Tenant:
// the dashboard re-reads the whole identity after a rename so the topbar and
// the settings form stay consistent in one round trip.
func (s *Server) UpdateTenant(ctx context.Context, request gen.UpdateTenantRequestObject) (gen.UpdateTenantResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UpdateTenant401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateTenant403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to organisation settings.")), nil
	}
	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return gen.UpdateTenant422JSONResponse(
			errorBody(codeValidation, "Organisation name must not be empty.")), nil
	}
	if err := store.UpdateTenantName(ctx, s.DB, identity, name); err != nil {
		return nil, err
	}
	identity.TenantName = name
	return gen.UpdateTenant200JSONResponse(meResponse(identity)), nil
}
