package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/saeedafri/sms-be/internal/connector"
)

// stubCarrier stands in for Airtel or Vi. The carriers' own wire behaviour is
// covered against fake HTTP servers in internal/connector; what matters here is
// the shape of the answer the endpoint builds from it, and that a caller cannot
// tell the two carriers apart except by the vendor field.
type stubCarrier struct {
	vendor    string
	reachable map[string]bool
	features  []string
	err       error

	singleCalls int
	bulkCalls   int
	sawNumbers  []string
}

func (c *stubCarrier) Vendor() string { return c.vendor }

func (c *stubCarrier) Capability(_ context.Context, msisdn string) (connector.RCSCapability, error) {
	c.singleCalls++
	c.sawNumbers = append(c.sawNumbers, msisdn)
	if c.err != nil {
		return connector.RCSCapability{}, c.err
	}
	return connector.RCSCapability{
		Msisdn:    msisdn,
		Reachable: c.reachable[msisdn],
		Features:  c.features,
		Vendor:    c.vendor,
	}, nil
}

func (c *stubCarrier) Reachable(_ context.Context, msisdns []string) ([]string, error) {
	c.bulkCalls++
	c.sawNumbers = append(c.sawNumbers, msisdns...)
	if c.err != nil {
		return nil, c.err
	}
	out := make([]string, 0, len(msisdns))
	for _, m := range msisdns {
		if c.reachable[m] {
			out = append(out, m)
		}
	}
	return out, nil
}

type capabilityReport struct {
	Vendor           string `json:"vendor"`
	CheckedCount     int    `json:"checkedCount"`
	ReachableCount   int    `json:"reachableCount"`
	FeaturesIncluded bool   `json:"featuresIncluded"`
	Results          []struct {
		Msisdn    string    `json:"msisdn"`
		Reachable bool      `json:"reachable"`
		Features  *[]string `json:"features"`
	} `json:"results"`
}

func TestASingleNumberCheckReturnsTheFeaturesTheHandsetSupports(t *testing.T) {
	h := newHarness(t)
	h.server.RCSCarrier = &stubCarrier{
		vendor:    "airtel",
		reachable: map[string]bool{"+919820000001": true},
		features:  []string{"RICHCARD_STANDALONE", "ACTION_DIAL"},
	}
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"+91 98200 00001"}})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body)
	}

	var report capabilityReport
	res.decode(t, &report)
	if report.Vendor != "airtel" {
		t.Errorf("vendor = %q, want the carrier that answered", report.Vendor)
	}
	if !report.FeaturesIncluded {
		t.Error("featuresIncluded = false on a single-number check")
	}
	if report.CheckedCount != 1 || report.ReachableCount != 1 {
		t.Errorf("counts = %d checked / %d reachable, want 1 / 1",
			report.CheckedCount, report.ReachableCount)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %d rows, want 1", len(report.Results))
	}
	// Normalised before it reached the carrier: the caller sent spaces.
	if report.Results[0].Msisdn != "+919820000001" {
		t.Errorf("msisdn = %q, want the canonical E.164 form", report.Results[0].Msisdn)
	}
	if report.Results[0].Features == nil || len(*report.Results[0].Features) != 2 {
		t.Errorf("features = %v, want the two the carrier listed", report.Results[0].Features)
	}
}

// The distinction the contract turns on: null features means "this kind of
// check does not answer that", an empty array means "reachable, nothing rich".
func TestAReachableHandsetWithNoRichFeaturesIsNotTheSameAsABulkAnswer(t *testing.T) {
	h := newHarness(t)
	h.server.RCSCarrier = &stubCarrier{
		vendor:    "vi",
		reachable: map[string]bool{"+914253136789": true},
		features:  nil,
	}
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"+914253136789"}})
	var report capabilityReport
	res.decode(t, &report)

	if report.Results[0].Features == nil {
		t.Fatal("features = null on a single check; want an empty array, which is a real answer")
	}
	if len(*report.Results[0].Features) != 0 {
		t.Errorf("features = %v, want empty", *report.Results[0].Features)
	}
}

