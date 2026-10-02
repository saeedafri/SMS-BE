package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saeedafri/sms-be/internal/platform/s3put"
	"github.com/saeedafri/sms-be/internal/store"
)

// Report export to the customer's own S3 bucket. Scheduled reports are still
// emailed; when a destination is set each send also writes a CSV there.
// Mounted directly until the contract declares it.
func (s *Server) mountReportS3Routes(r chi.Router) {
	r.Get("/v1/report-export/s3", s.getReportS3)
	r.Put("/v1/report-export/s3", s.putReportS3)
	r.Delete("/v1/report-export/s3", s.deleteReportS3)
	r.Post("/v1/report-export/s3/test", s.testReportS3)
}

type reportS3Out struct {
	Configured  bool       `json:"configured"`
	Bucket      string     `json:"bucket,omitempty"`
	Region      string     `json:"region,omitempty"`
	Prefix      string     `json:"prefix,omitempty"`
	AccessKeyID string     `json:"accessKeyIdLast4,omitempty"`
	LastStatus  *string    `json:"lastStatus"`
	LastError   *string    `json:"lastError"`
	LastAt      *time.Time `json:"lastAt"`
}

func toReportS3Out(d store.ReportS3, found bool) reportS3Out {
	if !found {
		return reportS3Out{}
	}
	last4 := d.AccessKeyID
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	return reportS3Out{true, d.Bucket, d.Region, d.Prefix, last4, d.LastStatus, d.LastError, d.LastAt}
}

func (s *Server) s3Client() s3put.Client { return s.S3 }

func (s *Server) reportS3Identity(w http.ResponseWriter, r *http.Request, write bool) (store.Identity, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return identity, false
	}
	if write && !canManageSettings(identity.Role) {
		writeError(w, http.StatusForbidden, codeForbidden, "Member role cannot change report export.")
		return identity, false
	}
	return identity, true
}

func (s *Server) getReportS3(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.reportS3Identity(w, r, false)
	if !ok {
		return
	}
	d, found, err := store.GetReportS3(r.Context(), s.DB, identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, toReportS3Out(d, found))
}

type reportS3Body struct {
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

func (s *Server) putReportS3(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.reportS3Identity(w, r, true)
	if !ok {
		return
	}
	var body reportS3Body
	if !decodeStrict(w, r, &body) {
		return
	}
	dest := s3put.Destination{Bucket: strings.TrimSpace(body.Bucket), Region: strings.TrimSpace(body.Region),
		Prefix: strings.Trim(strings.TrimSpace(body.Prefix), "/"), AccessKeyID: strings.TrimSpace(body.AccessKeyID),
		SecretAccessKey: body.SecretAccessKey}
	if err := dest.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
		return
	}
	if s.Secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Report export is not available on this deployment.")
		return
	}
	sealed, err := s.Secrets.Encrypt(dest.SecretAccessKey)
	if err != nil {
		s.Logger.Error("seal s3 secret", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	saved := store.ReportS3{Bucket: dest.Bucket, Region: dest.Region, Prefix: dest.Prefix,
		AccessKeyID: dest.AccessKeyID, SealedSecret: sealed}
	if err := store.SaveReportS3(r.Context(), s.DB, identity, saved); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, toReportS3Out(saved, true))
}

func (s *Server) deleteReportS3(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.reportS3Identity(w, r, true)
	if !ok {
		return
	}
	if err := store.DeleteReportS3(r.Context(), s.DB, identity); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// destination opens a tenant's stored bucket settings.
func (s *Server) destination(ctx context.Context, identity store.Identity) (s3put.Destination, bool) {
	d, found, err := store.GetReportS3(ctx, s.DB, identity)
	if err != nil || !found || s.Secrets == nil {
		return s3put.Destination{}, false
	}
	secret, err := s.Secrets.Decrypt(d.SealedSecret)
	if err != nil {
		return s3put.Destination{}, false
	}
	return s3put.Destination{Bucket: d.Bucket, Region: d.Region, Prefix: d.Prefix,
		AccessKeyID: d.AccessKeyID, SecretAccessKey: secret}, true
}

// testReportS3 writes one small object so a customer learns the credentials and
// the bucket policy work before the first scheduled report is due.
func (s *Server) testReportS3(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.reportS3Identity(w, r, true)
	if !ok {
		return
	}
	dest, found := s.destination(r.Context(), identity)
	if !found {
		writeError(w, http.StatusConflict, "conflict", "Set a destination first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	key := "relay-connection-test-" + strconv.FormatInt(time.Now().Unix(), 10) + ".txt"
	err := s.s3Client().Put(ctx, dest, key, "text/plain",
		[]byte("Relay can write to this bucket. You may delete this file.\n"))
	store.NoteReportS3Result(context.WithoutCancel(r.Context()), s.DB, identity, err == nil, errText(err))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": strings.TrimPrefix(dest.Prefix+"/"+key, "/")})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// reportCSV is a report's day-by-day rows, with the totals first.
func reportCSV(report store.ScheduledReport, summary store.AnalyticsSummary, buckets []store.AnalyticsBucket) []byte {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	_ = w.Write([]string{"report", report.Frequency, "range", report.Range})
	_ = w.Write([]string{"total_sent", strconv.Itoa(summary.Sent), "delivered", strconv.Itoa(summary.Delivered),
		"failed", strconv.Itoa(summary.Failed), "cost_minor", strconv.FormatInt(summary.CostMinor, 10),
		"currency", summary.Currency})
	_ = w.Write(nil)
	_ = w.Write([]string{"day", "sent", "delivered", "failed", "cost_minor"})
	for _, b := range buckets {
		_ = w.Write([]string{b.BucketStart.Format("2006-01-02"), strconv.Itoa(b.Sent),
			strconv.Itoa(b.Delivered), strconv.Itoa(b.Failed), strconv.FormatInt(b.CostMinor, 10)})
	}
	w.Flush()
	return out.Bytes()
}
