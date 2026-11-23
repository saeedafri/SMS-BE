package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/failures"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// Failure reasons: why a message did not arrive, in words, with who owned the
// cause and what to do. Resolved at read time from the failure_reasons table.

// failureRules is the dictionary for resolution. A dictionary that cannot be
// read leaves every failure unmapped rather than failing the page: the raw
// code is still shown.
func (s *Server) failureRules(ctx context.Context) []failures.Rule {
	rules, err := store.CachedFailureRules(ctx, s.DB, s.Hot)
	if err != nil && s.Logger != nil {
		s.Logger.Warn("failure dictionary unreadable; failures shown unmapped", "error", err)
	}
	return rules
}

// carriedChannel is the channel that actually carried a message: the fallback
// leg's when it went that way.
func carriedChannel(record store.MessageRecord) string {
	if record.DeliveredChannel != nil && *record.DeliveredChannel != "" {
		return *record.DeliveredChannel
	}
	return record.Channel
}

// failureContext is the question a stored message asks the dictionary.
func failureContext(record store.MessageRecord) (failures.Context, bool) {
	code, route, carrier := "", "", record.Carrier
	if record.ErrorCode != nil {
		code = *record.ErrorCode
	}
	if record.RouteID != nil {
		route = *record.RouteID
	}
	return failures.ContextFor(carriedChannel(record), record.Status, code, route, carrier)
}

// messageFailure explains a message that did not arrive, or is nil for one that
// has not failed. A failed message is never charged.
func messageFailure(rules []failures.Rule, record store.MessageRecord) *gen.MessageFailure {
	question, failed := failureContext(record)
	if !failed {
		return nil
	}
	answer := failures.Resolve(rules, question)
	out := &gen.MessageFailure{Code: answer.Code, Title: answer.Title, Detail: answer.Detail,
		Fix: answer.Fix, Retryable: answer.Retryable, Charged: false,
		Source: gen.FailureSource(answer.Source)}
	if !answer.Unmapped() {
		layer := gen.FailureLayer(answer.Layer)
		out.Layer = &layer
		autoRetried := answer.AutoRetried
		out.AutoRetried = &autoRetried
	}
	if answer.FixKind != nil {
		out.FixAction = &gen.FailureFixAction{Kind: gen.FailureFixActionKind(*answer.FixKind)}
	}
	// A one-time code's receipt can carry the code itself; its raw text is
	// never shown.
	if answer.ProviderCode != "" && !record.OTP {
		code := answer.ProviderCode
		out.ProviderCode = &code
	}
	return out
}

// messageTimeline is the message's path in order, from the times the record
// actually holds. Each step's elapsedMs is from the step before it.
func messageTimeline(record store.MessageRecord) []gen.MessageTimelineStep {
	type step struct {
		stage  gen.MessageTimelineStepStage
		name   string
		at     time.Time
		failed bool
	}
	var steps []step
	add := func(stage gen.MessageTimelineStepStage, name string, at *time.Time, failed bool) {
		if at != nil && !at.IsZero() {
			steps = append(steps, step{stage, name, *at, failed})
		}
	}
	created := record.CreatedAt
	switch record.Status {
	case "rejected":
		add(gen.MessageTimelineStepStagePlatform, "refused", &created, true)
	default:
		add(gen.MessageTimelineStepStagePlatform, "accepted", &created, false)
		add(gen.MessageTimelineStepStageRoute, "submitted", record.SentAt, false)
		if record.CarrierRef != nil {
			add(gen.MessageTimelineStepStageCarrier, "accepted", record.SentAt, false)
		}
		updated := record.UpdatedAt
		switch record.Status {
		case "carrier_rejected":
			add(gen.MessageTimelineStepStageCarrier, "rejected", &updated, true)
		case "undelivered", "expired":
			at := record.DLRReceivedAt
			if at == nil {
				at = &updated
			}
			add(gen.MessageTimelineStepStageCarrier, "failed", at, true)
		}
		add(gen.MessageTimelineStepStageHandset, "delivered", record.DeliveredAt, false)
		add(gen.MessageTimelineStepStageHandset, "read", record.ReadAt, false)
	}
	out := make([]gen.MessageTimelineStep, 0, len(steps))
	var previous *time.Time
	for _, s := range steps {
		at := s.at
		if previous != nil && at.Before(*previous) {
			at = *previous
		}
		entry := gen.MessageTimelineStep{Stage: s.stage, Step: s.name, At: at}
		if previous != nil {
			elapsed := int(at.Sub(*previous).Milliseconds())
			entry.ElapsedMs = &elapsed
		}
		if s.failed {
			failed := true
			entry.Failed = &failed
		}
		out = append(out, entry)
		previous = &at
	}
	return out
}

