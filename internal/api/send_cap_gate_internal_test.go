package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"

	"github.com/saeedafri/sms-be/internal/store"
)

// Signing in to the operator console is not the same permission as every
// button in it.
//
// operator_users has carried a role since 00018 and nothing read it: the role
// was rendered on GET /v1/operator/me and enforced on no route at all, so any
// signed-in operator could do whatever any other one could. The daily send
// ceiling is the first thing gated on it, and the gate is worth a test of its
// own because it is one comparison standing between a commercial decision and
// everybody who can reach the console.
func TestOnlyAnAdminPassesTheOperatorAdminGate(t *testing.T) {
	t.Parallel()
	server := &Server{}
	withRole := func(role string) context.Context {
		return context.WithValue(context.Background(), operatorKey{},
			store.OperatorIdentity{Role: role})
	}

	if _, err := server.requireOperatorAdmin(withRole("admin")); err != nil {
		t.Fatalf("an admin was refused: %v", err)
	}
	if _, err := server.requireOperatorAdmin(withRole("operator")); !errors.Is(err, errForbidden) {
		t.Fatalf("a plain operator got %v, want errForbidden", err)
	}
	// A role the schema does not have is not a free pass. The CHECK on
	// operator_users admits only 'operator' and 'admin', but a gate that
	// refused ONLY the one name it knows about would open itself the day a
	// third role is added.
	for _, role := range []string{"", "owner", "Admin", "superadmin", "super_admin"} {
		if _, err := server.requireOperatorAdmin(withRole(role)); !errors.Is(err, errForbidden) {
			t.Errorf("role %q got %v, want errForbidden", role, err)
		}
	}
	// And no session at all is unauthenticated rather than forbidden: the two
	// mean different things to a console deciding whether to show a login.
	if _, err := server.requireOperatorAdmin(context.Background()); !errors.Is(err, errUnauthenticated) {
		t.Errorf("no operator session got %v, want errUnauthenticated", err)
	}
}

// The ceiling must not reach a customer, and must not reach a non-admin
// operator either.
//
// This reads the frontend's own contract rather than our handlers, because the
// leak it guards against arrives as a SCHEMA change: the day sendCapPerDay is
// added to a response, this fails unless that response can refuse a non-admin.
// Written now, while the fields do not exist yet, so that it is already in
// place when they do.
//
// The spec comes from the EMBEDDED copy, not from disk. Read from disk it
// passed by skipping: the suite compiles here and runs on the server, where
// there is no repository to find openapi/control.json in, so a guard that
// looked for the file would have been silently absent exactly where it counts.
func TestTheSendCeilingNeverReachesANonAdmin(t *testing.T) {
	t.Parallel()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("load embedded spec: %v", err)
	}

	// The names the ceiling would travel under, matched case-insensitively so
	// a rename to SendCapPerDay or send_cap_per_day is caught too.
	mentionsCeiling := func(value any) bool {
		body, err := json.Marshal(value)
		if err != nil {
			return false
		}
		text := strings.ToLower(string(body))
		for _, field := range []string{"sendcapperday", "sendacceptedtoday", "sendwithheldtoday"} {
			if strings.Contains(text, field) {
				return true
			}
		}
		return false
	}

	// Every schema a TENANT can be served. A ceiling on any of these undoes the
	// whole feature: the customer is not meant to know one exists.
	for _, name := range []string{"Campaign", "CampaignEstimate", "CampaignDetail",
		"SendMessageResult", "Me", "Tenant", "WalletBalance"} {
		schema, declared := spec.Components.Schemas[name]
		if !declared {
			continue
		}
		if mentionsCeiling(schema) {
			t.Errorf("%s carries the send ceiling; a customer must not be able to "+
				"see that one exists", name)
		}
	}

	// Which named schemas carry it. Collected first because an operation's own
	// body says only "$ref: TenantDetail" — the reference is not inlined, so
	// asking whether the OPERATION mentions the ceiling finds nothing however
	// many ceiling fields the schema it returns has. That is not hypothetical:
	// it is what this test did on its first draft, and it passed while
	// TenantDetail carried the ceiling on a route with no 403.
	carriers := map[string]bool{}
	for name, schema := range spec.Components.Schemas {
		if mentionsCeiling(schema) {
			carriers[name] = true
		}
	}

	// And any operator route that serves or sets it has to be able to say no.
	// A route whose contract declares no 403 cannot refuse a non-admin without
	// breaking that contract — which is how the gate ends up not applied at all.
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			body, err := json.Marshal(op)
			if err != nil {
				continue
			}
			touches := mentionsCeiling(op) || strings.Contains(path, "send-cap")
			for name := range carriers {
				if strings.Contains(string(body), "/"+name+`"`) {
					touches = true
				}
			}
			if !touches {
				continue
			}
			if op.Responses == nil {
				t.Errorf("%s %s touches the send ceiling and declares no responses",
					method, path)
				continue
			}
			if _, declared := op.Responses.Map()["403"]; !declared {
				t.Errorf("%s %s serves or sets the send ceiling but declares no 403, "+
					"so it has no way to refuse a non-admin operator", method, path)
			}
		}
	}
}
