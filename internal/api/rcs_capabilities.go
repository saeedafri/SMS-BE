package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// CheckRcsCapabilities asks the configured carrier which of these handsets can
// receive RCS.
//
// One endpoint serves both callers because both carriers do: a single number
// returns features, a list returns reachability only. Splitting it in two would
// mean the audience screen and the send path disagreeing about which endpoint
// is authoritative, and neither carrier offers features in bulk anyway.
func (s *Server) CheckRcsCapabilities(ctx context.Context, request gen.CheckRcsCapabilitiesRequestObject) (gen.CheckRcsCapabilitiesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.CheckRcsCapabilities401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if request.Body == nil || len(request.Body.Msisdns) == 0 {
		return gen.CheckRcsCapabilities400JSONResponse(
			errorBody(codeValidation, "Provide at least one msisdn to check")), nil
	}
	// Required, and checked here rather than trusted to the binder. A
	// non-pointer uuid decodes an omitted key as the zero id, so a request that
	// never named an agent arrives looking like one that named uuid.Nil — and
	// nothing in Go forces a handler to notice. Answering for no agent would be
	// the shared-agent answer this field exists to retire.
	if request.Body.RcsAgentId == uuid.Nil {
		return gen.CheckRcsCapabilities400JSONResponse(errorBody(codeValidation,
			"Name the agent to check reach for: rcsAgentId is required, because the "+
				"same handset is reachable for one agent and not another.")), nil
	}
	// Normalise here rather than letting the carrier judge. Airtel refuses an
	// entire list on its first malformed number, so one bad row in a
	// ten-thousand-contact audience would take the whole check down with it.
	// A number we cannot canonicalise is reported unreachable and never sent.
	valid := make([]string, 0, len(request.Body.Msisdns))
	rejected := make([]string, 0)
	for _, raw := range request.Body.Msisdns {
		if normalised, ok := audience.NormaliseE164(raw); ok {
			valid = append(valid, normalised)
			continue
		}
		rejected = append(rejected, raw)
	}

	// Every number malformed is a different situation from some of them: it is
	// almost always a single mistyped number in a try-it box, and answering
	// "not reachable" would send the caller looking for a carrier problem that
	// does not exist.
	if len(valid) == 0 {
		return gen.CheckRcsCapabilities400JSONResponse(errorBody(codeValidation,
			"None of the numbers are valid E.164 — include the country code and a leading +")), nil
	}
	if len(valid) > connector.MaxRCSBulkNumbers {
		return gen.CheckRcsCapabilities400JSONResponse(errorBody(codeValidation,
			"Both carriers cap a capability check at 10,000 numbers; split the list")), nil
	}

	// Before the carrier check, so the answer about the agent does not depend on
	// whether this deployment has credentials. Another tenant's agent reads as
	// one that does not exist: a distinct answer would confirm the id is real.
	if _, err := store.GetRcsAgent(ctx, s.DB, identity, request.Body.RcsAgentId); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.CheckRcsCapabilities404JSONResponse(
				errorBody(codeNotFound, "No such RCS agent.")), nil
		}
		return nil, err
	}
	if s.RCSCarrier == nil {
		return gen.CheckRcsCapabilities503JSONResponse(errorBody(codeValidation,
			"This deployment has no RCS carrier configured, so handset reachability cannot be checked")), nil
	}
	// The carrier's own id for this agent, on the carrier this deployment checks
	// through — under the same three conditions a send uses, from the same query,
	// so "reachable" here and "sendable" at the gate cannot disagree.
	carrier := connector.RCSIntegrations[s.RCSCarrier.Vendor()]
	agentID, err := store.CarrierAgentID(ctx, s.DB, identity, request.Body.RcsAgentId, carrier)
	if errors.Is(err, store.ErrNotFound) {
		// Refused rather than answered with reachableCount 0. A zero reads as a
		// fact about the handsets; the truth is a fact about the agent.
		return gen.CheckRcsCapabilities422JSONResponse(errorBody(codeValidation,
			"This agent has no approved launch on "+carrier+", so it reaches no one "+
				"there. Reach is checked through "+carrier+" on this deployment.")), nil
	}
	if err != nil {
		return nil, err
	}

	report := gen.RcsCapabilityReport{
		Vendor:  gen.RcsVendor(s.RCSCarrier.Vendor()),
		Results: make([]gen.RcsCapability, 0, len(valid)+len(rejected)),
	}

	if len(valid) == 1 {
		capability, err := s.RCSCarrier.Capability(ctx, agentID, valid[0])
		if err != nil {
			return rcsCarrierError(err)
		}
		// An empty features array is a real answer — reachable, nothing rich
		// supported — and must not be collapsed into null, which means "this
		// kind of check does not return features at all".
		features := capability.Features
		if features == nil {
			features = []string{}
		}
		report.FeaturesIncluded = true
		report.Results = append(report.Results, gen.RcsCapability{
			Msisdn:    capability.Msisdn,
			Reachable: capability.Reachable,
			Features:  &features,
		})
	} else {
		reachable, err := s.RCSCarrier.Reachable(ctx, agentID, valid)
		if err != nil {
			return rcsCarrierError(err)
		}
		reachableSet := make(map[string]struct{}, len(reachable))
		for _, msisdn := range reachable {
			reachableSet[msisdn] = struct{}{}
		}
		for _, msisdn := range valid {
			_, ok := reachableSet[msisdn]
			report.Results = append(report.Results,
				gen.RcsCapability{Msisdn: msisdn, Reachable: ok})
		}
	}

	// The numbers we refused travel back in the results so a caller reconciling
	// a list against the answer finds every row it submitted, rather than
	// silently losing the malformed ones.
	for _, raw := range rejected {
		report.Results = append(report.Results, gen.RcsCapability{Msisdn: raw, Reachable: false})
	}

	report.CheckedCount = len(valid)
	for _, result := range report.Results {
		if result.Reachable {
			report.ReachableCount++
		}
	}
	return gen.CheckRcsCapabilities200JSONResponse(report), nil
}

// rcsCarrierError keeps a carrier's own words out of the response. Airtel's
// failures quote a Google URL carrying the agent id, and Vi's quote the bot id;
// neither belongs in a tenant-facing body. The detail is already in the
// request log.
func rcsCarrierError(err error) (gen.CheckRcsCapabilitiesResponseObject, error) {
	if errors.Is(err, connector.ErrRCSNotConfigured) || errors.Is(err, connector.ErrRCSNoAgent) {
		return gen.CheckRcsCapabilities503JSONResponse(errorBody(codeValidation,
			"This deployment has no RCS carrier configured, so handset reachability cannot be checked")), nil
	}
	if errors.Is(err, connector.ErrRCSTooManyNumbers) {
		return gen.CheckRcsCapabilities400JSONResponse(errorBody(codeValidation,
			"Both carriers cap a capability check at 10,000 numbers; split the list")), nil
	}
	return gen.CheckRcsCapabilities502JSONResponse(errorBody(codeValidation,
		"The RCS carrier could not be reached. Try again shortly.")), nil
}
