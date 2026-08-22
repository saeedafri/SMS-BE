package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"
)

// harness is a live server backed by the real test database. These are
// integration tests on purpose: the thing most worth proving about identity is
// that RLS, the SECURITY DEFINER resolution functions, and the handlers agree
// with each other, and a mocked store would prove none of that.
type harness struct {
	t      *testing.T
	router http.Handler
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	// logs captures the server's structured output so tests can read tokens
	// the API deliberately never returns in a response.
	logs *bytes.Buffer
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

	// The operator console's pool, carrying app.operator=on.
	//
	// Without it every /v1/operator route in these tests reads through the plain
	// tenant pool, where row-level security correctly refuses cross-tenant rows —
	// so an operator handler that works in production returns an empty queue or a
	// 404 here, and looks like a bug in the handler rather than a missing pool.
	operator, err := store.OpenOperatorPool(ctx, url)
	if err != nil {
		t.Fatalf("open operator pool: %v", err)
	}
	t.Cleanup(operator.Close)

	logs := &bytes.Buffer{}
	h := &harness{t: t, pool: pool, admin: admin, logs: logs}
	h.router = api.NewRouter(&api.Server{
		DB:         pool,
		OperatorDB: operator,
		AdminDB:    admin,
		Logger:     slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
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

// setEmailVerified flips the flag directly, so a test can start from an
// unverified account without going through signup.
func (h *harness) setEmailVerified(userID uuid.UUID, verified bool) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE users SET email_verified = $1 WHERE id = $2`, verified, userID); err != nil {
		h.t.Fatalf("set email_verified: %v", err)
	}
}

// lastIssuedToken pulls a token out of the server's own log. Email delivery is
// not wired up yet, and the token is deliberately never returned in a response
// — so reading the log is how a test completes the flow, and it exercises the
// real code path rather than a test-only escape hatch.
func (h *harness) lastIssuedToken(t *testing.T, kind string) string {
	t.Helper()

	var found string
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Msg   string `json:"msg"`
			Kind  string `json:"kind"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Msg == "account token issued" && entry.Kind == kind {
			found = entry.Token
		}
	}
	if found == "" {
		t.Fatalf("no %s token found in the server log:\n%s", kind, h.logs.String())
	}
	return found
}

// enableMfa takes an account all the way through enrolment and returns the
// secret, for tests that need MFA already on.
func (h *harness) enableMfa(t *testing.T, acct account) string {
	t.Helper()

	res := h.do(http.MethodPost, "/v1/auth/mfa/enroll", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("enroll: status = %d; body = %s", res.Code, res.Body)
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	res.decode(t, &enrollment)

	code, err := totp.GenerateCodeCustom(enrollment.Secret, time.Now().UTC(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: 6,
	})
	if err != nil {
		t.Fatalf("generate totp: %v", err)
	}
	confirm := h.do(http.MethodPost, "/v1/auth/mfa/enroll/confirm", acct.Token,
		map[string]string{"code": code})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm enrolment: status = %d; body = %s", confirm.Code, confirm.Body)
	}
	return enrollment.Secret
}

// doWithHeaders is do() plus extra request headers, for operations whose
// contract puts meaning in a header — Idempotency-Key, notably.
func (h *harness) doWithHeaders(method, path, token string, body any,
	headers map[string]string) response {
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
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return response{Code: rec.Code, Body: rec.Body.Bytes()}
}

// operatorToken signs in as platform staff.
//
// Operators are a separate identity system with their own table and endpoint, so
// there is no way to derive one from newAccount — a tenant token is refused on
// every /v1/operator route, which is the property those routes exist to have.
func (h *harness) operatorToken() string {
	h.t.Helper()
	const email, password = "harness-ops@relay.internal", "harness-ops-password"
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatalf("hash operator password: %v", err)
	}
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO operator_users (email, name, password_hash, role)
		VALUES ($1, 'Harness Ops', $2, 'admin')
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash`,
		email, hash); err != nil {
		h.t.Fatalf("seed operator: %v", err)
	}
	res := h.do(http.MethodPost, "/v1/operator/login", "",
		map[string]any{"email": email, "password": password})
	if res.Code != http.StatusOK {
		h.t.Fatalf("operator login = %d\n%s", res.Code, res.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	res.decode(h.t, &out)
	return out.Token
}

// createRegistration submits one compliance registration and returns its id.
func (h *harness) createRegistration(token string) string {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/registrations", token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
	})
	if res.Code != http.StatusCreated && res.Code != http.StatusOK {
		h.t.Fatalf("create registration = %d\n%s", res.Code, res.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	res.decode(h.t, &out)
	return out.ID
}
