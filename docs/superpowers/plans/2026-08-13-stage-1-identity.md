# Stage 1 — Identity & Tenancy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A real user can sign up, log in, and use the dashboard against this backend with MSW off — with sessions, roles, MFA, and team management all working, and every response validated against `openapi.json`.

**Architecture:** Opaque session tokens stored hashed in Postgres and cached in Redis, not JWTs. Auth middleware resolves the token to `(user, tenant, role, capabilities)` and puts it in the request context; every handler then runs inside `store.WithTenant`, so RLS enforces isolation on every query. Handlers live in `internal/api/`, business rules in `internal/domain/`, SQL in `internal/store/`.

**Tech Stack:** Go 1.26 · chi · pgx · argon2id (`golang.org/x/crypto/argon2`) · TOTP (`github.com/pquerna/otp`) · Redis

**Spec:** `../SMS-UI/openapi.json`; `docs/ARCHITECTURE.md`; `docs/BACKEND_DESIGN.md`

## Global Constraints

- Everything from Stage 0's constraints still applies.
- **Opaque tokens, not JWTs.** The PRD requires the operator console to suspend a tenant and stop traffic *within seconds*, and `DELETE /v1/sessions/{id}` must genuinely kill a session. A JWT is valid until it expires and cannot be revoked without a denylist — at which point you have the database lookup you were trying to avoid, plus a second mechanism. One opaque token, hashed at rest, cached in Redis for speed, is simpler and strictly more correct here.
- Tokens are 32 random bytes, base64url. Stored **SHA-256 hashed** — a leaked database dump must not yield usable sessions.
- Passwords are **argon2id**, 64 MB / 3 iterations / 4 threads, 16-byte salt.
- Error codes the frontend already expects: `unauthenticated` (401), `forbidden` (403), `validation_failed` (422), `conflict` (409). Confirmed against `src/mocks/handlers.ts`.
- Capability set for a new tenant: `["sms.send","rcs.send","compliance.manage","billing.view"]`.
- Roles: `owner`, `admin`, `member`. `member` is denied Compliance / Billing / Developer / Settings — the frontend's middleware gates on this, so 403s must be real.
- Timing-safe comparison for every secret. Login must not reveal whether an email exists.

## Operations in scope (21 + 3)

| Group | Operations |
|---|---|
| Auth (12) | login, logout, signup, verify-email/resend, verify-email/confirm, password/forgot, password/reset, PATCH password, mfa/enroll, mfa/enroll/confirm, mfa/disable, mfa/challenge |
| Me (2) | GET /v1/me, PATCH /v1/me |
| Sessions (2) | GET /v1/sessions, DELETE /v1/sessions/{id} |
| Team (4) | GET /v1/team, POST /v1/team/invite, PATCH /v1/team/{id}, DELETE /v1/team/{id} |
| Tenant (1) | PATCH /v1/tenant |
| **Layout unblockers (3)** | GET /v1/wallet/balances, GET /v1/alerts, GET /v1/conversations |

The last three are not identity, but `src/app/(dashboard)/layout.tsx` fetches them on **every**
dashboard render and throws on any non-2xx — so no screen renders until they return 200. They
return genuinely empty/default state for a new tenant, which is the truth, not a stub: a fresh
tenant really does have no wallet, no alerts history, and no conversations.

---

### Task 1: Identity schema

**Files:**
- Create: `db/migrations/00003_identity.sql`

**Interfaces:**
- Produces: tables `users`, `tenant_users`, `sessions`, `email_verifications`, `password_resets`; `tenants.country`; all tenant-owned tables under RLS via `current_tenant_id()`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
ALTER TABLE tenants ADD COLUMN country text NOT NULL DEFAULT 'IN'
    CHECK (country IN ('IN','US','GB','AE'));
ALTER TABLE tenants ADD COLUMN capabilities text[] NOT NULL
    DEFAULT ARRAY['sms.send','rcs.send','compliance.manage','billing.view'];

