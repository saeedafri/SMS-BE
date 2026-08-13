package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"
)

// harness is a live server backed by the real test database. These are
// integration tests on purpose: the thing most worth proving about identity is
// that RLS, the SECURITY DEFINER resolution functions, and the handlers agree
// with each other, and a mocked store would prove none of that.
type harness struct {
	t       *testing.T
	router  http.Handler
	pool    *pgxpool.Pool
	admin   *pgxpool.Pool
	cleanup []func()
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	url, adminURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_DATABASE_ADMIN_URL")
	if url == "" || adminURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()

	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	h := &harness{t: t, pool: pool, admin: admin}
	h.router = api.NewRouter(&api.Server{DB: pool})
	return h
}

type account struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	Email    string
	Token    string
	Role     string
}

// newAccount seeds a tenant, a user and an active session directly, so tests of
// (say) GET /v1/me do not depend on signup working yet.
func (h *harness) newAccount(role string) account {
	h.t.Helper()
	ctx := context.Background()

	tenantID, userID := uuid.New(), uuid.New()
	email := "user-" + userID.String() + "@example.test"

	hash, err := auth.HashPassword("test-password-123")
	if err != nil {
		h.t.Fatalf("HashPassword: %v", err)
	}
	if _, err := h.admin.Exec(ctx,
		`INSERT INTO tenants (id, name, country) VALUES ($1, $2, 'IN')`,
		tenantID, "Org "+tenantID.String()[:8]); err != nil {
		h.t.Fatalf("seed tenant: %v", err)
	}
	if _, err := h.admin.Exec(ctx,
		`INSERT INTO users (id, email, name, password_hash, email_verified)
		 VALUES ($1, $2, $3, $4, true)`,
		userID, email, "Test User", hash); err != nil {
		h.t.Fatalf("seed user: %v", err)
	}
	if _, err := h.admin.Exec(ctx,
		`INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)`,
		tenantID, userID, role); err != nil {
		h.t.Fatalf("seed membership: %v", err)
	}

	raw, tokenHash, err := auth.NewToken()
	if err != nil {
		h.t.Fatalf("NewToken: %v", err)
	}
	if _, err := h.admin.Exec(ctx,
		`INSERT INTO sessions (tenant_id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, now() + interval '24 hours')`,
		tenantID, userID, tokenHash); err != nil {
		h.t.Fatalf("seed session: %v", err)
	}

	h.t.Cleanup(func() {
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	return account{TenantID: tenantID, UserID: userID, Email: email, Token: raw, Role: role}
}

// trackTenant registers a tenant created by the code under test (e.g. signup)
// for cleanup.
func (h *harness) trackTenant(email string) {
	h.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.admin.Exec(ctx,
			`DELETE FROM tenants WHERE id IN (
			   SELECT tu.tenant_id FROM tenant_users tu
			   JOIN users u ON u.id = tu.user_id WHERE u.email = $1)`, email)
		_, _ = h.admin.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	})
}

type response struct {
	Code int
	Body []byte
}

func (r response) decode(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, into); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	r.decode(t, &body)
	if body.Error.Message == "" {
		t.Errorf("error body has no message: %s", r.Body)
	}
	return body.Error.Code
}

func (h *harness) do(method, path, token string, body any) response {
	h.t.Helper()

	var reader *bytesReader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal request: %v", err)
		}
		reader = newBytesReader(encoded)
	}

	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return response{Code: rec.Code, Body: rec.Body.Bytes()}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

// bytesReader adapts a byte slice to the io.Reader httptest.NewRequest wants,
// without pulling bytes.Reader's Seek/ReadAt surface into the test API.
type bytesReader struct {
	data []byte
	pos  int
}

func newBytesReader(data []byte) *bytesReader { return &bytesReader{data: data} }

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
