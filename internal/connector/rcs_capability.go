package connector

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// RCS capability discovery: can this handset receive RCS at all, and which
// rich features will render on it.
//
// It is the question that decides RCS-versus-SMS fallback, so it belongs on
// the send path rather than on a screen — a campaign that sends RCS to a
// handset which cannot display it produces neither a message nor an error,
// just silence and a bill.
//
// The two Indian gateways we integrate — Airtel IQ and Vi RBM — disagree about
// almost everything (Airtel authenticates with a static Basic credential, Vi
// with an OAuth token that is itself rate limited), but they agree about this
// one vocabulary, and that is what makes a single normalised answer honest.
//
// Vi publishes capability discovery TWICE, and the choice between them matters:
//
//	§2.3 {serverRoot}/bot/v1/{botId}/contactCapabilities
//	     GSMA MaaP vocabulary — chat, fileTransfer, videoCall, geolocationPush,
//	     callComposer, chatBotCommunication. These describe the handset's IMS
//	     services. They cannot tell you whether a rich card will render.
//
//	§3.5 {serverRoot}/rcs/v1/phones/{msisdn}/capabilities?botId=…
//	     Google RBM vocabulary — RICHCARD_STANDALONE, ACTION_DIAL, and the rest.
//	     Character for character the list Airtel returns.
//
// We use §3.5. Choosing §2.3 would mean inventing a mapping from "fileTransfer"
// to "can this show a carousel", which is a guess dressed up as an answer, and
// it would make the same Relay field mean two different things depending on
// which carrier a tenant happened to route through.
//
// Because both vendors speak Google's vocabulary, Features passes through
// unmapped. There is no translation table here on purpose: a name Relay does
// not recognise is far more useful reaching the caller intact than being
// silently dropped by a filter written before the vendor added it.

// Feature names seen from one or both vendors. Declared for documentation and
// for the UI's sake, NOT used to filter — see above.
const (
	RCSRichCardStandalone = "RICHCARD_STANDALONE"
	RCSRichCardCarousel   = "RICHCARD_CAROUSEL"
	RCSActionDial         = "ACTION_DIAL"
	RCSActionOpenURL      = "ACTION_OPEN_URL"
	RCSActionShareLoc     = "ACTION_SHARE_LOCATION"
	RCSActionViewLoc      = "ACTION_VIEW_LOCATION"
	RCSActionCalendar     = "ACTION_CREATE_CALENDAR_EVENT"

	// Airtel only.
	RCSPDFInRichCards   = "PDF_IN_RICH_CARDS"
	RCSActionURLWebview = "ACTION_OPEN_URL_IN_WEBVIEW"

	// Vi only — Vi can recall a sent message, Airtel cannot.
	RCSRevocation = "REVOCATION"
)

// RCSCapability is one handset's answer.
//
// Reachable and Features are deliberately separate. A handset can be reachable
// with an empty feature set (RCS enabled, nothing rich supported), and both
// vendors express "not reachable" differently enough that collapsing the two
// into "len(Features) > 0" would be wrong for one of them: Vi returns 200 with
// an empty object, Airtel returns an error carrying a wrapped Google 404.
type RCSCapability struct {
	Msisdn    string
	Reachable bool
	Features  []string
	Vendor    string
}