func TestAMultiNumberCheckUsesTheBulkPathAndOmitsFeatures(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{
		vendor: "vi",
		reachable: map[string]bool{
			"+919686960876": true,
			"+919687895543": false,
			"+919688757768": true,
		},
		features: []string{"RICHCARD_STANDALONE"},
	}
	h.server.RCSCarrier = carrier
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token, map[string]any{
		"msisdns": []string{"+919687895543", "+919686960876", "+919688757768"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body)
	}

	var report capabilityReport
	res.decode(t, &report)
	if carrier.bulkCalls != 1 || carrier.singleCalls != 0 {
		t.Errorf("carrier saw %d bulk / %d single calls, want 1 / 0",
			carrier.bulkCalls, carrier.singleCalls)
	}
	if report.FeaturesIncluded {
		t.Error("featuresIncluded = true on a bulk check; no carrier returns them")
	}
	if report.ReachableCount != 2 {
		t.Errorf("reachableCount = %d, want 2", report.ReachableCount)
	}
	for _, row := range report.Results {
		if row.Features != nil {
			t.Errorf("%s carried features on a bulk check: %v", row.Msisdn, *row.Features)
		}
	}
}

// One mistyped row must not cost the other 9,999 their answer — Airtel refuses
// a whole list on its first malformed number, so the filtering happens here.
func TestOneMalformedNumberDoesNotFailTheWholeList(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{
		vendor:    "airtel",
		reachable: map[string]bool{"+919820000001": true, "+919820000002": true},
	}
	h.server.RCSCarrier = carrier
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token, map[string]any{
		"msisdns": []string{"+919820000001", "not-a-number", "+919820000002"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body)
	}

	var report capabilityReport
	res.decode(t, &report)
	if report.CheckedCount != 2 {
		t.Errorf("checkedCount = %d, want 2 — only what the carrier was asked", report.CheckedCount)
	}
	if len(report.Results) != 3 {
		t.Errorf("results = %d rows, want 3 — the rejected row comes back too", len(report.Results))
	}
	for _, number := range carrier.sawNumbers {
		if number == "not-a-number" {
			t.Error("a number that failed normalisation was sent to the carrier")
		}
	}
	var sawRejected bool
	for _, row := range report.Results {
		if row.Msisdn == "not-a-number" {
			sawRejected = true
			if row.Reachable {
				t.Error("a malformed number was reported reachable")
			}
		}
	}
	if !sawRejected {
		t.Error("the malformed row was dropped from the answer instead of returned unreachable")
	}
}

// A single mistyped number is a typo, not a carrier verdict.
func TestANumberThatIsEntirelyMalformedIsRejectedRatherThanCalledUnreachable(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{vendor: "airtel"}
	h.server.RCSCarrier = carrier
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"98200"}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", res.Code, res.Body)
	}
	if carrier.singleCalls+carrier.bulkCalls != 0 {
		t.Error("an unusable number was still sent to the carrier")
	}
}

func TestADeploymentWithNoRCSCarrierSaysSoInsteadOfReportingEveryoneUnreachable(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"+919820000001"}})
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", res.Code, res.Body)
	}
}

// A carrier failure must not reach the tenant verbatim: Airtel's errors quote a
// Google URL carrying the agent id, Vi's quote the bot id.
func TestACarrierFailureIsABadGatewayWithoutLeakingTheAgentIdentity(t *testing.T) {
	h := newHarness(t)
	h.server.RCSCarrier = &stubCarrier{
		vendor: "airtel",
		err: errCarrier("Failed to fetch capabilities: 401 GET https://asia-rcsbusinessmessaging" +
			".googleapis.com/v1/phones/+91/capabilities?agentId=relay_prod_agent_7f3a"),
	}
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"+919820000001"}})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", res.Code, res.Body)
	}
	for _, secret := range []string{"relay_prod_agent_7f3a", "googleapis.com", "agentId"} {
		if contains(string(res.Body), secret) {
			t.Errorf("the response body leaked %q: %s", secret, res.Body)
		}
	}
}

func TestCapabilityCheckNeedsABearerToken(t *testing.T) {
	h := newHarness(t)
	h.server.RCSCarrier = &stubCarrier{vendor: "airtel"}

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", "",
		map[string]any{"msisdns": []string{"+919820000001"}})
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", res.Code, res.Body)
	}
}

func TestAnEmptyListIsRejected(t *testing.T) {
	h := newHarness(t)
	h.server.RCSCarrier = &stubCarrier{vendor: "airtel"}
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", res.Code, res.Body)
	}
}

type errCarrier string

func (e errCarrier) Error() string { return string(e) }
