package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// Drip sending: a campaign goes out in instalments, N recipients every M
// minutes. See migration 00072 and sending.LaunchCampaign.
//
// dripBatchSize and dripIntervalMinutes arrive on POST /v1/campaigns, which is
// a generated operation whose request type does not declare them yet. The body
// is peeked here and the two values carried in the request context, so the
// create handler can read them today and keeps working unchanged once the
// contract declares them — the frontend adds the fields, nothing here moves.
type dripSettings struct {
	BatchSize       *int `json:"dripBatchSize"`
	IntervalMinutes *int `json:"dripIntervalMinutes"`
	// Malformed is set when the fields were present but not whole numbers.
	Malformed bool `json:"-"`
}

type dripKey struct{}

func withDripPeek(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/campaigns" && r.Body != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				if isBodyTooLarge(err) {
					writeTooLarge(w, bodyLimitFor(r))
					return
				}
				writeError(w, http.StatusUnprocessableEntity, codeValidation, "can't read the request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var settings dripSettings
			if json.Unmarshal(raw, &settings) != nil {
				// Either not JSON at all, which the binder will refuse, or the
				// fields are the wrong type, which the handler will.
				var probe map[string]json.RawMessage
				if json.Unmarshal(raw, &probe) == nil {
					_, a := probe["dripBatchSize"]
					_, b := probe["dripIntervalMinutes"]
					settings = dripSettings{Malformed: a || b}
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), dripKey{}, settings))
		}
		next.ServeHTTP(w, r)
	})
}

// dripFrom reads the settings the middleware left, and says what is wrong with
// them. An empty problem with nil settings means an ordinary campaign.
func dripFrom(ctx context.Context) (batch, interval *int, problem string) {
	settings, _ := ctx.Value(dripKey{}).(dripSettings)
	if settings.Malformed {
		return nil, nil, "dripBatchSize and dripIntervalMinutes must be whole numbers."
	}
	return validDrip(settings.BatchSize, settings.IntervalMinutes)
}

func validDrip(batch, interval *int) (*int, *int, string) {
	if batch == nil && interval == nil {
		return nil, nil, ""
	}
	if batch == nil || interval == nil {
		return nil, nil, "dripBatchSize and dripIntervalMinutes go together: send both or neither."
	}
	if *batch < 1 || *batch > 100000 {
		return nil, nil, "dripBatchSize must be between 1 and 100000."
	}
	if *interval < 1 || *interval > 1440 {
		return nil, nil, "dripIntervalMinutes must be between 1 and 1440 (one day)."
	}
	return batch, interval, ""
}

func (s *Server) mountCampaignDripRoutes(r chi.Router) {
	r.Get("/v1/campaigns/{id}/drip", s.getDrip)
	r.Put("/v1/campaigns/{id}/drip", s.putDrip)
}

func dripBody(c store.Campaign) map[string]any {
	body := map[string]any{"dripping": c.Dripping(), "batchSize": c.DripBatchSize,
		"intervalMinutes": c.DripIntervalMinutes, "nextBatchAt": nil}
	// Between instalments a dripping campaign is 'scheduled' with its next
	// instalment time as the schedule.
	if c.Dripping() && c.Status == "scheduled" && c.ScheduledAt != nil {
		body["nextBatchAt"] = c.ScheduledAt
	}
	return body
}

func (s *Server) getDrip(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return
	}
	campaign, err := store.GetCampaign(r.Context(), s.DB, identity, id)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "No such campaign.")
		return
	}
	writeJSON(w, http.StatusOK, dripBody(campaign))
}

// putDrip sets or clears the instalments on a campaign that has not started.
func (s *Server) putDrip(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return
	}
	var body struct {
		BatchSize       *int `json:"batchSize"`
		IntervalMinutes *int `json:"intervalMinutes"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	batch, interval, problem := validDrip(body.BatchSize, body.IntervalMinutes)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			problem)
		return
	}
	if _, err := store.GetCampaign(r.Context(), s.DB, identity, id); err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "No such campaign.")
		return
	}
	applied, err := store.SetDrip(r.Context(), s.DB, identity, id, batch, interval)
	if err != nil {
		s.Logger.Error("set drip", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	if !applied {
		writeError(w, http.StatusConflict, "conflict",
			"Instalments can only be changed on a scheduled campaign that has not started.")
		return
	}
	campaign, _ := store.GetCampaign(r.Context(), s.DB, identity, id)
	writeJSON(w, http.StatusOK, dripBody(campaign))
}
