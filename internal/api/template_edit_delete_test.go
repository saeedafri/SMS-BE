package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// seedTemplateOn inserts a template in a chosen status on a chosen sender, so
// each test below starts from the exact state whose rule it is about.
func (h *harness) seedTemplateOn(tenant account, senderID uuid.UUID, channel, status string,
	columns map[string]any) uuid.UUID {

	h.t.Helper()
	names := []string{"tenant_id", "sender_id", "name", "channel", "country", "status"}
	values := []any{tenant.TenantID, senderID,
		fmt.Sprintf("template %d", h.nextSenderSeq()), channel, "IN", status}
	for column, value := range columns {
		names = append(names, column)
		values = append(values, value)
	}
	placeholders := ""
	for i := range names {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += fmt.Sprintf("$%d", i+1)
	}
	var id uuid.UUID
	query := "INSERT INTO templates (" + joinNames(names) + ") VALUES (" +
		placeholders + ") RETURNING id"
	if err := h.admin.QueryRow(context.Background(), query, values...).Scan(&id); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	return id
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}

func (h *harness) seedSender(tenant account, channel string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, $3, 'IN', 'approved') RETURNING id`,
		tenant.TenantID, fmt.Sprintf("EDT%03d", h.nextSenderSeq()), channel).Scan(&id); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	return id
}

func (h *harness) patchTemplate(tenant account, id uuid.UUID, body any) response {
	h.t.Helper()
	return h.do(http.MethodPatch, "/v1/templates/"+id.String(), tenant.Token, body)
}

// A template's name is the platform's own label and its words are what a
// regulator approved. The first is editable in every status; the second stops
// being editable the moment it is approved, because in India the DLT content
// template id is registered against those exact words.
func TestATemplateNameIsEditableInEveryStatusAndItsWordsAreNot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := h.seedSender(acct, "SMS")
	body := map[string]any{"body": "Hi {{first_name}}, your order shipped."}

	inReview := h.seedTemplateOn(acct, sender, "SMS", "pending_review",
		map[string]any{"body": "Hello {{first_name}}"})
	approved := h.seedTemplateOn(acct, sender, "SMS", "approved",
		map[string]any{"body": "Hello {{first_name}}"})

	rename := h.patchTemplate(acct, inReview, map[string]any{"name": "Order shipped v2"})
	if rename.Code != http.StatusOK {
		t.Fatalf("rename in review = %d, want 200\n%s", rename.Code, rename.Body)
	}
	var renamed gen.Template
	rename.decode(t, &renamed)
	if renamed.Name != "Order shipped v2" {
		t.Errorf("name = %q, want the new one", renamed.Name)
	}
	if renamed.Body == nil || *renamed.Body != "Hello {{first_name}}" {
		t.Errorf("a rename changed the body to %v", renamed.Body)
	}

	if res := h.patchTemplate(acct, approved, map[string]any{"name": "Approved, relabelled"}); res.Code != http.StatusOK {
		t.Errorf("rename approved = %d, want 200 — a label is never part of an approval\n%s",
			res.Code, res.Body)
	}
	if res := h.patchTemplate(acct, inReview, body); res.Code != http.StatusOK {
		t.Errorf("edit words in review = %d, want 200\n%s", res.Code, res.Body)
	}
	if res := h.patchTemplate(acct, approved, body); res.Code != http.StatusConflict {
		t.Errorf("edit an approved template's words = %d, want 409\n%s", res.Code, res.Body)
	}

	// Atomic: a name travelling with refused substance is refused with it,
	// rather than the caller half-succeeding.
	both := h.patchTemplate(acct, approved,
		map[string]any{"name": "Renamed anyway", "body": "new words"})
	if both.Code != http.StatusConflict {
		t.Fatalf("name plus substance on approved = %d, want 409\n%s", both.Code, both.Body)
	}
	read := h.do(http.MethodGet, "/v1/templates/"+approved.String(), acct.Token, nil)
	var after gen.Template
	read.decode(t, &after)
	if after.Name == "Renamed anyway" {
		t.Error("the refused request applied its name anyway")
	}

	if res := h.patchTemplate(acct, inReview, map[string]any{"name": "   "}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("blank name = %d, want 422\n%s", res.Code, res.Body)
	}
	if res := h.patchTemplate(acct, inReview, map[string]any{}); res.Code != http.StatusOK {
		t.Errorf("empty patch = %d, want 200 — a form that changed nothing asked for nothing\n%s",
			res.Code, res.Body)
	}
}

// The rejection was a decision about the old copy. Without this the customer
// fixes exactly what the reason told them to fix and the template reads
// rejected for ever, with no way back into the queue.
func TestFixingARejectedTemplateReturnsItToReviewButRenamingDoesNot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := h.seedSender(acct, "SMS")
	seed := map[string]any{"body": "Hello {{first_name}}", "rejection_reason": "Body has a shortener."}

	fixed := h.seedTemplateOn(acct, sender, "SMS", "rejected", seed)
	res := h.patchTemplate(acct, fixed, map[string]any{"body": "Hi {{first_name}}, welcome."})
	if res.Code != http.StatusOK {
		t.Fatalf("fix a rejected template = %d, want 200\n%s", res.Code, res.Body)
	}
	var updated gen.Template
	res.decode(t, &updated)
	if updated.Status != "pending_review" {
		t.Errorf("status after the fix = %q, want pending_review", updated.Status)
	}
	if updated.RejectionReason != nil {
		t.Errorf("rejectionReason after the fix = %v, want null", *updated.RejectionReason)
	}

	renamed := h.seedTemplateOn(acct, sender, "SMS", "rejected", seed)
	res = h.patchTemplate(acct, renamed, map[string]any{"name": "Rejected, relabelled"})
	if res.Code != http.StatusOK {
		t.Fatalf("rename a rejected template = %d, want 200\n%s", res.Code, res.Body)
	}
	res.decode(t, &updated)
	if updated.Status != "rejected" {
		t.Errorf("a rename moved status to %q — it changes nothing that was reviewed", updated.Status)
	}
	if updated.RejectionReason == nil {
		t.Error("a rename cleared the rejection reason")
	}
}

// Every rule here is the channel declaring the field at all. Content belonging
// to a channel that has none of it is refused rather than stored: a silently
// dropped payload costs the customer a template that looks saved and has lost
// the thing they edited.
func TestATemplateEditRefusesWhatItsChannelDoesNotCarry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sms := h.seedTemplateOn(acct, h.seedSender(acct, "SMS"), "SMS", "pending_review",
		map[string]any{"body": "Hello {{first_name}}"})
	rcsCard, _ := json.Marshal(map[string]any{
		"kind": "text", "text": "Hi {{first_name}}", "suggestions": []any{},
	})
	rcs := h.seedTemplateOn(acct, h.seedSender(acct, "RCS"), "RCS", "pending_review",
		map[string]any{"rcs_content": rcsCard})
	whatsapp := h.seedTemplateOn(acct, h.seedSender(acct, "WHATSAPP"), "WHATSAPP",
		"pending_review", map[string]any{"body": "Hello {{first_name}}"})

	for _, row := range []struct {
		name     string
		template uuid.UUID
		body     any
		want     int
	}{
		{"a body on an RCS template", rcs, map[string]any{"body": "words"}, 422},
		{"a category on an RCS template", rcs, map[string]any{"category": "MARKETING"}, 200},
		{"TRANSACTIONAL on an RCS template", rcs,
			map[string]any{"category": "TRANSACTIONAL"}, 422},
		{"rcsContent on an SMS template", sms,
			map[string]any{"rcsContent": map[string]any{"kind": "text", "text": "x",
				"suggestions": []any{}}}, 422},
		{"an empty body", sms, map[string]any{"body": ""}, 422},
		{"a malformed variable token", sms, map[string]any{"body": "Hi {{first_name"}, 422},
		{"a category on an SMS template", sms, map[string]any{"category": "MARKETING"}, 422},
		{"TRANSACTIONAL on a WhatsApp template", whatsapp,
			map[string]any{"category": "TRANSACTIONAL"}, 422},
		{"MARKETING on a WhatsApp template", whatsapp,
			map[string]any{"category": "MARKETING"}, 200},
		{"an unknown dltCategory", sms, map[string]any{"dltCategory": "NOT_A_CATEGORY"}, 422},
		{"a registrationId on a WhatsApp template", whatsapp,
			map[string]any{"registrationId": "1107161234567890123"}, 422},
		{"clearing India's registrationId", sms,
			map[string]any{"registrationId": nil}, 422},
		{"a registrationId as a JSON number", sms,
			json.RawMessage(`{"registrationId":1707161234567890123}`), 422},
		{"repointing the sender", sms,
			map[string]any{"senderId": uuid.NewString()}, 422},
		{"a shortener inside RCS content", rcs,
			map[string]any{"rcsContent": map[string]any{"kind": "text",
				"text": "Track it at https://bit.ly/x", "suggestions": []any{}}}, 422},
	} {
		res := h.patchTemplate(acct, row.template, row.body)
		if res.Code != row.want {
			t.Errorf("%s = %d, want %d\n%s", row.name, res.Code, row.want, res.Body)
		}
	}

	// The sender never moves, whatever the refused request asked for.
	read := h.do(http.MethodGet, "/v1/templates/"+sms.String(), acct.Token, nil)
	var after gen.Template
	read.decode(t, &after)
	if after.Body == nil || *after.Body != "Hello {{first_name}}" {
		t.Errorf("a refused edit changed the body to %v", after.Body)
	}
}

// variables are re-derived from whatever words the template now carries, and
// for rich content that means every string in it — a card's title, its
// description and its suggestion labels, not just one field.
func TestAnEditReDerivesVariablesFromTheWordsItLeavesBehind(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sms := h.seedTemplateOn(acct, h.seedSender(acct, "SMS"), "SMS", "pending_review",
		map[string]any{"body": "Hello {{first_name}}", "variables": []string{"first_name"}})

	res := h.patchTemplate(acct, sms, map[string]any{
		"body": "Hi {{first_name}}, order {{order_id}} ships to {{city}}.",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("edit body = %d, want 200\n%s", res.Code, res.Body)
	}
	var updated gen.Template
	res.decode(t, &updated)
	if len(updated.Variables) != 3 {
		t.Errorf("variables after the edit = %v, want the three the new words carry",
			updated.Variables)
	}

	card, _ := json.Marshal(map[string]any{"kind": "text", "text": "Hi", "suggestions": []any{}})
	rcs := h.seedTemplateOn(acct, h.seedSender(acct, "RCS"), "RCS", "pending_review",
		map[string]any{"rcs_content": card})
	res = h.patchTemplate(acct, rcs, map[string]any{"rcsContent": map[string]any{
		"kind": "card", "title": "Hi {{first_name}}", "description": "Order {{order_id}}",
		"suggestions": []any{map[string]any{"kind": "reply", "label": "Thanks {{city}}",
			"postbackData": "ok"}},
	}})
	if res.Code != http.StatusOK {
		t.Fatalf("edit rcsContent = %d, want 200\n%s", res.Code, res.Body)
	}
	res.decode(t, &updated)
	if len(updated.Variables) != 3 {
		t.Errorf("variables from the card = %v, want one per string the card carries",
			updated.Variables)
	}
}

// What gates a delete is USE, not standing — the opposite of the rule on
// editing. Two of the four references have no foreign key behind them, and a
// Verify service has no template id at all: it holds OTP copy, and it is live
// only while an approved template on the same sender matches those words.
func TestATemplateAnythingStillNeedsCannotBeDeleted(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	sender := h.seedSender(acct, "SMS")
	unused := h.seedTemplateOn(acct, sender, "SMS", "approved",
		map[string]any{"body": "Hello"})
	inCampaign := h.seedTemplateOn(acct, sender, "SMS", "approved",
		map[string]any{"body": "Hello"})
	asFallback := h.seedTemplateOn(acct, sender, "SMS", "approved",
		map[string]any{"body": "Hello"})
	inJourney := h.seedTemplateOn(acct, sender, "SMS", "approved",
		map[string]any{"body": "Hello"})

	ctx := context.Background()
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id)
		VALUES ($1, 'uses a template', 'SMS', 'IN', $2, $3)`,
		acct.TenantID, sender, inCampaign); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id,
		                       fallback_template_id)
		VALUES ($1, 'uses a fallback', 'SMS', 'IN', $2, $3, $4)`,
		acct.TenantID, sender, unused, asFallback); err != nil {
		t.Fatalf("seed fallback campaign: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO journeys (tenant_id, name, trigger_type, steps)
		VALUES ($1, 'uses a template', 'list_entry',
		        jsonb_build_array(jsonb_build_object('type', 'send', 'id', 's1',
		            'channel', 'SMS', 'senderId', $2::text, 'templateId', $3::text)))`,
		acct.TenantID, sender, inJourney); err != nil {
		t.Fatalf("seed journey: %v", err)
	}

	// The Verify service reference: no id anywhere, only copy that an approved
	// template on the same sender matches.
	otpSender := h.approvedSender(acct)
	otpCopy := "{{code}} is your login code. Do not share it."
	otpTemplate := uuid.MustParse(h.registeredTemplate(acct, otpSender, otpCopy))
	service := h.otpService(acct, otpSender)

	for _, row := range []struct {
		name     string
		template uuid.UUID
		want     int
		names    string
	}{
		{"a campaign", inCampaign, http.StatusConflict, "1 campaign"},
		{"a campaign's fallback leg", asFallback, http.StatusConflict, "1 campaign fallback"},
		{"a journey send step", inJourney, http.StatusConflict, "1 journey"},
		{"a verify service's copy", otpTemplate, http.StatusConflict, "1 verify service"},
	} {
		res := h.do(http.MethodDelete, "/v1/templates/"+row.template.String(), acct.Token, nil)
		if res.Code != row.want {
			t.Errorf("delete one %s uses = %d, want %d\n%s", row.name, res.Code, row.want, res.Body)
			continue
		}
		if !strings.Contains(string(res.Body), row.names) {
			t.Errorf("the refusal for %s does not name %q: %s", row.name, row.names, res.Body)
		}
	}

	// The service is still live: a refused delete changes nothing.
	read := h.do(http.MethodGet, "/v1/verify/services/"+service, acct.Token, nil)
	var readService gen.VerifyService
	read.decode(t, &readService)
	if readService.Status != "live" {
		t.Errorf("the service reads %q after a refused delete, want live", readService.Status)
	}

	// An approved template nothing references still goes: use gates this, not
	// standing.
	gone := h.seedTemplateOn(acct, sender, "SMS", "approved", map[string]any{"body": "Hello"})
	if res := h.do(http.MethodDelete, "/v1/templates/"+gone.String(), acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("delete an unused approved template = %d, want 204\n%s", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/templates/"+gone.String(), acct.Token, nil); res.Code != http.StatusNotFound {
		t.Errorf("after the delete, read = %d, want 404", res.Code)
	}
}