// messageEncoding is the SMS character set the text needed. Nil for a channel
// with no such concept, and when no text was kept to judge.
func messageEncoding(record store.MessageRecord) *gen.MessageLogEntryEncoding {
	if carriedChannel(record) != "SMS" || record.RenderedText == nil {
		return nil
	}
	encoding := gen.MessageLogEntryEncoding("unicode")
	if billing.IsGSM7(*record.RenderedText) {
		encoding = "gsm"
	}
	return &encoding
}

// withFailure adds the dictionary's answer, the timeline and the encoding to a
// log row.
func withFailure(entry gen.MessageLogEntry, rules []failures.Rule, record store.MessageRecord) gen.MessageLogEntry {
	entry.Failure = messageFailure(rules, record)
	timeline := messageTimeline(record)
	entry.Timeline = &timeline
	entry.Encoding = messageEncoding(record)
	return entry
}

// failureQuestions are this tenant's distinct failures whose answer lies in
// one layer, for the log's layer filter. Never nil: no match is an empty page.
func (s *Server) failureQuestions(ctx context.Context, identity store.Identity,
	layer gen.FailureLayer) ([]store.FailedCode, error) {

	conn, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := store.FailedCodes(ctx, conn, &identity.TenantID)
	if err != nil {
		return nil, err
	}
	rules := s.failureRules(ctx)
	matching := []store.FailedCode{}
	for _, code := range codes {
		question, failed := failures.ContextFor(code.Channel, code.Status, code.ErrorCode,
			code.RouteID, code.Carrier)
		if failed && failures.Resolve(rules, question).Layer == string(layer) {
			matching = append(matching, code)
		}
	}
	return matching, nil
}

// ---- the dictionary, for operators ---------------------------------------------

func dictionaryRow(rule store.FailureRule) gen.FailureReason {
	out := gen.FailureReason{Id: rule.ID, Source: gen.FailureSource(rule.Source),
		RouteId: rule.RouteID, Vendor: rule.Vendor,
		Match: gen.FailureReasonMatch{Kind: gen.FailureReasonMatchKind(rule.MatchKind),
			Value: rule.MatchValue},
		Code: rule.Code, Layer: gen.FailureLayer(rule.Layer), Title: rule.Title,
		Detail: rule.Detail, Fix: rule.Fix, Retryable: rule.Retryable,
		CustomerVisible: rule.CustomerVisible, Version: rule.Version,
		UpdatedBy: rule.UpdatedBy, UpdatedAt: rule.UpdatedAt}
	if rule.Channel != nil {
		channel := gen.ChannelId(*rule.Channel)
		out.Channel = &channel
	}
	if rule.FixKind != nil {
		out.FixAction = &gen.FailureFixAction{Kind: gen.FailureFixActionKind(*rule.FixKind)}
	}
	return out
}

func (s *Server) GetFailureReasons(ctx context.Context, request gen.GetFailureReasonsRequestObject) (
	gen.GetFailureReasonsResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetFailureReasons401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	params := request.Params
	if problem := failureFilterProblem(params.Channel, params.Source); problem != "" {
		return gen.GetFailureReasons422JSONResponse(errorBody(codeValidation, problem)), nil
	}
	page, limit, problem := dictionaryPage(params.Page, params.Limit)
	if problem != "" {
		return gen.GetFailureReasons422JSONResponse(errorBody(codeValidation, problem)), nil
	}
	rules, err := store.FailureRules(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	q := ""
	if params.Q != nil {
		q = strings.ToLower(strings.TrimSpace(*params.Q))
	}
	var matched []gen.FailureReason
	for _, rule := range rules {
		if params.Channel != nil && (rule.Channel == nil || *rule.Channel != string(*params.Channel)) {
			continue
		}
		if params.Source != nil && rule.Source != string(*params.Source) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(rule.Code), q) &&
			!strings.Contains(strings.ToLower(rule.Title), q) &&
			!strings.Contains(strings.ToLower(rule.MatchValue), q) {
			continue
		}
		matched = append(matched, dictionaryRow(rule))
	}
	items := pageOf(matched, page, limit)
	return gen.GetFailureReasons200JSONResponse{Items: items, Total: len(matched)}, nil
}

