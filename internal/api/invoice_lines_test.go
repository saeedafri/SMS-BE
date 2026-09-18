package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// seedLastMonthDelivered writes delivered SMS into the month the invoicing job
// bills: last month, in IST.
func (h *harness) seedLastMonthDelivered(tenant uuid.UUID, count int, segments uint8, cost int64) {
	h.t.Helper()
	ist := time.FixedZone("IST", 5*3600+1800)
	now := time.Now().In(ist)
	at := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, ist).AddDate(0, -1, 9).UTC()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	records := make([]store.MessageRecord, 0, count)
	for i := 0; i < count; i++ {
		records = append(records, store.MessageRecord{
			TenantID: tenant, ID: uuid.New(), Channel: "SMS", Country: "IN", SenderHeader: "ACMERT",
			Msisdn: "919820000041", Status: "delivered", FraudFlag: "none", Segments: segments,
			CostMinor: cost, Currency: "INR", CreatedAt: at, UpdatedAt: at, Version: 3,
		})
	}
	if err := store.InsertMessages(context.Background(), conn, records); err != nil {
		h.t.Fatalf("seed messages: %v", err)
	}
}

func (h *harness) invoiceIDs(acct account) []string {
	h.t.Helper()
	var page gen.InvoicePage
	h.do(http.MethodGet, "/v1/billing/invoices", acct.Token, nil).decode(h.t, &page)
	ids := make([]string, 0, len(page.Invoices))
	for _, invoice := range page.Invoices {
		ids = append(ids, invoice.Id)
	}
	return ids
}

// Ask 38. Every line the invoice detail serves is in the contract: channel null
// or a ChannelId, quantity at least 1, and no per-campaign fields.
func TestAnInvoiceLineIsInContract(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.seedLastMonthDelivered(acct.TenantID, 3, 1, 12)
	_ = h.server.IssueMonthlyInvoices(context.Background())

	ids := h.invoiceIDs(acct)
	if len(ids) != 1 {
		t.Fatalf("%d invoices issued for last month's delivered traffic, want 1", len(ids))
	}
	res := h.do(http.MethodGet, "/v1/billing/invoices/"+ids[0], acct.Token, nil)
	var raw struct {
		LineItems []map[string]json.RawMessage `json:"lineItems"`
	}
	res.decode(t, &raw)
	if len(raw.LineItems) == 0 {
		t.Fatal("the invoice has no lines")
	}
	for _, line := range raw.LineItems {
		var channel *string
		_ = json.Unmarshal(line["channel"], &channel)
		if channel != nil && !gen.ChannelId(*channel).Valid() {
			t.Errorf("channel %q is not a ChannelId", *channel)
		}
		var quantity int
		if err := json.Unmarshal(line["quantity"], &quantity); err != nil || quantity < 1 {
			t.Errorf("quantity = %s, want an integer of at least 1", line["quantity"])
		}
		if _, present := line["campaignId"]; present {
			t.Error("a line still carries campaignId")
		}
	}
}

// Ask 38. A delivered message whose cost is not a whole number of segments is a
// pricing defect. That tenant's invoice is not issued, and the error names it.
func TestAMispricedMessageBlocksTheInvoice(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.seedLastMonthDelivered(acct.TenantID, 1, 2, 25)

	err := h.server.IssueMonthlyInvoices(context.Background())
	if ids := h.invoiceIDs(acct); len(ids) != 0 {
		t.Fatalf("an invoice was issued over a message costing 25 for 2 segments")
	}
	if err == nil || !strings.Contains(err.Error(), acct.TenantID.String()) {
		t.Fatalf("error = %v, want one naming tenant %s", err, acct.TenantID)
	}
}

// Ask 38. Reconciliation is enforced by the database, not by the one function
// that writes lines today.
func TestTheLineReconcilesConstraintHolds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	invoiceID := seedInvoice(t, h, acct.TenantID, "INR", 100)
	_, err := h.admin.Exec(context.Background(), `
		INSERT INTO invoice_line_items (invoice_id, tenant_id, description, quantity, unit_minor, amount_minor)
		VALUES ($1, $2, 'SMS messages', 3, 33, 100)`, invoiceID, acct.TenantID)
	if err == nil || !strings.Contains(err.Error(), "invoice_line_reconciles") {
		t.Fatalf("inserting 3 x 33 = 100: err = %v, want invoice_line_reconciles", err)
	}
}
