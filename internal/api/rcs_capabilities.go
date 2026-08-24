package api

import (
	"context"
	"errors"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"

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
	if _, ok := identityFrom(ctx); !ok {
		return gen.CheckRcsCapabilities401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if request.Body == nil || len(request.Body.Msisdns) == 0 {
		return gen.CheckRcsCapabilities400JSONResponse(
			errorBody(codeValidation, "Provide at least one msisdn to check")), nil
	}
	if s.RCSCarrier == nil {
		return gen.CheckRcsCapabilities503JSONResponse(errorBody(codeValidation,
			"This deployment has no RCS carrier configured, so handset reachability cannot be checked")), nil
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

	report := gen.RcsCapabilityReport{
		Vendor:  gen.RcsCapabilityReportVendor(s.RCSCarrier.Vendor()),
		Results: make([]gen.RcsCapability, 0, len(valid)+len(rejected)),
	}

	if len(valid) == 1 {
		capability, err := s.RCSCarrier.Capability(ctx, valid[0])
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
		reachable, err := s.RCSCarrier.Reachable(ctx, valid)
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
	if errors.Is(err, connector.ErrRCSNotConfigured) {
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