// Neither door answers to a stranger, and neither invents a template.
func TestTemplateEditAndDeleteRefuseUnknownIdsAndUnknownCallers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	missing := uuid.New().String()

	for _, row := range []struct {
		method, token string
		want          int
	}{
		{http.MethodPatch, acct.Token, http.StatusNotFound},
		{http.MethodDelete, acct.Token, http.StatusNotFound},
		{http.MethodPatch, "", http.StatusUnauthorized},
		{http.MethodDelete, "", http.StatusUnauthorized},
	} {
		var body any
		if row.method == http.MethodPatch {
			body = map[string]any{"name": "X"}
		}
		res := h.do(row.method, "/v1/templates/"+missing, row.token, body)
		if res.Code != row.want {
			t.Errorf("%s with token=%t = %d, want %d\n%s",
				row.method, row.token != "", res.Code, row.want, res.Body)
		}
	}
}

// A registry id that is still blank can be filled in on an approved template,
// and nothing else about it can.
//
// India's DLT issues a content-template id when it approves the words, which
// can be after the template is approved here — and until it arrives the
// template cannot send at all, because SMPPRouter.Submit refuses an Indian SMS
// with no DLT ids. Under the plain freeze rule that state was permanent.
func TestABlankRegistryIdCanBeFilledInOnAnApprovedTemplateAndNothingElseCan(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := h.seedSender(acct, "SMS")
	approved := func(columns map[string]any) uuid.UUID {
		columns["body"] = "Hello {{first_name}}"
		return h.seedTemplateOn(acct, sender, "SMS", "approved", columns)
	}

	blank := approved(map[string]any{})
	res := h.patchTemplate(acct, blank, map[string]any{"registrationId": "1207161000000000007"})
	if res.Code != http.StatusOK {
		t.Fatalf("filling a blank registry id = %d, want 200\n%s", res.Code, res.Body)
	}
	var updated gen.Template
	res.decode(t, &updated)
	if updated.RegistrationId == nil || *updated.RegistrationId != "1207161000000000007" {
		t.Errorf("registrationId = %v, want the one just supplied", updated.RegistrationId)
	}

	for _, row := range []struct {
		name     string
		template uuid.UUID
		body     any
		want     int
	}{
		// Already set: changing it would point registered words at a different
		// registration, which is the thing the freeze exists for.
		{"replacing an id that is already set", blank,
			map[string]any{"registrationId": "1207161000000000008"}, http.StatusConflict},
		{"clearing it", approved(map[string]any{}),
			map[string]any{"registrationId": nil}, http.StatusUnprocessableEntity},
		{"blanking it", approved(map[string]any{}),
			map[string]any{"registrationId": ""}, http.StatusUnprocessableEntity},
		// The exception is registrationId ALONE. A body travelling beside it is
		// still a body.
		{"words alongside it", approved(map[string]any{}),
			map[string]any{"registrationId": "1207161000000000009", "body": "New words"},
			http.StatusConflict},
		{"words on their own", approved(map[string]any{}),
			map[string]any{"body": "New words"}, http.StatusConflict},
		{"a DLT category", approved(map[string]any{}),
			map[string]any{"dltCategory": "PROMOTIONAL"}, http.StatusConflict},
	} {
		got := h.patchTemplate(acct, row.template, row.body)
		if got.Code != row.want {
			t.Errorf("%s = %d, want %d\n%s", row.name, got.Code, row.want, got.Body)
		}
	}
}

