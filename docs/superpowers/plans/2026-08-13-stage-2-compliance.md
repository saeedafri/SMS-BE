# Stage 2 — Compliance Spine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sender IDs, templates and per-country regulatory registrations work end to end against the real UI, with India/DLT and US/10DLC as working regime adapters and UK/UAE proving the pattern generalises.

**Architecture:** A `Regime` interface with one implementation per country, holding that country's registration objects, field specs and validators. Handlers never branch on country — they look the regime up and ask it. Approval state is a single `ApprovalStatus` shared by senders, templates and registrations, because the operator console (Stage 9) reviews all three through one queue.

**Tech Stack:** unchanged — Go 1.26, chi, pgx, Postgres with RLS

**Spec:** `../SMS-UI/openapi.json`; `../SMS-UI/src/lib/registries/regimes.ts` is the frontend's mirror of the same registry and must stay in agreement.

## Global Constraints

- Everything from Stages 0–1 still applies.
- **The regime registry is data, not branches.** Adding a country must mean adding one file, never editing a handler. This is the PRD's "adaptability law" and the thing most likely to erode under time pressure.
- `ApprovalStatus` is exactly: `draft`, `submitted`, `pending_review`, `approved`, `rejected`, `blocked`, `expired`. New objects start `pending_review` — the frontend's compliance board keys its empty/pending states off this.
- **Compliance is owner/admin only.** `member` gets 403 with `error.code: "forbidden"`, matching Stage 1 and the frontend's area gate.
- Template variables are `{{name}}` with `[A-Za-z0-9_]+`, parsed to a distinct ordered list. Must match `../SMS-UI/src/lib/templates/variables.ts` exactly — the UI shows a live preview from the same string.
- **India CTA URLs reject public shorteners.** DLT requires the full whitelisted URL. The frontend validates this too; ours is the authoritative check.
- A registration object with `dependsOn` cannot be submitted until its dependency is `approved` (US: `tcr_campaign` needs `tcr_brand`).

## Registration objects (mirrors the frontend registry)

| Country | Key | Tier | Depends on |
|---|---|---|---|
| IN | `pe_rtm_entity` | entity | — |
| IN | `dlt_header` | sender | — |
| IN | `dlt_template` | template | — |
| US | `tcr_brand` | entity | — |
| US | `tcr_campaign` | sender | `tcr_brand` |
| GB, AE | none (stub regimes) | — | — |

## Operations in scope (11)

`listSenderIds`, `createSenderId`, `getSenderId`, `requestVoiceCall`, `confirmVoiceCode`,
`listTemplates`, `createTemplate`, `getTemplate`,
`listRegistrations`, `createRegistration`, `getRegistration`

---

### Task 1: Regime registry

**Files:**
- Create: `internal/domain/compliance/regime.go`, `regime_test.go`
- Create: `internal/domain/compliance/india.go`, `us.go`, `stubs.go`

**Interfaces:**
- Produces: `compliance.Regime` interface; `compliance.For(country string) (Regime, bool)`; `compliance.RegistrationObject{Key, Label, Tier, DependsOn string; Fields []FieldSpec}`; `compliance.FieldSpec{Key, Label, Type string; Required bool; Options []Option}`

- [ ] **Step 1: Write the failing tests** — `For("IN")` returns a non-stub regime with the three objects above in order; `For("US")` returns two with `tcr_campaign.DependsOn == "tcr_brand"`; `For("GB")` and `For("AE")` are stubs with no objects; `For("ZZ")` returns false; every object's required fields are non-empty; India rejects `https://bit.ly/x` and `not-a-url` but accepts `https://acme.example/offer`; the US regime accepts a shortener because 10DLC has no such rule.

- [ ] **Step 2: Run — expect failure.** `go test ./internal/domain/compliance/`

- [ ] **Step 3: Implement** the interface plus one file per country. `stubs.go` holds GB and AE.

- [ ] **Step 4: Run — expect pass. Commit.**

---

### Task 2: Compliance schema

**Files:**
- Create: `db/migrations/00008_compliance.sql`
- Modify: `internal/store/tenant_isolation_test.go`

**Interfaces:**
- Produces: tables `registrations`, `sender_ids`, `templates`, all tenant-scoped under RLS

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
CREATE TABLE registrations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    country          text        NOT NULL,
    object_key       text        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    external_id      text,
    fields           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- One registration per object per country per tenant: submitting the same
    -- DLT entity twice is a conflict, not a second application.
    UNIQUE (tenant_id, country, object_key)
);