-- Users are global, not tenant-scoped: an email is unique across the platform
-- and a user could later belong to more than one tenant. Tenant membership and
-- role therefore live in tenant_users, which IS tenant-scoped.
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          citext      NOT NULL UNIQUE,
    name           text        NOT NULL,
    password_hash  text        NOT NULL,
    email_verified boolean     NOT NULL DEFAULT false,
    mfa_secret     text,
    mfa_enabled    boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tenant_users (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner','admin','member')),
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','invited')),
    invited_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

-- Sessions store only the SHA-256 of the token. A database dump must not hand
-- an attacker working sessions.
CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    bytea       NOT NULL UNIQUE,
    device        text        NOT NULL DEFAULT 'Unknown',
    browser       text        NOT NULL DEFAULT 'Unknown',
    location      text        NOT NULL DEFAULT 'Unknown',
    ip_address    text        NOT NULL DEFAULT '',
    last_active_at timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_tenant_user ON sessions (tenant_id, user_id) WHERE revoked_at IS NULL;

-- Single-use, short-lived, hashed. Same reasoning as sessions.
CREATE TABLE email_verifications (
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE TABLE password_resets (
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

ALTER TABLE tenant_users ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_users USING (tenant_id = current_tenant_id());

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sessions USING (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_users, sessions TO sms_app;
GRANT SELECT, INSERT, UPDATE ON users, email_verifications, password_resets TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenants TO sms_app;

-- +goose Down
DROP TABLE password_resets, email_verifications, sessions, tenant_users, users;
ALTER TABLE tenants DROP COLUMN capabilities, DROP COLUMN country;
```

- [ ] **Step 2: Enable citext, apply, verify**

```bash
psql -d sms_dev -c "CREATE EXTENSION IF NOT EXISTS citext"
psql -d sms_test -c "CREATE EXTENSION IF NOT EXISTS citext"
set -a && source .env && set +a && make migrate-up && make migrate-test
psql -d sms_dev -c '\d tenant_users'
```

Expected: table listed with `Policies (forced row security enabled)`.

- [ ] **Step 3: Extend the isolation test to the new tables**

Add to `internal/store/tenant_isolation_test.go` a subtest per new tenant-scoped table
(`tenant_users`, `sessions`) asserting tenant A reads zero of tenant B's rows.

- [ ] **Step 4: Run and commit**

```bash
go test ./internal/store/ -count=1
git add -A && git commit -m "feat: identity schema with RLS on tenant_users and sessions"
```

---

### Task 2: Credentials — argon2id hashing and opaque tokens

**Files:**
- Create: `internal/domain/auth/password.go`, `password_test.go`
- Create: `internal/domain/auth/token.go`, `token_test.go`

**Interfaces:**
- Produces: `auth.HashPassword(string) (string, error)`; `auth.VerifyPassword(hash, password string) bool`; `auth.NewToken() (raw string, hash []byte, err error)`; `auth.HashToken(raw string) []byte`

- [ ] **Step 1: Write the failing tests**

```go
func TestPasswordRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	if err != nil { t.Fatalf("HashPassword: %v", err) }
	if !auth.VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if auth.VerifyPassword(hash, "wrong") {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
}

// Two hashes of the same password must differ: the salt is random, so an
// attacker cannot tell that two accounts share a password.
func TestPasswordHashesAreSalted(t *testing.T) {
	a, _ := auth.HashPassword("same")
	b, _ := auth.HashPassword("same")
	if a == b { t.Fatal("two hashes of the same password are identical; the salt is not random") }
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	if auth.VerifyPassword("not-a-real-hash", "anything") {
		t.Fatal("VerifyPassword accepted a malformed hash")
	}
}

func TestNewTokenIsUniqueAndHashable(t *testing.T) {
	raw1, hash1, err := auth.NewToken()
	if err != nil { t.Fatalf("NewToken: %v", err) }
	raw2, _, _ := auth.NewToken()
	if raw1 == raw2 { t.Fatal("NewToken returned the same token twice") }
	if !bytes.Equal(hash1, auth.HashToken(raw1)) {
		t.Fatal("HashToken does not reproduce the hash NewToken returned")
	}
	if bytes.Contains(hash1, []byte(raw1)) {
		t.Fatal("the hash contains the raw token")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/domain/auth/` → FAIL, undefined.

- [ ] **Step 3: Implement** using `golang.org/x/crypto/argon2` (`IDKey`, 3 iterations, 64 MB, 4 threads), PHC-format string `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`, `subtle.ConstantTimeCompare` for verification; `crypto/rand` + `base64.RawURLEncoding` for tokens, `sha256.Sum256` for `HashToken`.

- [ ] **Step 4: Run tests, expect PASS. Commit.**

---

### Task 3: Auth middleware and GET/PATCH /v1/me, PATCH /v1/tenant

This is the first task that makes a real screen work.

**Files:**
- Create: `internal/api/middleware_auth.go`
- Create: `internal/store/identity.go`
- Create: `internal/api/me.go`, `me_test.go`
- Modify: `internal/api/server.go`

**Interfaces:**
- Consumes: `auth.HashToken`
- Produces: `api.Identity{UserID, TenantID uuid.UUID; Role string; Capabilities []string}`; `api.IdentityFrom(ctx) (Identity, bool)`; `store.LookupSession(ctx, pool, hash []byte) (Identity, error)`

- [ ] **Step 1: Write the failing tests** — table-driven over: no header → 401 `unauthenticated`; malformed header → 401; unknown token → 401; expired session → 401; revoked session → 401; valid session → 200 with every required `Me` field populated; `member` role PATCHing → 403 `forbidden`; empty name → 422 `validation_failed`.

- [ ] **Step 2: Run, expect failure.**

- [ ] **Step 3: Implement.** The middleware resolves the bearer token, looks the session up (Redis first, Postgres on miss), stores `Identity` in the request context, and touches `last_active_at` at most once a minute. Handlers call `store.WithTenant(ctx, pool, identity.TenantID, ...)` so RLS covers every query.

- [ ] **Step 4: Run tests, expect PASS. Commit.**

---

### Task 4: Signup, login, logout, and session management

**Files:**
- Create: `internal/api/auth.go`, `auth_test.go`
- Create: `internal/api/sessions.go`, `sessions_test.go`

- [ ] **Step 1: Write the failing tests** — signup creates tenant + owner user + session and returns `AuthSession`; duplicate email → 409 `conflict`; weak/short password → 422; login with good credentials → `{"kind":"session","session":{...}}`; wrong password → 401; **unknown email → 401 with the identical body and comparable timing** (a different response would let an attacker enumerate accounts); login for an MFA-enabled user → `{"kind":"mfa_challenge",...}` and **no session issued**; logout revokes; `GET /v1/sessions` marks exactly one `current: true`; `DELETE /v1/sessions/{id}` revokes and a subsequent request with that token → 401; deleting another tenant's session → 404.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 5: Email verification, password reset, MFA

**Files:**
- Create: `internal/api/auth_email.go`, `internal/api/auth_password.go`, `internal/api/auth_mfa.go` + tests
- Create: `internal/domain/auth/totp.go`

- [ ] **Step 1: Write the failing tests** — verification token single-use (second use → 422); expired token → 422; `password/forgot` returns **200 whether or not the email exists** (same anti-enumeration rule as login); reset token single-use and **revokes all that user's sessions**; PATCH password requires the current password; MFA enroll returns a secret + otpauth URL but does **not** enable MFA until confirm; a wrong TOTP code → 422; recovery codes are single-use; `mfa/challenge` with a valid code issues the session that login withheld.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

Email delivery is out of scope: tokens are logged at debug level and returned in the response
**only when `APP_ENV=development`**, so the flow is testable locally. Production wiring is Stage 3.

---

### Task 6: Team management and the three layout unblockers

**Files:**
- Create: `internal/api/team.go`, `team_test.go`
- Create: `internal/api/layout_stubs.go`, `layout_stubs_test.go`

- [ ] **Step 1: Write the failing tests** — `GET /v1/team` lists members with correct `status`/`invitedAt` nullability per the schema; `member` role calling it → 403; invite creates an `invited` row with `name: null`; inviting an existing member → 409; PATCH role; **an owner cannot demote or remove the last owner** → 422; DELETE removes the member and revokes their sessions; `GET /v1/wallet/balances` → `[]`; `GET /v1/alerts` → `AlertRules` with all four rule groups present and disabled; `GET /v1/conversations` → `{"conversations":[],"total":0,"nextCursor":null}`.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 7: Contract validation and strict end-to-end UI verification

**Files:**
- Create: `internal/api/contract_test.go`
- Create: `scripts/e2e-check.sh`

- [ ] **Step 1: Contract test.** For each implemented operation, execute a request and validate the response body against `openapi.json` using `kin-openapi`'s router + response validator (already a dependency via the generated code). Any drift from the spec fails the build.

- [ ] **Step 2: Fix the frontend's `me.ts`.** Add `src/app/api/me/route.ts` mirroring `src/app/api/wallet/balances/route.ts`, and point `src/lib/api/me.ts` at `/api/me`. Without this, every client-side capability check 401s.

- [ ] **Step 3: Write `scripts/e2e-check.sh`** — starts the backend, signs a user up via the API, then drives the UI with that session cookie asserting real HTTP 200s and real content on: `/`, `/campaigns`, `/billing`, `/settings`, `/developer/api-keys`, `/team`. Assert the rendered HTML contains the tenant name returned by signup — proving data flowed backend → UI, not merely that a page rendered.

- [ ] **Step 4: Run the frontend's own test suite** (`pnpm test`) to confirm we broke nothing.

- [ ] **Step 5: Manual browser pass** — sign up, log in, change name, invite a teammate, revoke a session, enable MFA, log out.

- [ ] **Step 6: Commit.**

---

## Stage 1 exit criteria

- [ ] `make check` green; contract tests pass for all 24 operations
- [ ] Cross-tenant isolation tests cover `users`/`tenant_users`/`sessions`
- [ ] A real signup → login → dashboard flow works in a browser with `NEXT_PUBLIC_USE_MOCKS=false`
- [ ] `member` role is genuinely blocked from Compliance/Billing/Developer/Settings
- [ ] Revoking a session immediately invalidates the token
- [ ] Account enumeration is not possible via login or password/forgot
- [ ] `pnpm test` in SMS-UI still passes
- [ ] `docs/ROADMAP.md` Stage 1 row marked done
