package connector

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// RCSGateway is one RCS operator: it sends and it answers reachability.
type RCSGateway interface {
	Connector
	RCSCapabilityChecker
}

// RCSRouter sends each RCS message through the operator the send path chose
// for it, from the accounts configured in the database.
//
// Submission.Carrier names the operator. The send path picks it from the
// operators the sender's agent is launched on, in route priority, so the router
// only dispatches; a message naming an operator with no active account is
// refused NO_RCS_CONNECTION rather than sent somewhere else.
type RCSRouter struct {
	mu       sync.RWMutex
	gateways map[string]RCSGateway // by operator name, upper case
	versions map[string]string
}

func (r *RCSRouter) Name() string { return "rcs" }

// Empty reports that no operator is configured, which makes the registry treat
// RCS as having no gateway of its own.
func (r *RCSRouter) Empty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.gateways) == 0
}

// For returns the operator's gateway.
func (r *RCSRouter) For(carrier string) (RCSGateway, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	gateway, ok := r.gateways[strings.ToUpper(carrier)]
	return gateway, ok
}

// Carriers lists the configured operators, sorted.
func (r *RCSRouter) Carriers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	carriers := make([]string, 0, len(r.gateways))
	for carrier := range r.gateways {
		carriers = append(carriers, carrier)
	}
	sort.Strings(carriers)
	return carriers
}

func (r *RCSRouter) Health(ctx context.Context) Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.gateways) == 0 {
		return Health{Detail: "rcs: no operator accounts"}
	}
	health := Health{Healthy: true}
	details := make([]string, 0, len(r.gateways))
	for _, gateway := range r.gateways {
		h := gateway.Health(ctx)
		health.Healthy = health.Healthy && h.Healthy
		details = append(details, h.Detail)
	}
	sort.Strings(details)
	health.Detail = strings.Join(details, "; ")
	return health
}

func (r *RCSRouter) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	grouped := map[RCSGateway][]Submission{}
	var receipts []Receipt
	for _, s := range submissions {
		gateway, ok := r.For(s.Carrier)
		if !ok {
			receipts = append(receipts, Receipt{MessageID: s.MessageID, ErrorCode: "NO_RCS_CONNECTION"})
			continue
		}
		grouped[gateway] = append(grouped[gateway], s)
	}
	for gateway, batch := range grouped {
		got, err := gateway.Submit(ctx, batch)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, got...)
	}
	return receipts, nil
}

// RCSAccount is one account the router should hold: Version changes whenever
// its configuration does, and Build makes its gateway.
type RCSAccount struct {
	Carrier string
	Version string
	Build   func() (RCSGateway, error)
}

// Sync makes the router hold exactly these accounts. An account whose version
// is unchanged keeps its gateway, and with it the tokens that gateway has
// cached; the rest are built again. It returns the build error of each
// operator whose account could not be built, which is then left out.
func (r *RCSRouter) Sync(accounts []RCSAccount) map[string]error {
	r.mu.RLock()
	current, versions := r.gateways, r.versions
	r.mu.RUnlock()

	next := map[string]RCSGateway{}
	nextVersions := map[string]string{}
	failures := map[string]error{}
	for _, account := range accounts {
		carrier := strings.ToUpper(account.Carrier)
		if live, ok := current[carrier]; ok && versions[carrier] == account.Version {
			next[carrier], nextVersions[carrier] = live, account.Version
			continue
		}
		gateway, err := account.Build()
		if err != nil {
			failures[carrier] = err
			continue
		}
		next[carrier], nextVersions[carrier] = gateway, account.Version
	}
	r.mu.Lock()
	r.gateways, r.versions = next, nextVersions
	r.mu.Unlock()
	return failures
}

var _ RCSGateway = (*JioRCS)(nil)
