package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/saeedafri/sms-be/internal/platform/s3put"
)

type fakeS3 struct {
	server  *httptest.Server
	mu      sync.Mutex
	puts    map[string]string
	auth    string
	respond int
}

func startFakeS3(t *testing.T, status int) *fakeS3 {
	t.Helper()
	f := &fakeS3{puts: map[string]string{}, respond: status}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.puts[r.URL.Path] = string(body)
		f.auth = r.Header.Get("Authorization")
		status := f.respond
		f.mu.Unlock()
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.puts) }

func (f *fakeS3) only() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.puts {
		return k, v
	}
	return "", ""
}

var s3Body = map[string]any{"bucket": "acme-exports", "region": "ap-south-1", "prefix": "relay/reports/",
	"accessKeyId": "AKIAIOSFODNN7EXAMPLE", "secretAccessKey": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}

func TestADueReportIsAlsoWrittenToTheCustomersBucket(t *testing.T) {
	h := newSendHarness(t)
	h.server.S3 = s3put.Client{Endpoint: startFakeS3(t, 200).server.URL}
	fake := startFakeS3(t, 200)
	h.server.S3 = s3put.Client{Endpoint: fake.server.URL}
	mail := h.recordMail()
	tenant := h.newAccount("owner")

	res := h.do(http.MethodPut, "/v1/report-export/s3", tenant.Token, s3Body)
	if res.Code != 200 || strings.Contains(string(res.Body), "wJalr") || strings.Contains(string(res.Body), "AKIAIOSFODNN7") {
		t.Fatalf("put = %d %s, want no secret or full key id back", res.Code, res.Body)
	}
	var shown struct {
		Configured       bool
		Bucket, Prefix   string
		AccessKeyIdLast4 string
	}
	_ = json.Unmarshal(res.Body, &shown)
	if !shown.Configured || shown.Bucket != "acme-exports" || shown.Prefix != "relay/reports" || shown.AccessKeyIdLast4 != "MPLE" {
		t.Errorf("shown = %+v", shown)
	}

	address := uniqueAddress("ceo")
	res = h.do(http.MethodPost, "/v1/analytics/reports", tenant.Token, map[string]any{
		"frequency": "daily", "range": "7d", "recipients": []string{address}})
	var created reportView
	res.decode(t, &created)
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE scheduled_reports SET next_send_at = now() - interval '1 minute' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	_ = h.server.SendDueReports(context.Background())

	key, body := fake.only()
	if fake.count() != 1 || !strings.HasPrefix(key, "/acme-exports/relay/reports/daily/") || !strings.HasSuffix(key, ".csv") {
		t.Fatalf("uploads = %d, key %q", fake.count(), key)
	}
	if !strings.Contains(body, "total_sent") || !strings.Contains(body, "day,sent,delivered,failed,cost_minor") {
		t.Errorf("csv = %q", body)
	}
	if !strings.HasPrefix(fake.auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") {
		t.Errorf("not signed: %s", fake.auth)
	}
	if mail.to(address) != 1 {
		t.Errorf("the email did not go as well: %d", mail.to(address))
	}
	var status struct{ LastStatus *string }
	_ = json.Unmarshal(h.do(http.MethodGet, "/v1/report-export/s3", tenant.Token, nil).Body, &status)
	if status.LastStatus == nil || *status.LastStatus != "ok" {
		t.Errorf("lastStatus = %v, want ok", status.LastStatus)
	}
}

// A bucket that refuses must not cost the customer their email, and the screen
// has to say why nothing arrived.
func TestAFailingBucketNeverStopsTheEmailAndIsReported(t *testing.T) {
	h := newSendHarness(t)
	h.server.S3 = s3put.Client{Endpoint: startFakeS3(t, 403).server.URL}
	mail := h.recordMail()
	tenant := h.newAccount("owner")
	h.do(http.MethodPut, "/v1/report-export/s3", tenant.Token, s3Body)
	address := uniqueAddress("cfo")
	var created reportView
	h.do(http.MethodPost, "/v1/analytics/reports", tenant.Token, map[string]any{
		"frequency": "daily", "range": "7d", "recipients": []string{address}}).decode(t, &created)
	_, _ = h.admin.Exec(context.Background(),
		`UPDATE scheduled_reports SET next_send_at = now() - interval '1 minute' WHERE id = $1`, created.ID)
	_ = h.server.SendDueReports(context.Background())

	if mail.to(address) != 1 {
		t.Errorf("a refusing bucket stopped the email: %d", mail.to(address))
	}
	var status struct{ LastStatus, LastError *string }
	_ = json.Unmarshal(h.do(http.MethodGet, "/v1/report-export/s3", tenant.Token, nil).Body, &status)
	if status.LastStatus == nil || *status.LastStatus != "failed" || status.LastError == nil ||
		!strings.Contains(*status.LastError, "AccessDenied") {
		t.Errorf("status = %v / %v, want failed with AccessDenied", status.LastStatus, status.LastError)
	}
}

func TestTheConnectionTestWritesOneObjectAndSaysSo(t *testing.T) {
	h := newSendHarness(t)
	fake := startFakeS3(t, 200)
	h.server.S3 = s3put.Client{Endpoint: fake.server.URL}
	tenant := h.newAccount("owner")
	if res := h.do(http.MethodPost, "/v1/report-export/s3/test", tenant.Token, nil); res.Code != 409 {
		t.Errorf("test with no destination = %d, want 409", res.Code)
	}
	h.do(http.MethodPut, "/v1/report-export/s3", tenant.Token, s3Body)
	res := h.do(http.MethodPost, "/v1/report-export/s3/test", tenant.Token, nil)
	if res.Code != 200 || !strings.Contains(string(res.Body), `"ok":true`) || fake.count() != 1 {
		t.Errorf("test = %d %s, uploads %d", res.Code, res.Body, fake.count())
	}
	fake.mu.Lock()
	fake.respond = 403
	fake.mu.Unlock()
	res = h.do(http.MethodPost, "/v1/report-export/s3/test", tenant.Token, nil)
	if !strings.Contains(string(res.Body), `"ok":false`) || !strings.Contains(string(res.Body), "AccessDenied") {
		t.Errorf("failing test = %s", res.Body)
	}
}

func TestReportExportRefusesBadDestinationsAndNonAdmins(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	with := func(k string, v any) map[string]any {
		b := map[string]any{}
		for key, val := range s3Body {
			b[key] = val
		}
		b[k] = v
		return b
	}
	for name, body := range map[string]map[string]any{
		"host as region": with("region", "evil.example.com"),
		"bad bucket":     with("bucket", "A/B"),
		"traversal":      with("prefix", "a/../b"),
		"short secret":   with("secretAccessKey", "x"),
		"unknown field":  with("endpoint", "https://evil.example"),
	} {
		if res := h.do(http.MethodPut, "/v1/report-export/s3", tenant.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	member := h.newAccount("member")
	if res := h.do(http.MethodPut, "/v1/report-export/s3", member.Token, s3Body); res.Code != 403 {
		t.Errorf("member put = %d, want 403", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/report-export/s3", member.Token, nil); res.Code != 200 {
		t.Errorf("member get = %d, want 200", res.Code)
	}
	if res := h.do(http.MethodDelete, "/v1/report-export/s3", tenant.Token, nil); res.Code != 204 {
		t.Errorf("delete = %d", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/report-export/s3", "", nil); res.Code != 401 {
		t.Errorf("anonymous = %d", res.Code)
	}
}
