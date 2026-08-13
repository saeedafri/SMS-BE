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
| 3 | Money | 15 | wallet, ledger, top-up, invoices, pricing | — |
| 4 | Audience | 11 | contacts, lists, CSV import, suppressions | — |
| 5 | **Data plane** | +public spec | message logs; first real delivered message | — |
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
- **Totals**: 35 of 151 contract operations live. 154 backend tests, 43 contract-validated
  request/response pairs, 37/37 end-to-end checks against the real UI. SMS-UI's own 2037
  tests and typecheck still green.

## Open decisions carried forward

- **VPS specs** (blocks production sizing, not local development)
- **Launch countries** — Stage 2 shipped IN + US as working regimes and GB + AE as stubs,
  per the PRD's reference-adapter plan. If your launch set differs, say so and the stubs
  become real adapters (a file each, no handler changes).
- Payment gateway → shapes Stage 3
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