CREATE TABLE sender_ids (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    header           text        NOT NULL,
    channel          text        NOT NULL,
    country          text        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    external_id      text,
    -- Channel-specific columns are nullable by design: the contract's SenderId
    -- carries WhatsApp, Email and Voice fields that are null for SMS.
    waba_id          text,
    display_name     text,
    phone_number     text,
    email_domain     text,
    from_address     text,
    from_name        text,
    caller_id_number text,
    voice_code       text,
    voice_verified   boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, header, channel, country)
);

CREATE TABLE templates (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sender_id        uuid        NOT NULL REFERENCES sender_ids(id) ON DELETE CASCADE,
    name             text        NOT NULL,
    channel          text        NOT NULL,
    country          text        NOT NULL,
    body             text,
    category         text,
    variables        text[]      NOT NULL DEFAULT '{}',
    cta_url          text,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- Same RLS shape as every other tenant-owned table.
ALTER TABLE registrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE registrations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON registrations USING (tenant_id = current_tenant_id());
ALTER TABLE sender_ids ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender_ids FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sender_ids USING (tenant_id = current_tenant_id());
ALTER TABLE templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON templates USING (tenant_id = current_tenant_id());

-- INSERT needs its own WITH CHECK: a policy's USING clause does not cover it.
-- Stage 1 learned this the hard way when signup silently could not insert.
CREATE POLICY tenant_insert ON registrations FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON sender_ids   FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON templates    FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON registrations, sender_ids, templates TO sms_app;

-- +goose Down
DROP TABLE templates;
DROP TABLE sender_ids;
DROP TABLE registrations;
```

- [ ] **Step 2: Apply and add the three tables to `TestEveryTenantScopedTableIsolates`.**

- [ ] **Step 3: Run isolation tests, prove they fail with RLS off, commit.**

---

### Task 3: Sender IDs

**Files:**
- Create: `internal/store/compliance.go`, `internal/api/senders.go`, `senders_test.go`

- [ ] **Step 1: Write the failing tests** — create returns 201 with `status: "pending_review"`; a duplicate header/channel/country → 409; an unknown country → 422; `member` → 403; list is tenant-scoped (tenant A never sees tenant B's header); get by another tenant's id → 404; voice-call returns a 6-digit code and voice-code with it → 204 then the sender reads `voiceVerification.verified: true`; a wrong code → 422.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 4: Templates

**Files:**
- Create: `internal/domain/compliance/variables.go`, `variables_test.go`
- Create: `internal/api/templates.go`, `templates_test.go`

- [ ] **Step 1: Write the failing tests for variable parsing** — `"Hi {{name}}, order {{order_id}}"` → `["name","order_id"]`; repeats collapse preserving first-seen order; `{{ spaced }}` parses to `spaced`; `{{bad-name}}`, `{{}}`, `{{unclosed` are malformed; a body with no tokens yields an empty slice, never nil.

- [ ] **Step 2: Write the failing handler tests** — create derives `variables` from the body and inherits `channel`/`country` from the sender; a template naming another tenant's sender → 422 (not 404: revealing that the id exists elsewhere is a cross-tenant leak); a malformed body → 422; a duplicate name → 409; an India CTA URL of `https://bit.ly/x` → 422 with the shortener reason; the same URL under a US sender → 201.

- [ ] **Step 3–5: Run (fail), implement, run (pass), commit.**

---

### Task 5: Registrations

**Files:**
- Create: `internal/api/registrations.go`, `registrations_test.go`

- [ ] **Step 1: Write the failing tests** — create returns 201 `pending_review` and echoes `fields`; an unknown `objectKey` → 422; a missing required field → 422 naming it; a duplicate → 409; `member` → 403; a stub regime (GB) → 422 since it has no objects; **`tcr_campaign` before `tcr_brand` is approved → 422**, and succeeds once the brand is approved; list and get are tenant-scoped.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 6: Contract validation and UI verification

- [ ] **Step 1** Add all 11 operations to `contract_test.go`'s table, success and failure shapes.
- [ ] **Step 2** Extend `scripts/e2e-check.sh`: create a sender through the API, then assert its header appears on `/compliance` and `/senders` in the rendered HTML.
- [ ] **Step 3** Run `pnpm test` and `pnpm typecheck` in SMS-UI.
- [ ] **Step 4** Verify the validator still catches drift by breaking one field.
- [ ] **Step 5** Update `ROADMAP.md` and `ARCHITECTURE.md`; commit.

---

## Stage 2 exit criteria

- [ ] `make check` green; all 11 operations contract-validated
- [ ] Isolation tests cover `registrations`, `sender_ids`, `templates`
- [ ] India CTA shortener rule enforced server-side
- [ ] `dependsOn` ordering enforced (US campaign after brand)
- [ ] Compliance screens render live data with mocks off
- [ ] SMS-UI's own tests and typecheck still pass