func (s *Server) GetUnmappedFailureCodes(ctx context.Context, request gen.GetUnmappedFailureCodesRequestObject) (
	gen.GetUnmappedFailureCodesResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetUnmappedFailureCodes401JSONResponse(errorBody(codeUnauthenticated,
			"Sign in to the operator console.")), nil
	}
	params := request.Params
	if problem := failureFilterProblem(params.Channel, params.Source); problem != "" {
		return gen.GetUnmappedFailureCodes422JSONResponse(errorBody(codeValidation, problem)), nil
	}
	page, limit, problem := dictionaryPage(params.Page, params.Limit)
	if problem != "" {
		return gen.GetUnmappedFailureCodes422JSONResponse(errorBody(codeValidation, problem)), nil
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := store.FailedCodes(ctx, conn, nil)
	if err != nil {
		return nil, s.clickhouseFailed(err)
	}
	rules := s.failureRules(ctx)
	// A code leaves this queue the moment a rule resolves it, and not before:
	// the queue is the log's failures that the dictionary cannot answer.
	type key struct{ channel, source, code, route string }
	merged := map[key]*gen.UnmappedFailureCode{}
	for _, code := range codes {
		question, failed := failures.ContextFor(code.Channel, code.Status, code.ErrorCode,
			code.RouteID, code.Carrier)
		if !failed || !failures.Resolve(rules, question).Unmapped() {
			continue
		}
		if params.Channel != nil && question.Channel != string(*params.Channel) {
			continue
		}
		if params.Source != nil && question.Source != string(*params.Source) {
			continue
		}
		k := key{question.Channel, question.Source, question.Raw, question.RouteID}
		row, seen := merged[k]
		if !seen {
			row = &gen.UnmappedFailureCode{Channel: gen.ChannelId(question.Channel),
				Source: gen.FailureSource(question.Source), ProviderCode: question.Raw,
				FirstSeen: code.FirstSeen, LastSeen: code.LastSeen}
			if question.RouteID != "" {
				route := question.RouteID
				row.RouteId = &route
			}
			merged[k] = row
		}
		row.Count += int(code.Count)
		if code.FirstSeen.Before(row.FirstSeen) {
			row.FirstSeen = code.FirstSeen
		}
		if code.LastSeen.After(row.LastSeen) {
			row.LastSeen = code.LastSeen
		}
	}
	all := make([]gen.UnmappedFailureCode, 0, len(merged))
	for _, row := range merged {
		all = append(all, *row)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].ProviderCode < all[j].ProviderCode
	})
	return gen.GetUnmappedFailureCodes200JSONResponse{Items: pageOf(all, page, limit), Total: len(all)}, nil
}

func failureFilterProblem(channel *gen.ChannelId, source *gen.FailureSource) string {
	if channel != nil && !channel.Valid() {
		return "channel must be a known channel."
	}
	if source != nil && !source.Valid() {
		return "source must be one of: " + strings.Join(failureSources, ", ") + "."
	}
	return ""
}

var (
	failureSources = []string{"gate", "smpp_submit", "smpp_receipt", "rcs_vendor", "whatsapp", "email", "voice"}
	failureLayers  = []string{"ours", "regulator", "sender_setup", "carrier", "handset", "provider"}
	fixKinds       = []string{"compliance", "senders", "templates", "wallet", "audience",
		"suppressions", "limits", "support"}
)

// dictionaryPage reads page and limit, limit defaulting to 100 as the contract says.
func dictionaryPage(pageParam, limitParam *int) (int, int, string) {
	page, ok := pageNumber(pageParam)
	if !ok {
		return 0, 0, pageTooLow
	}
	limit := 100
	if limitParam != nil {
		if *limitParam < 1 || *limitParam > 200 {
			return 0, 0, limitOutOfRange
		}
		limit = *limitParam
	}
	return page, limit, ""
}