// The refusal is shown verbatim in the edit drawer, and "a approved template"
// is what it used to read — on the status it fires on most.
func TestTheFrozenTemplateRefusalReadsAsEnglish(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := h.seedSender(acct, "SMS")

	for _, status := range []string{"approved", "expired", "blocked"} {
		template := h.seedTemplateOn(acct, sender, "SMS", status,
			map[string]any{"body": "Hello"})
		res := h.patchTemplate(acct, template, map[string]any{"body": "New words"})
		if res.Code != http.StatusConflict {
			t.Fatalf("%s = %d, want 409\n%s", status, res.Code, res.Body)
		}
		for _, wrong := range []string{"a approved", "a expired", "a blocked"} {
			if strings.Contains(string(res.Body), wrong) {
				t.Errorf("the %s refusal reads %q: %s", status, wrong, res.Body)
			}
		}
		if !strings.Contains(string(res.Body), "it is "+status) {
			t.Errorf("the %s refusal does not say which status it is: %s", status, res.Body)
		}
	}
}

// Create used to be looser than edit: it stored a Meta category on a channel
// that declares none, and the PATCH that would have corrected it refused. The
// two answered differently in adjacent lines of one file.
func TestCreateRefusesACategoryTheChannelDoesNotDeclare(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")

	create := func(channel string, body map[string]any) response {
		sender := h.seedSender(acct, channel)
		body["senderId"] = sender.String()
		body["name"] = fmt.Sprintf("created %d", h.nextSenderSeq())
		return h.do(http.MethodPost, "/v1/templates", acct.Token, body)
	}

	for _, row := range []struct {
		name    string
		channel string
		body    map[string]any
		want    int
	}{
		{"a category on an SMS template", "SMS",
			map[string]any{"body": "Hello", "category": "UTILITY"}, 422},
		{"a category on an RCS template", "RCS",
			map[string]any{"rcsContent": map[string]any{"kind": "text", "text": "Hello",
				"suggestions": []any{}}, "category": "UTILITY"}, 201},
		{"TRANSACTIONAL on an RCS template", "RCS",
			map[string]any{"rcsContent": map[string]any{"kind": "text", "text": "Hello",
				"suggestions": []any{}}, "category": "TRANSACTIONAL"}, 422},
		{"TRANSACTIONAL on a WhatsApp template", "WHATSAPP",
			map[string]any{"waContent": map[string]any{"kind": "text", "body": "Hello"},
				"category": "TRANSACTIONAL"}, 422},
		{"MARKETING on a WhatsApp template", "WHATSAPP",
			map[string]any{"waContent": map[string]any{"kind": "text", "body": "Hello"},
				"category": "MARKETING"}, 201},
		{"no category at all on SMS", "SMS", map[string]any{"body": "Hello"}, 201},
	} {
		res := create(row.channel, row.body)
		if res.Code != row.want {
			t.Errorf("%s = %d, want %d\n%s", row.name, res.Code, row.want, res.Body)
			continue
		}
		// The same sentence PATCH answers with. "must be one of: ." is what a
		// channel with no taxonomy produces when the refusal is left to the
		// enum check, and it tells the customer nothing.
		if row.want == 422 && row.channel == "SMS" {
			want := row.channel + " templates carry no category."
			if !strings.Contains(string(res.Body), want) {
				t.Errorf("%s refused with %s, want %q", row.name, res.Body, want)
			}
		}
	}
}
