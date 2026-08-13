package api

import (
	"context"
	"errors"
	"time"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// rangeSince turns the contract's coarse range into a start time.
func rangeSince(value string) time.Time {
	days := 30
	switch value {
	case "7d":
		days = 7
	case "90d":
		days = 90
	}
	// Truncated to the hour because the rollup is hourly: a start time mid-hour
	// would silently drop part of the oldest bucket.
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Hour)
}

func (s *Server) GetAnalytics(ctx context.Context, request gen.GetAnalyticsRequestObject) (gen.GetAnalyticsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if s.ClickHouse == nil {
		return nil, errClickHouseUnavailable
	}

	filter := store.AnalyticsFilter{Since: rangeSince("30d")}
	if request.Params.Range != nil {
		filter.Since = rangeSince(string(*request.Params.Range))
	}
	if request.Params.Channel != nil {
		filter.Channel = string(*request.Params.Channel)
	}
	if request.Params.Country != nil {
		filter.Country = string(*request.Params.Country)
	}

	summary, buckets, deliverability, err := store.QueryAnalytics(
		ctx, s.ClickHouse, identity.TenantID, filter)
	if err != nil {
		return nil, err
	}

	// A delivery rate with no traffic behind it is 0, not NaN or 1. Dividing by
	// a zero denominator here would render as "NaN%" on an empty dashboard.
	deliveryRate := 0.0
	if summary.Sent > 0 {
		deliveryRate = float64(summary.Delivered) / float64(summary.Sent)
	}

	out := gen.Analytics{
		Summary: gen.AnalyticsSummary{
			Sent: summary.Sent, Delivered: summary.Delivered,
			Failed: summary.Failed, Read: summary.Read,
			DeliveryRate: float32(deliveryRate),
			CostMinor:    int(summary.CostMinor),
			// Cost per conversion needs conversion tracking, which does not
			// exist yet. Reporting cost here instead would silently redefine
			// the metric, so it stays zero until there is something real.
			CostPerConversionMinor: 0,
			Currency:               gen.CurrencyCode(summary.Currency),
			CurrencyMixed:          summary.CurrencyMixed,
			Latency:                gen.AnalyticsLatency{P50Ms: 0, P90Ms: 0},
			FraudCounts:            gen.MessageFraudCounts{Velocity: 0, GeoAnomaly: 0, Blocked: 0},
		},
		Buckets:        make([]gen.AnalyticsBucket, 0, len(buckets)),
		Deliverability: make([]gen.AnalyticsDeliverabilityRow, 0, len(deliverability)),
	}
	for _, bucket := range buckets {
		out.Buckets = append(out.Buckets, gen.AnalyticsBucket{
			BucketStart: bucket.BucketStart, Sent: bucket.Sent,
			Delivered: bucket.Delivered, Failed: bucket.Failed,
			Read: bucket.Read, CostMinor: int(bucket.CostMinor),
		})
	}
	for _, row := range deliverability {
		rate := 0.0
		if row.Sent > 0 {
			rate = float64(row.Delivered) / float64(row.Sent)
		}
		out.Deliverability = append(out.Deliverability, gen.AnalyticsDeliverabilityRow{
			Country: gen.CountryCode(row.Country), Channel: gen.ChannelId(row.Channel),
			// Per-carrier attribution needs the carrier the route actually
			// used, which the sandbox connector does not report. "all" is
			// honest about the granularity we have rather than inventing one.
			Carrier: gen.CarrierId("all"),
			Sent:    row.Sent, Delivered: row.Delivered, DeliveryRate: float32(rate),
		})
	}
	return gen.GetAnalytics200JSONResponse(out), nil
}

func toScheduledReport(report store.ScheduledReport) gen.ScheduledReport {
	out := gen.ScheduledReport{
		Id: report.ID, Frequency: gen.ReportFrequency(report.Frequency),
		Range: gen.AnalyticsRange(report.Range), Recipients: report.Recipients,
		Paused: report.Paused, CreatedAt: report.CreatedAt,
		RecentSends: []time.Time{},
	}
	if !report.Paused {
		next := nextSend(report.CreatedAt, report.Frequency)
		out.NextSendAt = &next
		out.RecentSends = recentSends(report.CreatedAt, report.Frequency)
	}
	return out
}

// nextSend is the next occurrence after now, derived rather than stored. A
// stored timestamp would go stale the moment the process missed a tick.
func nextSend(createdAt time.Time, frequency string) time.Time {
	step := 24 * time.Hour
	switch frequency {
	case "weekly":
		step = 7 * 24 * time.Hour
	case "monthly":
		step = 30 * 24 * time.Hour
	}
	next := createdAt
	now := time.Now().UTC()
	for !next.After(now) {
		next = next.Add(step)
	}
	return next
}