func pageOf[T any](all []T, page, limit int) []T {
	start := (page - 1) * limit
	if start >= len(all) {
		return []T{}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end]
}

func (s *Server) mountFailureReasonRoutes(r chi.Router) {
	r.Post("/v1/operator/failure-reasons", s.createFailureReason)
	r.Patch("/v1/operator/failure-reasons/{id}", s.updateFailureReason)
}

// editableRuleFields are the fields a PATCH may carry; a create carries these
// and the question the rule answers.
var editableRuleFields = map[string]bool{"layer": true, "title": true, "detail": true,
	"fix": true, "fixAction": true, "retryable": true, "customerVisible": true}

var questionRuleFields = map[string]bool{"channel": true, "source": true, "routeId": true,
	"vendor": true, "match": true, "code": true}

// readRuleBody reads a JSON object and refuses a key outside allowed.
func readRuleBody(r *http.Request, allowed ...map[string]bool) (map[string]json.RawMessage, string) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "The request body could not be read."
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, "The body must be a JSON object."
	}
	var unknown []string
	for key := range body {
		known := false
		for _, set := range allowed {
			known = known || set[key]
		}
		if !known {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Sprintf("Unknown field(s): %s.", strings.Join(unknown, ", "))
	}
	return body, ""
}

func isNull(value json.RawMessage) bool { return strings.TrimSpace(string(value)) == "null" }

// nullableText reads text or null; blank text is null.
func nullableText(value json.RawMessage, field string) (*string, string) {
	if isNull(value) {
		return nil, ""
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return nil, field + " must be text or null."
	}
	if text = strings.TrimSpace(text); text == "" {
		return nil, ""
	}
	return &text, ""
}

func oneOfText(value json.RawMessage, list []string) (string, bool) {
	var text string
	if json.Unmarshal(value, &text) != nil {
		return "", false
	}
	for _, item := range list {
		if item == text {
			return text, true
		}
	}
	return "", false
}

// rulePatch validates every editable field present before any is written.
func rulePatch(body map[string]json.RawMessage) (store.FailureRulePatch, string) {
	var patch store.FailureRulePatch
	if value, present := body["layer"]; present {
		layer, ok := oneOfText(value, failureLayers)
		if !ok {
			return patch, "layer must be one of: " + strings.Join(failureLayers, ", ") + "."
		}
		patch.Layer = &layer
	}
	if value, present := body["title"]; present {
		var title string
		if json.Unmarshal(value, &title) != nil || strings.TrimSpace(title) == "" {
			return patch, "title is required and cannot be blank."
		}
		title = strings.TrimSpace(title)
		patch.Title = &title
	}
	for _, field := range []string{"detail", "fix"} {
		value, present := body[field]
		if !present {
			continue
		}
		text, problem := nullableText(value, field)
		if problem != "" {
			return patch, problem
		}
		if field == "detail" {
			patch.Detail, patch.DetailSet = text, true
		} else {
			patch.Fix, patch.FixSet = text, true
		}
	}
	if value, present := body["fixAction"]; present {
		patch.FixKindSet = true
		if !isNull(value) {
			var action map[string]json.RawMessage
			kind := ""
			ok := json.Unmarshal(value, &action) == nil && action != nil
			if ok {
				kind, ok = oneOfText(action["kind"], fixKinds)
			}
			if !ok {
				return patch, "fixAction must be null or { kind } with kind one of: " +
					strings.Join(fixKinds, ", ") + "."
			}
			patch.FixKind = &kind
		}
	}
	for _, field := range []string{"retryable", "customerVisible"} {
		value, present := body[field]
		if !present {
			continue
		}
		var flag bool
		if json.Unmarshal(value, &flag) != nil {
			return patch, field + " must be true or false."
		}
		if field == "retryable" {
			patch.Retryable = &flag
		} else {
			patch.CustomerVisible = &flag
		}
	}
	return patch, ""
}

