# Roadmap — stage by stage

One plan document per stage, in `docs/superpowers/plans/`. Each stage ends with a **live
end-to-end test against the real UI** with `NEXT_PUBLIC_USE_MOCKS=false`, so we always know
exactly which domains are real and which are still mocked.

**Rule for every stage**: the UI is the acceptance test. If a screen doesn't work against our
backend, the stage isn't done — regardless of what our own tests say.

| # | Stage | Contract ops | UI screens that must work live | Plan |
|---|---|---|---|---|
| **0** | **Foundation** | 0 (+`/healthz`) | none — infra only | `2026-08-13-stage-0-foundation.md` |
| 1 | Identity & tenancy | 21 | login, signup, MFA, /me, team, sessions, profile | — |
| 2 | Compliance spine | 11 | sender IDs, templates, registrations | — |
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

## Open decisions carried forward

- VPS specs (blocks production sizing, not local development)
- Launch countries/currencies → shapes Stage 2
- Payment gateway → shapes Stage 3
- `/v1/dev/*` endpoints: implement for sandbox, or gate off in UI → decide by Stage 1
- `src/lib/api/me.ts` client-side fetch has no auth header → must be fixed before Stage 1's
  UI acceptance test can pass
