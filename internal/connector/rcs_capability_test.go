package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// These exercise both carriers against fake HTTP servers rather than the real
// gateways. A test that reached Airtel or Vi would need a commercial
// credential, would bill per call, and would send capability lookups for
// invented numbers to a live carrier.

func airtelStub(t *testing.T, handler http.HandlerFunc) *AirtelRCS {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &AirtelRCS{
		BaseURL:   server.URL,
		AuthToken: "dGVzdDp0ZXN0",
		AgentID:   "relay_test_agent",
		HTTP:      server.Client(),
	}
}

func TestAirtelReturnsTheFeaturesAHandsetSupports(t *testing.T) {
	airtel := airtelStub(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Basic dGVzdDp0ZXN0" {
			t.Errorf("Authorization = %q, want the token verbatim after Basic", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/rcs/capabilities") {
			t.Errorf("path = %q, want the single-check endpoint", r.URL.Path)
		}

		// Airtel documents this as a GET carrying a JSON body. If that ever
		// stops arriving, every capability answer silently becomes an
		// agentId validation error, so assert the body actually made it.
		var body struct {
			PhoneNumber string `json:"phoneNumber"`
			AgentID     string `json:"agentId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the GET body: %v", err)
		}
		if body.PhoneNumber != "+919820000001" || body.AgentID != "relay_test_agent" {
			t.Errorf("body = %+v, want the number and the configured agent", body)
		}

		fmt.Fprint(w, `{"success":true,"code":200,"message":"success",
			"features":["RICHCARD_STANDALONE","ACTION_DIAL","PDF_IN_RICH_CARDS"]}`)
	})

	capability, err := airtel.Capability(context.Background(), "+919820000001")
	if err != nil {
		t.Fatalf("Capability: %v", err)
	}
	if !capability.Reachable {
		t.Error("Reachable = false, want true for a handset that answered with features")
	}
	if !capability.Supports(RCSRichCardStandalone) || !capability.Supports(RCSPDFInRichCards) {
		t.Errorf("Features = %v, want the vendor's list passed through unmapped", capability.Features)
	}
	if capability.Supports(RCSRichCardCarousel) {
		t.Error("Supports reported a carousel the carrier did not list")
	}
	if capability.Vendor != "airtel" {
		t.Errorf("Vendor = %q, want airtel", capability.Vendor)
	}
}

// The case that decides whether this is usable at all: Airtel reports an
// unreachable handset as a FAILED envelope wrapping a Google 404, so a naive
// reading turns every non-RCS subscriber into an outage.
func TestAirtelUnreachableHandsetIsAnAnswerNotAnError(t *testing.T) {
	airtel := airtelStub(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"code":400,"message":"Failed to fetch capabilities: 404 Not Found\nGET https://asia-rcsbusinessmessaging.googleapis.com/v1/phones/+917388000000/capabilities \"status\" : \"NOT_FOUND\""}`)
	})

	capability, err := airtel.Capability(context.Background(), "+917388000000")
	if err != nil {
		t.Fatalf("Capability: %v — an unreachable handset must not be an error", err)
	}
	if capability.Reachable {
		t.Error("Reachable = true for a handset Airtel could not reach")
	}
	if len(capability.Features) != 0 {
		t.Errorf("Features = %v, want none", capability.Features)
	}
}

func TestAirtelValidationFailureIsStillAnError(t *testing.T) {
	airtel := airtelStub(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"code":400,"message":"Validation Error - Invalid Phone Number: 888XXXX"}`)
	})

	if _, err := airtel.Capability(context.Background(), "888"); err == nil {
		t.Fatal("a malformed number was reported as a successful check")
	}
}

func TestAirtelBulkCheckUsesTheBulkEndpointAtOrAboveItsFloor(t *testing.T) {
	var hits int32
	airtel := airtelStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if !strings.HasSuffix(r.URL.Path, "/users/reachability") {
			t.Errorf("path = %q, want the bulk endpoint", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Users []string `json:"users"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decoding bulk body: %v", err)
		}
		if len(request.Users) != AirtelBulkMinimum {
			t.Errorf("sent %d users, want %d", len(request.Users), AirtelBulkMinimum)
		}
		fmt.Fprint(w, `{"success":true,"code":200,"message":"success","reachableUsers":["+91982000000","+91982000005"]}`)
	})

	numbers := make([]string, AirtelBulkMinimum)
	for i := range numbers {
		numbers[i] = fmt.Sprintf("+9198200%05d", i)
	}

	reachable, err := airtel.Reachable(context.Background(), numbers)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(reachable) != 2 {
		t.Errorf("reachable = %v, want the two the carrier named", reachable)
	}
	if hits != 1 {
		t.Errorf("made %d requests, want exactly 1 bulk call", hits)
	}
}

