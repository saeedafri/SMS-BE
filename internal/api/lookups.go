package api

import (
	"context"
	"strings"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// Number lookup: what kind of line a number is. The body is judged first and
// availability second, so a malformed request learns that before it learns
// this deployment has no provider.

func lookupUnavailable() gen.Error {
	return errorBody("lookup_unavailable", "This deployment has no number lookup provider.")
}

func (s *Server) lookupOne(ctx context.Context, identity store.Identity, msisdn string,
	fields []string) (gen.LookupResult, error) {

	result := gen.LookupResult{Msisdn: msisdn, LineType: "unknown", CheckedAt: s.now().UTC()}
	if connector.IsE164(msisdn) {
		info, err := s.Lookup.Lookup(ctx, msisdn, fields)
		if err != nil {
			return result, err
		}
		result.LineType = gen.LookupResultLineType(info.LineType)
		result.Carrier, result.Mcc, result.Mnc = info.Carrier, info.MCC, info.MNC
		result.Ported, result.Reachable = info.Ported, info.Reachable
		if err := store.SaveLookup(ctx, s.DB, identity, msisdn, result, result.CheckedAt); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Server) LookupNumber(ctx context.Context, request gen.LookupNumberRequestObject) (
	gen.LookupNumberResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.LookupNumber403JSONResponse(errorBody(codeForbidden,
			"Your role cannot look up numbers.")), nil
	}
	msisdn := ""
	if request.Body != nil {
		msisdn = strings.TrimSpace(request.Body.Msisdn)
	}
	if !connector.IsE164(msisdn) {
		return gen.LookupNumber422JSONResponse(errorBody(codeValidation,
			"msisdn must be a phone number in international format, like +919876543210.")), nil
	}
	var fields []string
	if request.Body.Fields != nil {
		fields = []string{}
		for _, field := range *request.Body.Fields {
			if !field.Valid() {
				return gen.LookupNumber422JSONResponse(errorBody(codeValidation,
					"fields must list carrier, ported or reachable.")), nil
			}
			fields = append(fields, string(field))
		}
	}
	if s.Lookup == nil {
		return gen.LookupNumber409JSONResponse(lookupUnavailable()), nil
	}
	result, err := s.lookupOne(ctx, identity, msisdn, fields)
	if err != nil {
		return nil, err
	}
	return gen.LookupNumber200JSONResponse(result), nil
}

func (s *Server) LookupNumbers(ctx context.Context, request gen.LookupNumbersRequestObject) (
	gen.LookupNumbersResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.LookupNumbers403JSONResponse(errorBody(codeForbidden,
			"Your role cannot look up numbers.")), nil
	}
	if request.Body == nil || len(request.Body.Msisdns) < 1 || len(request.Body.Msisdns) > 100 {
		return gen.LookupNumbers422JSONResponse(errorBody(codeValidation,
			"msisdns must list between 1 and 100 numbers.")), nil
	}
	if s.Lookup == nil {
		return gen.LookupNumbers409JSONResponse(lookupUnavailable()), nil
	}
	// One row per number asked, in order. A number that does not parse is
	// "unknown" for that row and does not fail the call.
	results := make([]gen.LookupResult, 0, len(request.Body.Msisdns))
	for _, raw := range request.Body.Msisdns {
		result, err := s.lookupOne(ctx, identity, strings.TrimSpace(raw), nil)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return gen.LookupNumbers200JSONResponse{Results: results}, nil
}
