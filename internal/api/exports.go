package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"time"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// csvDownload writes a streamed CSV attachment.
//
// A hand-written response object rather than the generated one because the
// generated type sets Content-Type and nothing else, and a download without
// Content-Disposition opens in the browser instead of saving. The charset is
// explicit: Excel reads a CSV without one as mojibake, which turns any
// non-ASCII tenant name into a support ticket.
type csvDownload struct {
	filename string
	body     io.Reader
}

func (d csvDownload) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", d.filename))
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, d.body)
	return err
}

type auditLogCSV struct{ csvDownload }

func (r auditLogCSV) VisitExportAuditLogResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// ExportAuditLog streams the whole filtered audit log as CSV.
//
// The filters mean exactly what they mean on the paged endpoint, which is the
// requirement this exists for: an export that quietly ignores the filters an
// operator can see on screen is worse than no export, because nobody finds out
// until they read the file.
//
// Streamed through a pipe rather than assembled: the rows go out as they are
// read, so a 45,000-row export costs one row of memory rather than 45,000.
func (s *Server) ExportAuditLog(ctx context.Context, request gen.ExportAuditLogRequestObject) (
	gen.ExportAuditLogResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return nil, errUnauthenticated
	}
	filter := store.AuditLogFilter{
		TenantID: request.Params.TenantId,
		Since:    rangeStart(request.Params.Range),
	}
	if request.Params.Action != nil {
		value := string(*request.Params.Action)
		filter.Action = &value
	}

	reader, writer := io.Pipe()
	go func() {
		out := csv.NewWriter(writer)
		err := out.Write([]string{"occurredAt", "actor", "action",
			"tenantId", "tenantName", "targetLabel", "detail"})
		if err == nil {
			err = store.StreamAuditLog(context.WithoutCancel(ctx), s.operatorPool(), filter,
				func(entry store.AuditEntry) error {
					tenantID := ""
					if entry.TenantID != nil {
						tenantID = entry.TenantID.String()
					}
					return out.Write([]string{
						// RFC3339 so a spreadsheet sorts it and a re-import
						// round-trips.
						entry.OccurredAt.UTC().Format(time.RFC3339),
						entry.Actor, entry.Action, tenantID,
						textOrEmpty(entry.TenantName), textOrEmpty(entry.TargetLabel),
						textOrEmpty(entry.Detail),
					})
				})
		}
		out.Flush()
		if err == nil {
			err = out.Error()
		}
		// A half-written file must not look complete. Closing with an error
		// breaks the response body rather than truncating it silently.
		_ = writer.CloseWithError(err)
	}()

	return auditLogCSV{csvDownload{
		filename: "audit-log-" + time.Now().UTC().Format("2006-01-02") + ".csv",
		body:     reader,
	}}, nil
}

func textOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type userActivityCSV struct{ csvDownload }

func (r userActivityCSV) VisitExportUserActivityResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// ExportUserActivity streams the whole filtered user-activity log as CSV.
//
// The audit export's twin, deliberately: same streaming, same disposition, same
// charset, same ordering as the paged endpoint it mirrors. The columns are
// every field of UserActivityEntry except the id, in schema order, which is the
// convention the audit export set and the one the frontend declared against.
func (s *Server) ExportUserActivity(ctx context.Context, request gen.ExportUserActivityRequestObject) (
	gen.ExportUserActivityResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return nil, errUnauthenticated
	}
	filter := store.UserActivityFilter{
		TenantID: request.Params.TenantId,
		Since:    rangeStart(request.Params.Range),
	}
	if request.Params.EventType != nil {
		value := string(*request.Params.EventType)
		filter.EventType = &value
	}

	reader, writer := io.Pipe()
	go func() {
		out := csv.NewWriter(writer)
		err := out.Write([]string{"occurredAt", "tenantId", "tenantName",
			"userName", "userEmail", "eventType", "detail"})
		if err == nil {
			err = store.StreamUserActivity(context.WithoutCancel(ctx), s.operatorPool(), filter,
				func(entry store.UserActivityEntry) error {
					return out.Write([]string{
						entry.OccurredAt.UTC().Format(time.RFC3339),
						entry.TenantID.String(), entry.TenantName,
						entry.UserName, entry.UserEmail, entry.EventType, entry.Detail,
					})
				})
		}
		out.Flush()
		if err == nil {
			err = out.Error()
		}
		// A half-written file must not look complete.
		_ = writer.CloseWithError(err)
	}()

	return userActivityCSV{csvDownload{
		filename: "user-activity-" + time.Now().UTC().Format("2006-01-02") + ".csv",
		body:     reader,
	}}, nil
}
