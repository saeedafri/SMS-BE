package api

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/store"
)

// Per-recipient frequency caps: how many messages one handset may receive from
// the tenant on one channel per day, week and month. Mounted directly, outside
// the generated contract, until the frontend adds them; the shapes are in
// docs/HANDOFF_TO_UI_2026-10-02-sigmo-gaps.md.
//
// Session only. A key is not a credential here (see keyRoutes), and changing
// how often customers are contacted is a settings decision, so a member may read
// but only an owner or admin may write.
func (s *Server) mountFrequencyCapRoutes(r chi.Router) {
	r.Get("/v1/frequency-caps", s.listFrequencyCaps)
	r.Put("/v1/frequency-caps/{channel}", s.putFrequencyCap)
}

// maxExcludedNumbers keeps the list a setting rather than a contact database.
const maxExcludedNumbers = 1000

type frequencyCapBody struct {
	Channel         string   `json:"channel"`
	DailyLimit      *int     `json:"dailyLimit"`
	WeeklyLimit     *int     `json:"weeklyLimit"`
	MonthlyLimit    *int     `json:"monthlyLimit"`
	ExcludedNumbers []string `json:"excludedNumbers"`
}

func capBody(c store.FrequencyCap) frequencyCapBody {
	excluded := c.ExcludedNumbers
	if excluded == nil {
		excluded = []string{}
	}
	return frequencyCapBody{Channel: c.Channel, DailyLimit: c.DailyLimit,
		WeeklyLimit: c.WeeklyLimit, MonthlyLimit: c.MonthlyLimit, ExcludedNumbers: excluded}
}

func (s *Server) listFrequencyCaps(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	caps, err := store.ListFrequencyCaps(r.Context(), s.DB, identity)
	if err != nil {
		s.Logger.Error("list frequency caps", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	out := make([]frequencyCapBody, 0, len(caps))
	for _, c := range caps {
		out = append(out, capBody(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"caps": out})
}

func (s *Server) putFrequencyCap(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	if !canManageSettings(identity.Role) {
		writeError(w, http.StatusForbidden, codeForbidden,
			"Member role cannot change frequency caps.")
		return
	}
	channel := chi.URLParam(r, "channel")
	// Email recipients are addresses, not numbers, and a cap on them needs its
	// own exclusion rules. Refused rather than half-supported.
	if !slices.Contains(validChannels, channel) || channel == "EMAIL" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"Frequency caps apply to SMS, RCS, WHATSAPP and VOICE.")
		return
	}
	var body frequencyCapBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		if isBodyTooLarge(err) {
			writeTooLarge(w, bodyLimitFor(r))
			return
		}
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"can't decode JSON body: "+err.Error())
		return
	}
	frequencyCap, problem := validateFrequencyCap(channel, body)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	if err := store.SaveFrequencyCap(r.Context(), s.DB, s.Hot, identity, frequencyCap); err != nil {
		s.Logger.Error("save frequency cap", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, capBody(frequencyCap))
}

func validateFrequencyCap(channel string, body frequencyCapBody) (store.FrequencyCap, string) {
	if body.Channel != "" && body.Channel != channel {
		return store.FrequencyCap{}, "channel in the body must match the channel in the path."
	}
	limits := []struct {
		name  string
		value *int
	}{{"dailyLimit", body.DailyLimit}, {"weeklyLimit", body.WeeklyLimit},
		{"monthlyLimit", body.MonthlyLimit}}
	var previous *int
	var previousName string
	for _, limit := range limits {
		if limit.value == nil {
			continue
		}
		if *limit.value < 1 || *limit.value > 100000 {
			return store.FrequencyCap{}, limit.name + " must be between 1 and 100000, or null for no limit."
		}
		// A daily limit above the weekly one could never be reached, and
		// accepting it hides a mistake the customer would otherwise see.
		if previous != nil && *limit.value < *previous {
			return store.FrequencyCap{}, limit.name + " cannot be lower than " + previousName + "."
		}
		previous, previousName = limit.value, limit.name
	}
	if len(body.ExcludedNumbers) > maxExcludedNumbers {
		return store.FrequencyCap{}, "excludedNumbers holds at most 1000 numbers."
	}
	excluded := make([]string, 0, len(body.ExcludedNumbers))
	for _, raw := range body.ExcludedNumbers {
		number, valid := audience.NormaliseE164(raw)
		if !valid {
			return store.FrequencyCap{}, raw + " is not a phone number with a country code."
		}
		if !slices.Contains(excluded, number) {
			excluded = append(excluded, number)
		}
	}
	slices.Sort(excluded)
	return store.FrequencyCap{Channel: channel, DailyLimit: body.DailyLimit,
		WeeklyLimit: body.WeeklyLimit, MonthlyLimit: body.MonthlyLimit,
		ExcludedNumbers: excluded}, ""
}
