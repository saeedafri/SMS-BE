package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// An agent's lifecycle refuses what it should, in the states it should.
//
// The transitions are the product: an agent under carrier review must not
// change underneath the carrier reviewing it, a brand claim cannot be submitted
// twice, and an unverified agent cannot be put to a carrier. Each of those is a
// 409 rather than a quiet no-op, because a silent refusal is indistinguishable
// from success on the screen that asked for it.
func TestAnRcsAgentRefusesWhatItsStateDoesNotAllow(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")

	agent := h.createAgent(acct, "Acme Retail")
	if agent.Status != "draft" {
		t.Fatalf("a new agent is %q, want draft", agent.Status)
	}

	// Carrier launch is refused before verification: an agent nobody has
	// checked must not reach a carrier under a customer's brand.
	launch := h.do(http.MethodPost, "/v1/rcs/agents/"+agent.Id+"/launch", acct.Token,
		map[string]any{"carrier": "AIRTEL"})
	if launch.Code != http.StatusConflict {
		t.Errorf("launching an unverified agent = %d, want 409 (%s)", launch.Code, launch.Body)
	}

	// A draft is editable.
	patch := h.do(http.MethodPatch, "/v1/rcs/agents/"+agent.Id, acct.Token,
		map[string]any{"displayName": "Acme Retail Orders"})
	if patch.Code != http.StatusOK {
		t.Fatalf("editing a draft = %d: %s", patch.Code, patch.Body)
	}

	document := h.uploadAsset(acct, "verification_document", "letter.pdf",
		"application/pdf", []byte("%PDF-1.4\nletter of authorisation\n"))
	submitted := h.submitVerification(acct, agent.Id, document)
	if submitted.Status != "verification_submitted" {
		t.Fatalf("after submitting, status is %q", submitted.Status)
	}

	// Now under review, and closed to edits.
	patch = h.do(http.MethodPatch, "/v1/rcs/agents/"+agent.Id, acct.Token,
		map[string]any{"displayName": "Something Else"})
	if patch.Code != http.StatusConflict {
		t.Errorf("editing an agent under review = %d, want 409 — a carrier must not "+
			"have the thing it is reviewing change underneath it (%s)", patch.Code, patch.Body)
	}
	// And closed to a second submission, which would otherwise overwrite the
	// reviewer's queue row with a fresh one.
	again := h.do(http.MethodPost, "/v1/rcs/agents/"+agent.Id+"/verification", acct.Token,
		map[string]any{"contactName": "A", "contactEmail": "a@acme.test",
			"contactPhone": "+919876500001", "documentAssetId": document})
	if again.Code != http.StatusConflict {
		t.Errorf("submitting twice = %d, want 409", again.Code)
	}
}

// One customer must never read another's agent, and must not be able to tell it
// apart from one that does not exist.
//
// A 403 would confirm the id is real, which turns the id space into an
// enumeration oracle for who the customers are. That is why the contract says
// 404 and why this asserts the code rather than merely "not 200".
func TestAnotherTenantsAgentIsIndistinguishableFromNothing(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	h.approveRegistration(owner, "IN")

	agent := h.createAgent(owner, "Private Brand")

	real := h.do(http.MethodGet, "/v1/rcs/agents/"+agent.Id, other.Token, nil)
	invented := h.do(http.MethodGet, "/v1/rcs/agents/"+uuid.NewString(), other.Token, nil)

	if real.Code != http.StatusNotFound {
		t.Errorf("another tenant's agent = %d, want 404", real.Code)
	}
	if string(real.Body) != string(invented.Body) || real.Code != invented.Code {
		t.Errorf("a real agent and an invented id answer differently:\n  real     %d %s\n"+
			"  invented %d %s\nthe id space is an oracle for who the customers are",
			real.Code, real.Body, invented.Code, invented.Body)
	}

	// And it is absent from the list, both halves of the envelope.
	list := h.do(http.MethodGet, "/v1/rcs/agents", other.Token, nil)
	var page struct {
		Agents []struct{ Id string } `json:"agents"`
		Total  int                   `json:"total"`
	}
	if err := json.Unmarshal(list.Body, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Agents) != 0 || page.Total != 0 {
		t.Errorf("another tenant sees %d agents and a total of %d, want 0 and 0",
			len(page.Agents), page.Total)
	}
}

