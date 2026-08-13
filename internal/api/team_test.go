package api_test

import (
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func listTeam(t *testing.T, h *harness, token string) gen.TeamMemberPage {
	t.Helper()
	res := h.do(http.MethodGet, "/v1/team", token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /v1/team: status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var page gen.TeamMemberPage
	res.decode(t, &page)
	return page
}

func TestTeamListsTheOwner(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	page := listTeam(t, h, acct.Token)
	if len(page.Members) != 1 {
		t.Fatalf("got %d members, want 1", len(page.Members))
	}
	member := page.Members[0]
	if member.Id != acct.UserID {
		t.Errorf("id = %s, want %s", member.Id, acct.UserID)
	}
	if member.Role != gen.TeamRole("owner") {
		t.Errorf("role = %q, want owner", member.Role)
	}
	if member.Status != gen.TeamMemberStatus("active") {
		t.Errorf("status = %q, want active", member.Status)
	}
	// The contract says invitedAt is null for active members.
	if member.InvitedAt != nil {
		t.Errorf("invitedAt = %v for an active member, want null", member.InvitedAt)
	}
	if member.Name == nil || *member.Name == "" {
		t.Error("name is null for an active member, want a value")
	}
}

func TestTeamForbiddenForMemberRole(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("member")

	res := h.do(http.MethodGet, "/v1/team", acct.Token, nil)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
	}
	if code := res.errorCode(t); code != "forbidden" {
		t.Fatalf("error.code = %q, want forbidden", code)
	}
}

func TestInviteCreatesAPendingMember(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	invitee := "invitee-" + acct.TenantID.String()[:8] + "@example.test"
	h.trackTenant(invitee)

	res := h.do(http.MethodPost, "/v1/team/invite", acct.Token,
		map[string]string{"email": invitee, "role": "member"})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var member gen.TeamMember
	res.decode(t, &member)

	if string(member.Email) != invitee {
		t.Errorf("email = %s, want %s", member.Email, invitee)
	}
	if member.Status != gen.TeamMemberStatus("invited") {
		t.Errorf("status = %q, want invited", member.Status)
	}
	// The contract is explicit: an invited member's name is null until they
	// accept and choose one.
	if member.Name != nil {
		t.Errorf("name = %v for an invited member, want null", *member.Name)
	}
	if member.InvitedAt == nil {
		t.Error("invitedAt is null for an invited member, want a timestamp")
	}

	if page := listTeam(t, h, acct.Token); len(page.Members) != 2 {
		t.Fatalf("after inviting, team has %d members, want 2", len(page.Members))
	}
}

func TestInviteRejectsSomeoneAlreadyOnTheTeam(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/team/invite", acct.Token,
		map[string]string{"email": acct.Email, "role": "member"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
}

// An admin promoting someone to owner would be an escalation past their own
// level, so only an owner may do it.
func TestOnlyAnOwnerCanCreateAnotherOwner(t *testing.T) {
	h := newHarness(t)
	admin := h.newAccount("admin")
	invitee := "escalate-" + admin.TenantID.String()[:8] + "@example.test"
	h.trackTenant(invitee)

	res := h.do(http.MethodPost, "/v1/team/invite", admin.Token,
		map[string]string{"email": invitee, "role": "owner"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
	}
}

// Demoting the only owner would leave nobody able to promote anyone — an
// unrecoverable state, so it must be refused rather than repaired later.
func TestCannotDemoteTheLastOwner(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPatch, "/v1/team/"+acct.UserID.String(), acct.Token,
		map[string]string{"role": "member"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
	if code := res.errorCode(t); code != "validation_failed" {
		t.Fatalf("error.code = %q, want validation_failed", code)
	}

	// And the role must be unchanged.
	page := listTeam(t, h, acct.Token)
	if page.Members[0].Role != gen.TeamRole("owner") {
		t.Fatalf("role = %q after a refused demotion, want owner", page.Members[0].Role)
	}
}

func TestCannotRemoveTheLastOwner(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodDelete, "/v1/team/"+acct.UserID.String(), acct.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
	if page := listTeam(t, h, acct.Token); len(page.Members) != 1 {
		t.Fatal("the last owner was removed despite the refusal")
	}
}

func TestDemotingAnOwnerIsAllowedWhenAnotherExists(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	second := "second-owner-" + acct.TenantID.String()[:8] + "@example.test"
	h.trackTenant(second)

	invite := h.do(http.MethodPost, "/v1/team/invite", acct.Token,
		map[string]string{"email": second, "role": "owner"})
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite second owner: status = %d; body = %s", invite.Code, invite.Body)
	}

	res := h.do(http.MethodPatch, "/v1/team/"+acct.UserID.String(), acct.Token,
		map[string]string{"role": "admin"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var member gen.TeamMember
	res.decode(t, &member)
	if member.Role != gen.TeamRole("admin") {
		t.Fatalf("role = %q, want admin", member.Role)
	}
}

// Removing someone must log them out at once, not whenever their token
// happens to expire.
func TestRemovingAMemberRevokesTheirSessions(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	victim := h.newAccount("member")

	// Put the victim into the owner's tenant so the owner can remove them.
	if _, err := h.admin.Exec(t.Context(),
		`UPDATE tenant_users SET tenant_id = $1 WHERE user_id = $2`,
		owner.TenantID, victim.UserID); err != nil {
		t.Fatalf("move victim: %v", err)
	}
	if _, err := h.admin.Exec(t.Context(),
		`UPDATE sessions SET tenant_id = $1 WHERE user_id = $2`,
		owner.TenantID, victim.UserID); err != nil {
		t.Fatalf("move victim session: %v", err)
	}
	if res := h.do(http.MethodGet, "/v1/me", victim.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("precondition: victim cannot authenticate, status = %d", res.Code)
	}

	if res := h.do(http.MethodDelete, "/v1/team/"+victim.UserID.String(),
		owner.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("remove: status = %d, want 204; body = %s", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/me", victim.Token, nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("removed member still authenticates: status = %d", res.Code)
	}
}

func TestTeamOperationsCannotReachAnotherTenant(t *testing.T) {
	h := newHarness(t)
	attacker := h.newAccount("owner")
	victim := h.newAccount("owner")

	res := h.do(http.MethodPatch, "/v1/team/"+victim.UserID.String(), attacker.Token,
		map[string]string{"role": "member"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant role change: status = %d, want 404; body = %s", res.Code, res.Body)
	}

	remove := h.do(http.MethodDelete, "/v1/team/"+victim.UserID.String(), attacker.Token, nil)
	if remove.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant removal: status = %d, want 404; body = %s", remove.Code, remove.Body)
	}

	// The victim must be untouched.
	if page := listTeam(t, h, victim.Token); page.Members[0].Role != gen.TeamRole("owner") {
		t.Fatal("another tenant changed the victim's role")
	}
}