// createFailureReason adds a rule, usually from the unmapped queue's "Add a
// rule". Hand-mounted so every field is judged here, in one order, and an
// undeclared one is refused rather than dropped.
func (s *Server) createFailureReason(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	operator, err := s.requireOperator(ctx)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Sign in to the operator console.")
		return
	}
	body, problem := readRuleBody(r, editableRuleFields, questionRuleFields)
	if problem == "" {
		var rule failures.Rule
		rule, problem = newRule(body)
		if problem == "" {
			created, err := store.CreateFailureRule(ctx, s.DB, rule, operator.Email)
			if errors.Is(err, store.ErrDuplicateRule) {
				writeError(w, http.StatusConflict, "conflict", fmt.Sprintf(
					"A rule for %s on this source already exists. Edit it instead.", rule.MatchValue))
				return
			}
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			store.ForgetFailureRules(s.Hot)
			if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "failure_reason.create",
				nil, "", created.Code, fmt.Sprintf("Added a %s rule for %s (%s) as %s",
					created.MatchKind, created.MatchValue, created.Source, created.Code)); err != nil {
				s.internalError(w, r, err)
				return
			}
			writeJSON(w, http.StatusCreated, dictionaryRow(created))
			return
		}
	}
	writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
}

// newRule validates a whole create body.
func newRule(body map[string]json.RawMessage) (failures.Rule, string) {
	var rule failures.Rule
	if value, present := body["channel"]; present && !isNull(value) {
		channel, ok := oneOfText(value, channelNames())
		if !ok {
			return rule, "channel must be null or a known channel."
		}
		rule.Channel = &channel
	}
	source, ok := oneOfText(body["source"], failureSources)
	if !ok {
		return rule, "source must be one of: " + strings.Join(failureSources, ", ") + "."
	}
	rule.Source = source
	var match struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(body["match"], &match) != nil || (match.Kind != "exact" && match.Kind != "prefix") ||
		strings.TrimSpace(match.Value) == "" {
		return rule, "match must be { kind: exact or prefix, value: a non-blank code }."
	}
	rule.MatchKind, rule.MatchValue = match.Kind, strings.TrimSpace(match.Value)
	var code string
	if json.Unmarshal(body["code"], &code) != nil || strings.TrimSpace(code) == "" {
		return rule, "code is required and cannot be blank."
	}
	rule.Code = strings.TrimSpace(code)
	for _, field := range []string{"routeId", "vendor"} {
		value, present := body[field]
		if !present {
			continue
		}
		text, problem := nullableText(value, field)
		if problem != "" {
			return rule, problem
		}
		if field == "routeId" {
			rule.RouteID = text
		} else {
			rule.Vendor = text
		}
	}
	for _, field := range []string{"layer", "title", "retryable", "customerVisible"} {
		if _, present := body[field]; !present {
			return rule, field + " is required."
		}
	}
	patch, problem := rulePatch(body)
	if problem != "" {
		return rule, problem
	}
	rule.Layer, rule.Title = *patch.Layer, *patch.Title
	rule.Detail, rule.Fix, rule.FixKind = patch.Detail, patch.Fix, patch.FixKind
	rule.Retryable, rule.CustomerVisible = *patch.Retryable, *patch.CustomerVisible
	return rule, ""
}

// updateFailureReason edits a rule as its next version. The rule is looked up
// before the body is judged, so an unknown id is 404 whatever the body says;
// an empty body is 422; every field is validated before any is written.
func (s *Server) updateFailureReason(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	operator, err := s.requireOperator(ctx)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Sign in to the operator console.")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := store.GetFailureRule(ctx, s.DB, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "No such rule.")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	body, problem := readRuleBody(r, editableRuleFields)
	if problem == "" && len(body) == 0 {
		problem = "Send at least one field to change."
	}
	var patch store.FailureRulePatch
	if problem == "" {
		patch, problem = rulePatch(body)
	}
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	updated, err := store.UpdateFailureRule(ctx, s.DB, id, patch, operator.Email)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	store.ForgetFailureRules(s.Hot)
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "failure_reason.update", nil, "",
		updated.Code, fmt.Sprintf("Edited the rule for %s (%s) to version %d",
			updated.MatchValue, updated.Source, updated.Version)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dictionaryRow(updated))
}