// A carrier this deployment cannot reach is SHOWN, at not_submitted, rather
// than omitted.
//
// A customer whose reach looks short should be able to see which network is
// missing. Omitting it makes "we have no integration" indistinguishable from
// "that network does not exist", and sends them to look for a coverage gap that
// is not there.
func TestAnUnreachableCarrierIsListedRatherThanHidden(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")

	agent := h.createAgent(acct, "Reach Test")
	if len(agent.CarrierLaunches) == 0 {
		t.Fatal("a new agent lists no carriers at all — a customer cannot see " +
			"which network is missing")
	}
	for _, launch := range agent.CarrierLaunches {
		if launch.Status != "not_submitted" {
			t.Errorf("%s starts at %q, want not_submitted", launch.Carrier, launch.Status)
		}
		if launch.CarrierAgentId != nil {
			t.Errorf("%s has a carrier id before anything was submitted", launch.Carrier)
		}
	}
}

type agentBody struct {
	Id              string `json:"id"`
	Status          string `json:"status"`
	DisplayName     string `json:"displayName"`
	CarrierLaunches []struct {
		Carrier        string  `json:"carrier"`
		Status         string  `json:"status"`
		CarrierAgentId *string `json:"carrierAgentId"`
	} `json:"carrierLaunches"`
	Verification struct {
		Status          string  `json:"status"`
		DocumentAssetId *string `json:"documentAssetId"`
	} `json:"verification"`
}

func (h *harness) createAgent(acct account, name string) agentBody {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/rcs/agents", acct.Token, map[string]any{
		"displayName": name, "country": "IN", "useCase": "TRANSACTIONAL",
	})
	if res.Code != http.StatusCreated {
		h.t.Fatalf("create agent = %d: %s", res.Code, res.Body)
	}
	var agent agentBody
	if err := json.Unmarshal(res.Body, &agent); err != nil {
		h.t.Fatalf("decode agent: %v", err)
	}
	return agent
}

func (h *harness) submitVerification(acct account, agentID, documentID string) agentBody {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/rcs/agents/"+agentID+"/verification", acct.Token,
		map[string]any{"contactName": "Ada Brand", "contactEmail": "ada@acme.test",
			"contactPhone": "+919876500001", "documentAssetId": documentID})
	if res.Code != http.StatusOK {
		h.t.Fatalf("submit verification = %d: %s", res.Code, res.Body)
	}
	var agent agentBody
	if err := json.Unmarshal(res.Body, &agent); err != nil {
		h.t.Fatalf("decode agent: %v", err)
	}
	return agent
}