// Supports reports whether a named feature is present. Case sensitive, because
// both vendors emit these names in upper snake case and a case-insensitive
// match would quietly accept a typo.
func (c RCSCapability) Supports(feature string) bool {
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// RCSCapabilityChecker is one carrier's capability API.
//
// The agent identity (Airtel's agentId, Vi's botId) is a PARAMETER, not
// configuration held by the implementation. It used to be the latter, and the
// comment here used to defend that: a caller supplying it could supply the
// wrong one. That reasoning held while one agent served the whole deployment
// and is wrong now that customers own their agents — an identity held by the
// connector makes the brand a property of the process, so every tenant's
// handsets see the same name whoever sent the message.
//
// Reachability is genuinely agent-specific, which is what makes this more than
// bookkeeping: a handset with RCS enabled is unreachable for an agent that has
// not launched on that subscriber's carrier. The same list of numbers gives
// different answers for different agents.
type RCSCapabilityChecker interface {
	Vendor() string

	// Capability answers for one handset, including which features it supports.
	Capability(ctx context.Context, agentID, msisdn string) (RCSCapability, error)

	// Reachable answers for many, and ONLY reachability — neither vendor's bulk
	// endpoint returns features. It returns the subset of msisdns that can
	// receive RCS, in the order they were given.
	Reachable(ctx context.Context, agentID string, msisdns []string) ([]string, error)
}

var (
	// ErrRCSNotConfigured is what a deployment without carrier credentials
	// gets. It is a distinct error rather than a nil checker so the endpoint
	// can say "this deployment has no RCS carrier" instead of panicking or,
	// worse, reporting every handset as unreachable.
	ErrRCSNotConfigured = errors.New("connector: no RCS carrier configured")

	// ErrRCSThrottled is the carrier saying slow down — Airtel allows 40 TPS
	// per customer id by default, Vi throttles per account and separately caps
	// its token endpoint at 60 requests a minute. It is distinct from a
	// transport failure because the response is different: a throttled send
	// should be retried after a pause, a failed one usually should not.
	ErrRCSThrottled = errors.New("connector: rcs carrier is throttling this account")

	// ErrRCSTooManyNumbers guards both vendors' 10,000 ceiling before the
	// request leaves this process. Discovering it from a vendor 400 costs a
	// round trip and returns a message written for their support desk.
	ErrRCSTooManyNumbers = errors.New("connector: more than 10000 numbers in one capability check")

	// ErrRCSNoAgent is a carrier call with no agent identity to make it under.
	//
	// Distinct from ErrRCSNotConfigured because the two have different causes
	// and different fixes: not-configured is a deployment without carrier
	// credentials, and this is a deployment with credentials but a sender whose
	// agent has not launched on the carrier it routed to. Collapsing them would
	// tell a customer to go and find an operator about their own agent.
	ErrRCSNoAgent = errors.New("connector: no RCS agent identity for this send")
)

// MaxRCSBulkNumbers is the ceiling both vendors document.
const MaxRCSBulkNumbers = 10000

// dedupe preserves first-seen order. Airtel rejects a list containing
// duplicates ("must contain between 500 and 10000 unique numbers"), and
// counting the same handset twice would overstate an audience's reach anyway.
func dedupe(msisdns []string) []string {
	seen := make(map[string]struct{}, len(msisdns))
	unique := make([]string, 0, len(msisdns))
	for _, m := range msisdns {
		if m == "" {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		unique = append(unique, m)
	}
	return unique
}

// checkEach answers a bulk question by asking one at a time, bounded so a
// large list cannot open a connection per number. It exists for Airtel, whose
// bulk endpoint refuses lists under 500 — see rcs_airtel.go.
//
// A single failing number does not fail the batch. One unreachable handset in
// a 400-contact audience is an ordinary result, and returning an error for the
// whole list would turn it into a screen that shows nothing.
func checkEach(ctx context.Context, check func(context.Context, string) (RCSCapability, error), msisdns []string) ([]string, error) {
	const workers = 8

	type answer struct {
		index     int
		reachable bool
	}

	jobs := make(chan int)
	answers := make(chan answer, len(msisdns))

	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(msisdns); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				capability, err := check(ctx, msisdns[index])
				answers <- answer{index: index, reachable: err == nil && capability.Reachable}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i := range msisdns {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
	close(answers)

	indices := make([]int, 0, len(msisdns))
	for a := range answers {
		if a.reachable {
			indices = append(indices, a.index)
		}
	}
	// Workers finish out of order; the caller asked for input order.
	sort.Ints(indices)

	reachable := make([]string, 0, len(indices))
	for _, i := range indices {
		reachable = append(reachable, msisdns[i])
	}
	if err := ctx.Err(); err != nil {
		return reachable, err
	}
	return reachable, nil
}

// Compile-time proof that both carriers satisfy every seam they are used
// through. Without these, a missing method surfaces at the call site in another
// package as an unrelated-looking type error.
var (
	_ Connector            = (*AirtelRCS)(nil)
	_ Connector            = (*ViRCS)(nil)
	_ RCSCapabilityChecker = (*AirtelRCS)(nil)
	_ RCSCapabilityChecker = (*ViRCS)(nil)
	_ RCSTemplateRegistrar = (*AirtelRCS)(nil)
	_ RCSTemplateRegistrar = (*ViRCS)(nil)
)
