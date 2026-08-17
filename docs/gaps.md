# What is not done

Read this before promising anything. The credible version of a document like this is
the one that says what is missing.

## Backend gaps

| Gap | Effect | Status |
|---|---|---|
| Public send API + delivery-receipt ingest | Customers cannot send programmatically. The pipeline beneath is built and tested; the HTTP surface is not. | **Not built** |
| Email click tracking | The Clicked tile reports 0 — truthfully, since nothing records clicks. Needs a redirect service that rewrites links. | **Not built** |
| `TenantDetail.compliance` | Always an empty list, for every tenant | **Not built** |
| Cost per conversion | Reports 0 | Needs conversion tracking |
| Journey run telemetry | Funnel counts derived from suppression data, not recorded per run | Needs a decision |
| API-key and MFA activity events | Only sign-ins appear in user activity | Partial |
| **All 151 contract operations** | — | **Implemented** |

## Browser test status

**194 of 256 pass.** The remaining failures group as:

**Frontend-side, measured.** Filter links do not navigate — on `/analytics`,
`/developer/logs` and verify analytics. Run three times, 12 of 12 links failed;
direct navigation to the link's own href works every time and the server returns
200 for it. Reproduction: `SMS-UI/e2e-probe-filter-links.mjs`.

This is the largest remaining cluster and the one thing we deliberately did not
fix ourselves: it is a bug in a frontend component, and guessing at the fix
would have been worse than describing it precisely. See
[For the frontend team](ui-team.md).

**Run-to-run flakiness.** Measured across four consecutive runs with no code
changes between them: 79 tests failed in all four, and **15 flipped**. So a
single run's number carries roughly ±7 of noise, and only differences larger
than that mean anything.

**Modelling divergences — need a joint decision.**

- *Campaign progress.* The mock derives state from `sendStartedAt` + `sendDurationMs`,
  simulating a trajectory that advances with wall-clock time. The backend stores a
  status. Specs asserting mid-flight progress cannot be satisfied by a stored status.
- *Journey funnel.* A spec expects `exitedSuppressed` to be exactly 2. The backend
  derives it by counting genuinely suppressed list members — with the fixture data that
  is 0.

  **We are not hard-coding these.** A funnel reporting 2 when the data says 0 is worse
  than a failing test.

**Mock-only behaviour a real backend must refuse.** One spec expects an anonymous
visitor to a protected route to be handed a session. That is an auth bypass.

**Frontend specs we corrected ourselves.** Specs that never signed in, mock-only
UI, operator credentials on the tenant login page, and ids that were not valid
UUIDs. All fixed in their repo, uncommitted, each with a comment explaining why —
see [Handoff: frontend changes](handoff-ui.md).

## What can be claimed safely

> All 151 contract operations are implemented. The backend passes 35 adversarial
> security checks including cross-tenant isolation and append-only money guarantees, 25
> operator-console checks, and enum conformance across 23 endpoints. It sustains 38,668
> messages per second and recovers from dependency loss without an operator. 161 of 256
> browser tests pass; the remaining failures are documented and attributed, and none is
> an unimplemented operation.

What cannot be claimed: *"everything is done and tested."*
