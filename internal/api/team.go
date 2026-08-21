package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func teamMemberResponse(m store.TeamMember) gen.TeamMember {
	return gen.TeamMember{
		Id:        m.ID,
		Name:      m.Name,
		Email:     openapi_types.Email(m.Email),
		Role:      gen.TeamRole(m.Role),
		Status:    gen.TeamMemberStatus(m.Status),
		InvitedAt: m.InvitedAt,
	}
}

func (s *Server) GetTeamMembers(ctx context.Context, _ gen.GetTeamMembersRequestObject) (gen.GetTeamMembersResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetTeamMembers401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.GetTeamMembers403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to team settings.")), nil
	}
	members, err := store.ListTeamMembers(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.TeamMember, 0, len(members))
	for _, m := range members {
		out = append(out, teamMemberResponse(m))
	}
	return gen.GetTeamMembers200JSONResponse(gen.TeamMemberPage{Members: out}), nil
}

func (s *Server) InviteTeamMember(ctx context.Context, request gen.InviteTeamMemberRequestObject) (gen.InviteTeamMemberResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.InviteTeamMember401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.InviteTeamMember403JSONResponse(
			errorBody(codeForbidden, "Member role cannot invite teammates.")), nil
	}
	email := strings.ToLower(strings.TrimSpace(string(request.Body.Email)))
	if !strings.Contains(email, "@") {
		return gen.InviteTeamMember422JSONResponse(
			errorBody(codeValidation, "A valid email address is required.")), nil
	}
	// Only an owner may create another owner; an admin promoting someone to
	// owner would be an escalation past their own level.
	role := string(request.Body.Role)
	// Checked before the escalation rule below, not after: that rule only asks
	// whether the role IS "owner", so an unrecognised one slips past it and
	// lands on the tenant_users CHECK constraint as a 500.
	if !oneOf(role, validRoles) {
		return gen.InviteTeamMember422JSONResponse(
			errorBody(codeValidation, enumMessage("Role", validRoles))), nil
	}
	if role == "owner" && identity.Role != "owner" {
		return gen.InviteTeamMember403JSONResponse(
			errorBody(codeForbidden, "Only an owner can invite another owner.")), nil
	}

	member, err := store.InviteTeamMember(ctx, s.DB, identity, email, role)
	if errors.Is(err, store.ErrConflict) {
		return gen.InviteTeamMember422JSONResponse(
			errorBody(codeValidation,
				// Wording matches the frontend's copy for this case. It names
				// the reason — the ADDRESS is taken — rather than the outcome,
				// which is what tells the person what to change.
				"This email already belongs to a team member.")), nil
	}
	if err != nil {
		return nil, err
	}
	s.recordActivity(ctx, identity, store.ActivityTeamInvite,
		fmt.Sprintf("Invited %s as %s", member.Email, member.Role))
	s.sendInviteEmail(identity.TenantName, member.Email, member.Role)
	return gen.InviteTeamMember201JSONResponse(teamMemberResponse(member)), nil
}

func (s *Server) UpdateTeamMemberRole(ctx context.Context, request gen.UpdateTeamMemberRoleRequestObject) (gen.UpdateTeamMemberRoleResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UpdateTeamMemberRole401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateTeamMemberRole403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change roles.")), nil
	}
	memberID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.UpdateTeamMemberRole404JSONResponse(
			errorBody(codeNotFound, "No such team member.")), nil
	}
	role := string(request.Body.Role)
	if !oneOf(role, validRoles) {
		return gen.UpdateTeamMemberRole422JSONResponse(
			errorBody(codeValidation, enumMessage("Role", validRoles))), nil
	}
	if role == "owner" && identity.Role != "owner" {
		return gen.UpdateTeamMemberRole403JSONResponse(
			errorBody(codeForbidden, "Only an owner can promote someone to owner.")), nil
	}

	member, err := store.UpdateTeamMemberRole(ctx, s.DB, identity, memberID, role)
	switch {
	case errors.Is(err, store.ErrLastOwner):
		return gen.UpdateTeamMemberRole422JSONResponse(errorBody(codeValidation,
			"This is the only owner. Promote someone else to owner first.")), nil
	case errors.Is(err, store.ErrNotFound):
		return gen.UpdateTeamMemberRole404JSONResponse(
			errorBody(codeNotFound, "No such team member.")), nil
	case err != nil:
		return nil, err
	}
	s.recordActivity(ctx, identity, store.ActivityTeamRoleChange,
		fmt.Sprintf("Changed %s to %s", member.Email, member.Role))
	return gen.UpdateTeamMemberRole200JSONResponse(teamMemberResponse(member)), nil
}

func (s *Server) RemoveTeamMember(ctx context.Context, request gen.RemoveTeamMemberRequestObject) (gen.RemoveTeamMemberResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RemoveTeamMember401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.RemoveTeamMember403JSONResponse(
			errorBody(codeForbidden, "Member role cannot remove teammates.")), nil
	}
	memberID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.RemoveTeamMember404JSONResponse(
			errorBody(codeNotFound, "No such team member.")), nil
	}

	err = store.RemoveTeamMember(ctx, s.DB, identity, memberID)
	switch {
	case errors.Is(err, store.ErrLastOwner):
		return gen.RemoveTeamMember422JSONResponse(errorBody(codeValidation,
			"This is the only owner. Promote someone else to owner first.")), nil
	case errors.Is(err, store.ErrNotFound):
		return gen.RemoveTeamMember404JSONResponse(
			errorBody(codeNotFound, "No such team member.")), nil
	case err != nil:
		return nil, err
	}
	return gen.RemoveTeamMember204Response{}, nil
}