// approveRegistration gives the tenant the approved business entity an agent
// hangs its legal identity off.
func (h *harness) approveRegistration(acct account, country string) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO registrations (tenant_id, country, object_key, status)
		VALUES ($1, $2, $3, 'approved')`,
		acct.TenantID, country, "entity-"+uuid.NewString()[:8]); err != nil {
		h.t.Fatalf("approve registration: %v", err)
	}
}

// uploadAsset posts one file and returns its id.
func (h *harness) uploadAsset(acct account, purpose, filename, contentType string,
	body []byte) string {

	h.t.Helper()
	res := h.uploadAttempt(acct, purpose, filename, contentType, body)
	if res.Code != http.StatusCreated {
		h.t.Fatalf("upload %s = %d: %s", purpose, res.Code, res.Body)
	}
	var asset struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(res.Body, &asset); err != nil {
		h.t.Fatalf("decode asset: %v", err)
	}
	return asset.Id
}

func (h *harness) uploadAttempt(acct account, purpose, filename, contentType string,
	body []byte) response {

	h.t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	_ = form.WriteField("purpose", purpose)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("build form: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		h.t.Fatalf("write form: %v", err)
	}
	_ = form.Close()
	return h.doRaw(http.MethodPost, "/v1/media", acct.Token,
		form.FormDataContentType(), buf.Bytes())
}

// pngOf builds a real PNG of the given size, so dimension checks are exercised
// against a file rather than against a claim.
func pngOf(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

var _ = fmt.Sprintf

// The server reads image dimensions OFF THE FILE.
//
// This is the one thing the frontend cannot test for us and said so: their mock
// runs where there is no image decoder, so it validates the width and height
// the CLIENT measured and sent. A client-supplied dimension is a claim, not a
// measurement — a caller that posts a 4000-pixel photograph while declaring
// 224x224 passes any check written against what it said, and the refusal then
// lands at the carrier days later on a submission nobody can see the reason for.
//
// Every case here posts a REAL PNG, so the only thing that can satisfy the
// assertion is a decode.
func TestUploadedImagesAreMeasuredNotDescribed(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, c := range []struct {
		name          string
		purpose       string
		width, height int
		wantCode      int
		says          string
	}{
		{"a logo at the exact size Airtel demands", "agent_logo", 224, 224, http.StatusCreated, ""},
		{"a logo one pixel out", "agent_logo", 224, 225, http.StatusUnprocessableEntity, "224 x 224"},
		{"a photograph sent as a logo", "agent_logo", 1000, 1000, http.StatusUnprocessableEntity, "1000 x 1000"},
		{"a hero at the exact size", "agent_hero", 1440, 448, http.StatusCreated, ""},
		{"a hero at Vi's card size, not Airtel's banner size", "agent_hero", 1440, 480,
			http.StatusUnprocessableEntity, "1440 x 448"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := h.uploadAttempt(acct, c.purpose, "art.png", "image/png", pngOf(c.width, c.height))
			if res.Code != c.wantCode {
				t.Fatalf("= %d, want %d: %s", res.Code, c.wantCode, res.Body)
			}
			if c.says == "" {
				return
			}
			// The refusal has to name both numbers. The customer has to go and
			// re-export the file: "invalid image" costs them a support ticket,
			// "1000 x 1000, needs exactly 224 x 224" costs them ninety seconds.
			body := string(res.Body)
			if !bytes.Contains(res.Body, []byte(c.says)) {
				t.Errorf("the refusal does not say %q, so the customer cannot act on it: %s",
					c.says, body)
			}
		})
	}

	// And the content type is sniffed rather than believed, for the same
	// reason one field over: a declared type is a claim too.
	res := h.uploadAttempt(acct, "agent_logo", "logo.png", "image/png",
		[]byte("MZ\x90\x00 this is not a png"))
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("a file calling itself image/png = %d, want 422", res.Code)
	}
}

// The real carrier limits are enforced, and they are far tighter than the
// frontend's provisional guesses.
//
// Airtel p6 states 50 KB for a logo and 200 KB for a hero; the provisional
// numbers were 2 MB and 5 MB — generous by 40x and 25x. A customer would export
// a 1.8 MB logo, upload it happily, and have the agent submission refused days
// later with no way to connect the two events.
func TestTheCarrierSizeLimitsAreTheOnesEnforced(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	// A 224x224 PNG of noise, comfortably over 50 KB but far under the 2 MB the
	// frontend's provisional table would have allowed.
	big := image.NewRGBA(image.Rect(0, 0, 224, 224))
	for x := 0; x < 224; x++ {
		for y := 0; y < 224; y++ {
			big.Set(x, y, color.RGBA{R: uint8(x * y), G: uint8(x ^ y), B: uint8(x + y), A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, big)
	if buf.Len() <= 50*1024 {
		t.Skipf("the noise image compressed to %d bytes, under the 50 KB limit — "+
			"this fixture no longer straddles it", buf.Len())
	}

	res := h.uploadAttempt(acct, "agent_logo", "logo.png", "image/png", buf.Bytes())
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a %d-byte logo = %d, want 413 — Airtel's limit is 50 KB and the "+
			"provisional number was 2 MB", buf.Len(), res.Code)
	}
	if !bytes.Contains(res.Body, []byte("50 KB")) {
		t.Errorf("the refusal does not name the limit: %s", res.Body)
	}
}

// The operator can see and decide an agent that is waiting for them.
//
// This is the guard for a bug the full suite did not catch: rcs_agents shipped
// with a tenant-isolation policy and no operator policy, so the approval queue
// read nothing and approve answered 404 on an agent that plainly existed. Every
// customer-side test passed throughout — they all run as the tenant, which is
// exactly the role the missing policy did not affect.
//
// Row-level security was doing what it was told. What it was told was half the
// story, and only a test that crosses the boundary can say so.
func TestAnAgentAwaitingReviewReachesTheOperatorAndCanBeDecided(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	h.approveRegistration(acct, "IN")

	agent := h.createAgent(acct, "Queue Probe "+uuid.NewString()[:6])
	document := h.uploadAsset(acct, "verification_document", "loa.pdf",
		"application/pdf", []byte("%PDF-1.4\nletter\n"))
	h.submitVerification(acct, agent.Id, document)

	queue := h.do(http.MethodGet, "/v1/operator/approvals?type=rcs_agent&limit=50", operator, nil)
	if queue.Code != http.StatusOK {
		t.Fatalf("queue = %d: %s", queue.Code, queue.Body)
	}
	var page struct {
		Items []struct {
			Id              string  `json:"id"`
			ItemType        string  `json:"itemType"`
			ContactEmail    *string `json:"contactEmail"`
			DocumentAssetId *string `json:"documentAssetId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(queue.Body, &page); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	var row *struct {
		Id              string  `json:"id"`
		ItemType        string  `json:"itemType"`
		ContactEmail    *string `json:"contactEmail"`
		DocumentAssetId *string `json:"documentAssetId"`
	}
	for i := range page.Items {
		if page.Items[i].Id == agent.Id {
			row = &page.Items[i]
		}
	}
	if row == nil {
		t.Fatalf("the agent is not in the operator queue — it cannot be approved, "+
			"and the customer waits forever (%d rows)", len(page.Items))
	}
	if row.ItemType != "rcs_agent" {
		t.Errorf("itemType = %q, want rcs_agent", row.ItemType)
	}
	// The evidence the operator is being asked to judge. Without it the dialog
	// says "approve this?" and shows nothing to approve against.
	if row.ContactEmail == nil || row.DocumentAssetId == nil {
		t.Errorf("the queue row carries no contact or document to judge")
	}

	// A rejection with no reason is refused: a rejection the customer cannot
	// read is a dead end.
	blank := h.do(http.MethodPost, "/v1/operator/rcs-agents/"+agent.Id+"/reject",
		operator, map[string]any{"reason": "   "})
	if blank.Code != http.StatusUnprocessableEntity {
		t.Errorf("rejecting with a blank reason = %d, want 422", blank.Code)
	}

	approved := h.do(http.MethodPost, "/v1/operator/rcs-agents/"+agent.Id+"/approve", operator, nil)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", approved.Code, approved.Body)
	}
	// Approving the VERIFICATION does not make the agent live. It still reaches
	// nobody until a carrier admits it, and collapsing the two would let both
	// the operator and the customer believe otherwise.
	var after agentBody
	if err := json.Unmarshal(approved.Body, &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Status != "verification_approved" {
		t.Errorf("after approval the agent is %q, want verification_approved — "+
			"a verified agent is not a live one", after.Status)
	}
	// And a second decision on a decided agent is refused rather than silently
	// re-approving it.
	if again := h.do(http.MethodPost, "/v1/operator/rcs-agents/"+agent.Id+"/approve",
		operator, nil); again.Code != http.StatusConflict {
		t.Errorf("approving twice = %d, want 409", again.Code)
	}
}