// Under 500 the bulk endpoint does not merely perform badly — it refuses the
// request outright. Falling back to single checks is the only way to answer.
func TestAirtelSmallListFallsBackToSingleChecksInInputOrder(t *testing.T) {
	airtel := airtelStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "reachability") {
			t.Error("a list below the floor was sent to the bulk endpoint, which would 400")
		}
		var body struct {
			PhoneNumber string `json:"phoneNumber"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		// Every number ending in an odd digit is unreachable.
		last := body.PhoneNumber[len(body.PhoneNumber)-1]
		if (last-'0')%2 == 1 {
			fmt.Fprint(w, `{"success":false,"code":400,"message":"Failed to fetch capabilities: 404 Not Found NOT_FOUND"}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"code":200,"message":"success","features":["ACTION_DIAL"]}`)
	})

	numbers := []string{"+910000000004", "+910000000001", "+910000000002",
		"+910000000003", "+910000000000"}

	reachable, err := airtel.Reachable(context.Background(), numbers)
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	want := []string{"+910000000004", "+910000000002", "+910000000000"}
	if strings.Join(reachable, ",") != strings.Join(want, ",") {
		t.Errorf("reachable = %v, want %v — answers must come back in the order asked", reachable, want)
	}
}

func TestBulkCheckRefusesMoreThanTenThousandBeforeCallingTheCarrier(t *testing.T) {
	airtel := airtelStub(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an over-ceiling list reached the carrier instead of being refused here")
	})

	numbers := make([]string, MaxRCSBulkNumbers+1)
	for i := range numbers {
		numbers[i] = fmt.Sprintf("+9199%09d", i)
	}
	if _, err := airtel.Reachable(context.Background(), numbers); !errors.Is(err, ErrRCSTooManyNumbers) {
		t.Fatalf("err = %v, want ErrRCSTooManyNumbers", err)
	}
}

func TestAnUnconfiguredCarrierSaysSoRatherThanReportingEveryoneUnreachable(t *testing.T) {
	airtel := &AirtelRCS{}
	if _, err := airtel.Capability(context.Background(), "+919820000001"); !errors.Is(err, ErrRCSNotConfigured) {
		t.Errorf("airtel err = %v, want ErrRCSNotConfigured", err)
	}
	vi := &ViRCS{}
	if _, err := vi.Reachable(context.Background(), []string{"+919820000001"}); !errors.Is(err, ErrRCSNotConfigured) {
		t.Errorf("vi err = %v, want ErrRCSNotConfigured", err)
	}
}

// --- Vi ---

func viStub(t *testing.T, tokens *int32, handler http.HandlerFunc) *ViRCS {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/oauth/token") {
			atomic.AddInt32(tokens, 1)
			if r.Header.Get("Authorization") != "Basic Y2lkOnNlY3JldA==" {
				t.Errorf("token call Authorization = %q, want Basic base64(clientId:secret)",
					r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("grant_type") != "client_credentials" {
				t.Errorf("grant_type = %q, want client_credentials", r.URL.Query().Get("grant_type"))
			}
			fmt.Fprint(w, `{"access_token":"tok-1","expires_in":3600}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization = %q, want the minted bearer token", got)
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &ViRCS{
		BaseURL:      server.URL,
		TokenURL:     server.URL + "/auth/oauth/token",
		ClientID:     "cid",
		ClientSecret: "secret",
		BotID:        "OsQ0GwNvUdLTV9Bd",
		HTTP:         server.Client(),
	}
}

