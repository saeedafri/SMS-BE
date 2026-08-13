# Roadmap — stage by stage

One plan document per stage, in `docs/superpowers/plans/`. Each stage ends with a **live
end-to-end test against the real UI** with `NEXT_PUBLIC_USE_MOCKS=false`, so we always know
exactly which domains are real and which are still mocked.

**Rule for every stage**: the UI is the acceptance test. If a screen doesn't work against our
backend, the stage isn't done — regardless of what our own tests say.

| # | Stage | Contract ops | UI screens that must work live | Plan |
|---|---|---|---|---|
| **0** | ✅ **Foundation** | 0 (+`/healthz`) | none — infra only | `2026-08-13-stage-0-foundation.md` |
| 1 | ✅ Identity & tenancy | 21 | login, signup, MFA, /me, team, sessions, profile | `2026-08-13-stage-1-identity.md` |
| 2 | ✅ Compliance spine | 11 | sender IDs, templates, registrations | `2026-08-13-stage-2-compliance.md` |
| 3 | ✅ Money | 14 | wallet, ledger, top-up, invoices, pricing | `2026-08-13-stage-3-money.md` |
| 4 | ✅ Audience | 11 | contacts, lists, CSV import, suppressions | (built inline) |
| 5 | 🟡 **Data plane** | 1 + pipeline | message logs render live from ClickHouse | (built inline) |
| 6 | Campaigns | 5 | campaign list, wizard, live monitoring | — |
| 7 | Developer surface | 17 | API keys, webhooks, IP allowlist, rate limits | — |
| 8 | Analytics | 7 | analytics dashboards, reports, alerts, retention | — |
| 9 | Operator console | 34 | every operator screen | — |
| 10 | Verify / OTP | 8 | verify services, attempts, analytics | — |
| 11 | Support, inbox, journeys | 18 | tickets, conversations, automation | — |
| 12 | Hardening & deploy | — | load test, chaos, security pass, Hostinger | — |

Ordering is dependency-driven: identity before everything, compliance before sending, money
before campaigns, data plane before campaigns can mean anything.

## Definition of done, every stage

1. All contract tests for the stage's operations pass against `openapi.json`.
2. Cross-tenant isolation tests pass (attempt access as tenant B, get nothing).
3. The UI screens listed above work against the local backend with mocks **off**.
4. `make check` is green: build, vet, lint, unit, contract, integration.
5. Diagrams in `ARCHITECTURE.md` updated if the stage changed a flow.

## Status

- **Stage 0** complete: 8/8 tasks, RLS proven, contract surface generated.
- **Stage 1** complete: 21/21 contract operations + 3 dashboard-layout endpoints.
- **Stage 2** complete: 11/11 contract operations. India/DLT and US/10DLC are working
  regime adapters; GB/AE stubs prove the pattern.
- **Stage 3** complete: 14/14 contract operations. Append-only ledger, balance invariant
  proven under concurrency, GSM-7/UCS-2 segment arithmetic.
- **Stage 4** complete: 11/11 contract operations. Idempotent CSV import, global suppression
  honoured at import time, phone normalisation matching the UI exactly.
- **Totals**: 60 of 151 contract operations live. 230+ backend tests, 65 contract-validated
  request/response pairs, 54/54 end-to-end checks against the real UI. SMS-UI's own 2037
  tests and typecheck still green.

## Stage 5 status — core built, remainder scoped

**Done**: message state machine, send gate, sandbox connector, send pipeline with
hold/submit/settle, delivery-report application, ClickHouse schema (daily partitions +
permanent rollups), and `GET /v1/messages` serving live from ClickHouse.

Proven end to end: delivered charges once; undelivered refunds automatically; a carrier
rejection releases the hold immediately; a replayed receipt cannot refund twice; a gate refusal
moves no money; a report for an unknown message is dropped.

**Remaining for Stage 5**:
- `openapi.public.json` — the send API, DLR/MO ingest and signed webhook payloads. The pipeline
  they drive exists and is tested; what is missing is the public HTTP surface and its spec.
- The reconciler that expires messages stuck in `submitted`/`accepted` past their DLR window.
  The sandbox already produces that case on demand (numbers ending `003`).
- Batched worker + River job queue for campaign-scale fan-out. Single sends work today;
  Stage 6 needs the batch path.

## Original Stage 5 readiness notes

The data plane is the next stage and the biggest one. Everything it depends on now exists:
suppression checks (`store.IsSuppressed`), balance and overdraft refusal
(`store.AppendLedgerEntry`), segment costing (`billing.SegmentCount`), approved senders and
templates, and ClickHouse running locally. Stage 5 also authors `openapi.public.json` — the
send API, DLR ingest and webhook payloads the frontend contract does not cover.

## Open decisions carried forward

- **VPS specs** (blocks production sizing, not local development)
- **Launch countries** — Stage 2 shipped IN + US as working regimes and GB + AE as stubs,
  per the PRD's reference-adapter plan. If your launch set differs, say so and the stubs
  become real adapters (a file each, no handler changes).
- **Payment gateway** — Stage 3 shipped a `PaymentGateway` interface whose only implementation
  records a manual capture (correct for bank-transfer and invoice-paid customers). Adding
  Razorpay/Stripe is one new file implementing `Capture` plus a config switch. Tell me which
  provider when you want cards.
- `/v1/dev/*` endpoints: ten endpoints the UI's dev tooling calls that are absent from
  `openapi.json`. Implement for sandbox with a production kill-switch, or gate the tooling off.
  Not yet blocking anything.

## Resolved

- ~~`src/lib/api/me.ts` client-side fetch has no auth header~~ — fixed in Stage 1: added
  `src/app/api/me/route.ts` and pointed the hook at it.
- ~~ClickHouse install route~~ — resolved: the official curl installer sets no quarantine
  attribute, so Gatekeeper never blocks it and nothing is bypassed. Running locally, both
  databases created. See `LOCAL_DEV.md`.
- ~~Email delivery for verification/reset tokens~~ — tokens are logged; real delivery is
  Stage 3's payment/notification work. Tokens are deliberately never returned in a response.