// recentSends is the last five occurrences before now, capped as the contract
// requires. Also derived: there is no send history table yet, and the contract
// documents this field as computed rather than stored.
func recentSends(createdAt time.Time, frequency string) []time.Time {
	step := 24 * time.Hour
	switch frequency {
	case "weekly":
		step = 7 * 24 * time.Hour
	case "monthly":
		step = 30 * 24 * time.Hour
	}
	now := time.Now().UTC()
	var all []time.Time
	for point := createdAt; point.Before(now); point = point.Add(step) {
		all = append(all, point)
	}
	// Newest first, capped to five.
	out := make([]time.Time, 0, 5)
	for i := len(all) - 1; i >= 0 && len(out) < 5; i-- {
		out = append(out, all[i])
	}
	return out
}

func (s *Server) ListScheduledReports(ctx context.Context, _ gen.ListScheduledReportsRequestObject) (gen.ListScheduledReportsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	reports, err := store.ListScheduledReports(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.ScheduledReport, 0, len(reports))
	for _, report := range reports {
		out = append(out, toScheduledReport(report))
	}
	return gen.ListScheduledReports200JSONResponse(out), nil
}

func (s *Server) CreateScheduledReport(ctx context.Context, request gen.CreateScheduledReportRequestObject) (gen.CreateScheduledReportResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if len(request.Body.Recipients) == 0 {
		return gen.CreateScheduledReport422JSONResponse(errorBody(codeValidation,
			"At least one recipient is required.")), nil
	}
	report, err := store.CreateScheduledReport(ctx, s.DB, identity, store.ScheduledReport{
		Frequency:  string(request.Body.Frequency),
		Range:      string(request.Body.Range),
		Recipients: request.Body.Recipients,
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateScheduledReport201JSONResponse(toScheduledReport(report)), nil
}

func (s *Server) DeleteScheduledReport(ctx context.Context, request gen.DeleteScheduledReportRequestObject) (gen.DeleteScheduledReportResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	reportID, valid := parsePathID(request.Id)
	if !valid {
		return gen.DeleteScheduledReport404JSONResponse(errorBody("not_found", "No such report.")), nil
	}
	err := store.DeleteScheduledReport(ctx, s.DB, identity, reportID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DeleteScheduledReport404JSONResponse(errorBody("not_found", "No such report.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteScheduledReport204Response{}, nil
}

func toSSO(settings store.TenantSettings) gen.SsoConfig {
	config := gen.SsoConfig{
		Enabled: settings.SSOEnabled, MetadataUrl: settings.SSOMetadataURL,
		EntityId: settings.SSOEntityID,
	}
	if settings.SSOProvider != nil {
		provider := gen.SsoConfigProvider(*settings.SSOProvider)
		config.Provider = &provider
	}
	return config
}

func (s *Server) GetSso(ctx context.Context, _ gen.GetSsoRequestObject) (gen.GetSsoResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	settings, err := store.GetTenantSettings(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	return gen.GetSso200JSONResponse(toSSO(settings)), nil
}

func (s *Server) UpdateSso(ctx context.Context, request gen.UpdateSsoRequestObject) (gen.UpdateSsoResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// Enabling SSO without an identity provider would lock every user out of
	// the tenant at the next sign-in, so the details are required to turn it on
	// rather than validated later.
	if request.Body.Enabled && (request.Body.Provider == nil || request.Body.MetadataUrl == nil ||
		*request.Body.MetadataUrl == "") {
		return gen.UpdateSso422JSONResponse(errorBody(codeValidation,
			"A provider and metadata URL are required to enable SSO.")), nil
	}
	settings := store.TenantSettings{
		SSOEnabled:     request.Body.Enabled,
		SSOMetadataURL: request.Body.MetadataUrl,
		SSOEntityID:    request.Body.EntityId,
	}
	if request.Body.Provider != nil {
		provider := string(*request.Body.Provider)
		settings.SSOProvider = &provider
	}
	updated, err := store.UpdateSSO(ctx, s.DB, identity, settings)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSso200JSONResponse(toSSO(updated)), nil
}

func (s *Server) GetDataRetention(ctx context.Context, _ gen.GetDataRetentionRequestObject) (gen.GetDataRetentionResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	settings, err := store.GetTenantSettings(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	return gen.GetDataRetention200JSONResponse(gen.DataRetentionSettings{
		MessageLogRetentionDays: gen.DataRetentionSettingsMessageLogRetentionDays(
			settings.MessageLogRetentionDays),
	}), nil
}

func (s *Server) UpdateDataRetention(ctx context.Context, request gen.UpdateDataRetentionRequestObject) (gen.UpdateDataRetentionResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	days := int(request.Body.MessageLogRetentionDays)
	switch days {
	case 30, 90, 180, 365:
	default:
		return gen.UpdateDataRetention422JSONResponse(errorBody(codeValidation,
			"Retention must be 30, 90, 180 or 365 days.")), nil
	}
	settings, err := store.UpdateRetention(ctx, s.DB, identity, days)
	if err != nil {
		return nil, err
	}
	return gen.UpdateDataRetention200JSONResponse(gen.DataRetentionSettings{
		MessageLogRetentionDays: gen.DataRetentionSettingsMessageLogRetentionDays(
			settings.MessageLogRetentionDays),
	}), nil
}