func TestViReturnsTheSameFeatureVocabularyAirtelDoes(t *testing.T) {
	var tokens int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, r *http.Request) {
		// The Google-style path (§3.5), not the GSMA one (§2.3) — that choice
		// is what keeps one Relay answer meaning one thing across carriers.
		if !strings.HasPrefix(r.URL.Path, "/rcs/v1/phones/") ||
			!strings.HasSuffix(r.URL.Path, "/capabilities") {
			t.Errorf("path = %q, want the Google-style capability endpoint", r.URL.Path)
		}
		if r.URL.Query().Get("botId") != "OsQ0GwNvUdLTV9Bd" {
			t.Errorf("botId = %q, want the configured bot", r.URL.Query().Get("botId"))
		}
		fmt.Fprint(w, `{"features":["REVOCATION","RICHCARD_STANDALONE","ACTION_DIAL"]}`)
	})

	capability, err := vi.Capability(context.Background(), "+914253136789")
	if err != nil {
		t.Fatalf("Capability: %v", err)
	}
	if !capability.Reachable {
		t.Error("Reachable = false for a handset that listed features")
	}
	if !capability.Supports(RCSRichCardStandalone) {
		t.Errorf("Features = %v, want Google RBM names identical to Airtel's", capability.Features)
	}
	if !capability.Supports(RCSRevocation) {
		t.Error("Vi's revocation capability was dropped; a filter would have hidden it")
	}
	if capability.Vendor != "vi" {
		t.Errorf("Vendor = %q, want vi", capability.Vendor)
	}
}

func TestViEmptyObjectMeansTheHandsetHasNoRCS(t *testing.T) {
	var tokens int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{}`)
	})

	capability, err := vi.Capability(context.Background(), "+914253136700")
	if err != nil {
		t.Fatalf("Capability: %v — Vi answers this with 200, not an error", err)
	}
	if capability.Reachable {
		t.Error("Reachable = true for an empty capability object")
	}
}

func TestViBulkCheckServesSmallListsDirectly(t *testing.T) {
	var tokens int32
	var hits int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/rcsEnabledContacts") {
			t.Errorf("%s %s, want POST to the bulk endpoint", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"rcsEnabledContacts":["+919686960876"]}`)
	})

	// Three numbers: below Airtel's floor, and Vi has no floor at all, so this
	// must stay a single bulk call rather than fanning out.
	reachable, err := vi.Reachable(context.Background(),
		[]string{"+919687895543", "+919686960876", "+919688757768"})
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	if len(reachable) != 1 || reachable[0] != "+919686960876" {
		t.Errorf("reachable = %v, want the one contact Vi named", reachable)
	}
	if hits != 1 {
		t.Errorf("made %d bulk requests, want 1 — Vi has no minimum to work around", hits)
	}
}

// Vi's token endpoint allows 60 requests a minute per client. Minting one per
// capability check would spend that budget on a single audience screen.
func TestViMintsOneTokenAndReusesIt(t *testing.T) {
	var tokens int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"features":["ACTION_DIAL"]}`)
	})

	for i := 0; i < 5; i++ {
		if _, err := vi.Capability(context.Background(), "+91425313678"+fmt.Sprint(i)); err != nil {
			t.Fatalf("Capability %d: %v", i, err)
		}
	}
	if tokens != 1 {
		t.Errorf("minted %d tokens for 5 checks, want 1", tokens)
	}
}

func TestViDropsARejectedTokenSoTheNextCallMintsAFreshOne(t *testing.T) {
	var tokens int32
	var calls int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, _ *http.Request) {
		// Reject the first capability call the way an expired token would be
		// rejected, then behave.
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"features":["ACTION_DIAL"]}`)
	})

	if _, err := vi.Capability(context.Background(), "+914253136789"); err == nil {
		t.Fatal("a 401 was reported as a successful check")
	}
	if _, err := vi.Capability(context.Background(), "+914253136789"); err != nil {
		t.Fatalf("second Capability: %v — a stale token must not be permanent", err)
	}
	if tokens != 2 {
		t.Errorf("minted %d tokens, want 2 — the rejected one should have been dropped", tokens)
	}
}

func TestDuplicateNumbersAreCollapsedBeforeTheCarrierSeesThem(t *testing.T) {
	var tokens int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Users []string `json:"users"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		// Airtel counts duplicates against its unique-number rule and would
		// reject the list; both vendors would otherwise bill the same handset
		// twice and overstate an audience's reach.
		if len(body.Users) != 2 {
			t.Errorf("sent %v, want the two distinct numbers", body.Users)
		}
		fmt.Fprint(w, `{"rcsEnabledContacts":[]}`)
	})

	if _, err := vi.Reachable(context.Background(),
		[]string{"+911111111111", "+912222222222", "+911111111111", ""}); err != nil {
		t.Fatalf("Reachable: %v", err)
	}
}
