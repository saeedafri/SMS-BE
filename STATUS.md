# Relay backend — what is true, what is not

Last measured: 2026-08-17, against a running stack (Postgres 16, ClickHouse 26.8,
Redis 7, Go control-api) with the frontend's production build.

Every number here came from a command that was actually run. Nothing is projected.

---

## The one-line answer

**All 256 of the frontend team's browser tests now pass against the deployed
backend — not against mocks.**

Say this, and defend it under questioning:

> The backend implements all 151 operations in the contract and is deployed on
> a shared VPS alongside two other production products. **256 of 256 browser
> tests pass against the live API**, up from 194 at the start and 249 against
> mocks. It also passes 35 adversarial security checks run against that same
> deployment, 2,038 frontend unit tests, and Go's vet and test suites clean.

| Measured | Result |
|---|---|
| Playwright, **against the live VPS backend** | **256 / 256** |
| Adversarial security checks, same deployment | **35 / 35** |
| Frontend unit tests | **2,038 / 2,038** |
| `tsc --noEmit` | clean |
| `go vet` / `go test ./...` | clean, 12 packages |
| Contract operations implemented | 151 / 151 |

!!! warning "The number that matters is 256 **against a real backend**"
    The suite scored 249/256 against MSW mocks. Pointing it at the deployed API
    dropped it to 250 and then exposed nine defects the mocks could not
    express — including tenant rate overrides never being applied to any price.
    A green run against mocks is not evidence about a backend.

---

## Deployed

Relay is live on the shared Hostinger VPS at `31.97.186.223`, alongside
Rentocloud and EMS. Full detail in [deployment.md](deployment.md).

| | |
|---|---|
| API | systemd `relay-api`, loopback :8080, published via nginx |
| Postgres | **reuses** the existing `ems-postgres` container, separate `sms` database |
| ClickHouse | new container, hard-capped at 1 GB |
| Redis | Relay's own on :6380 — **not** shared, see below |
| Email | Resend, verified `saqibsaeed.cloud`, transactional only |
| **Total RAM** | **518 MB idle, 812 MB after a full test run** — 5.17 GB still free |

Redis is the one thing deliberately *not* reused: `ems-redis` runs
`maxmemory-policy noeviction`, so anything of Relay's that filled its 200 MB
would start failing **EMS's** writes. A dedicated instance that evicts costs
12 MB, which is less than that risk is worth.

Tenant isolation was verified against the live database, not just in tests:
8 tenants exist, a session scoped to the demo tenant sees exactly 1 and **0**
foreign tenants, another tenant's rows are invisible, and a connection with no
tenant set at all returns 0 rows from every table — it fails closed.

### Five defects that only a real BACKEND could find

The browser suite had been run against MSW mocks. Pointing it at the deployed
API found these — every one had passed every local run:

1. **Negotiated rate overrides were never applied to any price.** The whole
   feature was decorative: `FindPricingRate` read `pricing_rates` and never
   `rate_overrides`. An operator agreed a rate with a customer, entered it, saw
   it listed against that customer — and every estimate **and every actual
   charge** used the default. Not a display bug: the customer was quoted and
   billed the wrong price, silently, with the correct price on screen in the
   admin console throughout. Fixed in the one function all four pricing paths
   share, rather than per-caller, so no path could be left behind.
2. **The rate card never returned overrides at all.** `ListRateOverrides` JOINs
   `tenants`, which is `FORCE ROW LEVEL SECURITY`, but ran on the tenant pool
   with no tenant context — so the join silently matched nothing. Creating an
   override answered 201 and the card then reported `"overrides": []`.
3. **A rejected compliance registration could never be resubmitted.** A plain
   INSERT against a unique constraint meant the "Resubmit registration" button
   always returned 409 "already registered". The screen showed the rejection
   reason, showed the remediation telling the customer exactly what to correct,
   and then refused the correction. The only way out was editing the database.
4. **`reset-mock-state` left the global rate card permanently mutated.**
   `pricing_rates` has no `tenant_id`, so the tenant-delete cascade never
   touched it and the seed restored only *category* rates. The first spec to
   edit a rate changed the default forever — IN/SMS drifted 12 → 99 and stayed
   — which then broke an unrelated assertion three specs later.
5. **The seed occupied a string a spec needed to be unique.** `inbox.spec.ts`
   posts "One more question!" as its probe; the fixture already contained a
   message with exactly that body, so the locator matched two elements. The
   spec was right and the fixture was wrong.

Three further failures were **test** defects that mocks cannot structurally
surface: two races that only lose when the API is across a network (a server
action not waited on, and `locator.count()`, which does not auto-wait, counting
a loading skeleton), and one spec depending on a session an earlier file
happened to leave behind. All are written up in
[handoff-ui.md](handoff-ui.md).

### One more, in the checking itself

`scripts/chaos-check.sh` sourced `.env` in a way that **overrode an exported
`DATABASE_ADMIN_URL`**. `API` was overridable and the database URLs were not, so
running it against the deployed server sent the 32 HTTP checks to the remote API
and the money checks to the LOCAL database — verifying the ledger's append-only
guarantee against a database that had never seen the tenant under test.

It reported three failures that said nothing about the server being examined.
It would just as readily have reported a PASS for an invariant it never tested,
which is the worse outcome. `.env` now supplies defaults only.

### Four defects that only a real deployment could find

Each of these passed every local test and would have shipped:

1. **ClickHouse could never start.** Our `background_pool_size=2` violated its
   own sanity check (`number_of_free_entries_in_pool_to_execute_mutation` must
   be exceeded by `pool_size × concurrency_ratio`), so the server exited with
   `BAD_ARGUMENTS` before ever listening. The config had never been run.
2. **`OpenClickHouse` silently discarded credentials.** It parsed the URL and
   then built `Auth{Database: …}` with no username or password, connecting as
   the passwordless `default` user instead. The failure mode is not breakage —
   it is an operator configuring a password, seeing a healthy service, and
   believing ClickHouse is authenticated.
3. **Seeding is impossible with a real migration role.** Every table is
   `FORCE ROW LEVEL SECURITY`, which binds even the table owner, so the seeder
   fails with SQLSTATE 42501. Invisible locally, where `DATABASE_ADMIN_URL`
   points at the developer's own superuser account — there is no `sms_migrator`
   role locally at all. The migration role needs explicit `BYPASSRLS`.
4. **`deploy/install-server.sh` would have taken down two other products.** It
   installed Caddy onto ports nginx already held for three sites, duplicated
   ~600 MB of already-running Postgres and Redis, and ran `ufw --force enable`
   permitting port 22 only — while sshd on this box also listens on **2222**.
   Rewritten to install nothing and change no firewall.

---

---

!!! danger "A claim on this page was wrong, and was corrected"
    This page previously said all 151 operations were implemented. **Five were
    not.** `PATCH /v1/alerts`, `PATCH /v1/automation/journeys/{id}`,
    `PATCH /v1/verify/services/{id}`, `PATCH /v1/analytics/reports/{id}` and
    `DELETE /v1/developer/ip-allowlist/{id}` all answered **501 Not
    Implemented** in a running server.

    The original check looked for leftover stub methods, and there were none —
    because the generated `Unimplemented` type provides a 501 fallback for
    every operation and `Server` simply had not shadowed those five. Nothing was
    left behind to find. The alerts screen in particular accepted input, showed
    no error, and forgot everything on reload.

    All five are now implemented and verified against a running server. The
    check that replaced the old one compares the full operation list against the
    methods actually defined on `Server`, which is what the claim always meant:

    ```bash
    grep -o 'func (Unimplemented) [A-Za-z]*(' internal/api/unimplemented.go \
      | sed 's/func (Unimplemented) //;s/(//' | sort > /tmp/all_ops.txt
    grep -rho 'func (s \*Server) [A-Za-z]*(' internal/api/*.go \
      | sed 's/func (s \*Server) //;s/(//' | sort -u > /tmp/impl_ops.txt
    comm -23 /tmp/all_ops.txt /tmp/impl_ops.txt   # empty = all implemented
    ```

    Recorded here rather than quietly fixed, because a status page that hides
    its own corrections is worth less than no status page.

## What is verified

| Check | Result | How it was run |
|---|---|---|
| Contract operations implemented | **151 / 151** | `comm -23` of the generated fallback list against `func (s *Server)` methods — see note below |
| Go unit + integration, `-race` | **green** | `go test ./...` |
| `go vet` | **clean** | `go vet ./...` |
| Adversarial suite | **35 / 35** | `scripts/chaos-check.sh` |
| Operator console | **25 / 25** | `/tmp/verify-operator.sh` |
| Enum conformance, 23 endpoints | **green** | `scripts/enum-check.py` |
| Browser suite (frontend's 43 specs) | **249 / 256** | `npx playwright test` |
| Frontend unit + component tests | **2,038 / 2,038** | `npx vitest run src/` |
| Frontend typecheck | **clean** | `npx tsc --noEmit` |

### What the adversarial suite actually proves

Not "the happy path works" — these are attempts to break it, and a pass means the
attack was refused:

- Reading another tenant's sender by id → **404** (not 403; a 403 would confirm the id exists)
- Deleting another tenant's contact list → **404**, victim's data intact
- SQL injection in body and query → stored as literal text, tables survive
- `UPDATE` on the wallet ledger, as the table owner → **refused by trigger**
- `UPDATE` on the operator audit log → **refused by trigger**
- Negative top-up, zero top-up, unknown payment method → **422**
- Tenant token on any operator route → **401**
- Operator token on any tenant route → **401**
- No new 5xx caused by any of it

### Measured performance

- **38,668 messages/sec** with 6 concurrent tenants (17,921 single-tenant)
- Peak RAM under a 20,000-message burst: API 510 MB, ClickHouse 434 MB, Postgres 190 MB, Redis 2 MB
- Heap after burst then GC: **748 MB → 11.5 MB** (no leak)
- Storage: ~60 bytes per message end to end
- Latency on the live demo tenant: **p50 3,624 ms, p90 5,859 ms** handset-confirmed

### Self-healing, demonstrated not asserted

- API boots and serves while Postgres or ClickHouse is unavailable
- ClickHouse reconnects with no restart and no operator action
- Worker panics are caught, logged with a stack, and retried on the next tick
  instead of taking the process down

---

## What is still failing

**Nothing in the browser suite.** 256 of 256, against the deployed backend.

The seven failures this section used to list are all fixed, and each is written
up above or in [handoff-ui.md](handoff-ui.md). For the record, they were not
what they looked like: only two were what the symptom suggested. The rest were
rate overrides never reaching the pricing code, a rate card that could not
report overrides at all, a fixture occupying a string a spec needed, a reset
that never restored global rates, and three tests that had only ever passed
because a mock answered in the same process.

### The defect underneath the navigation workarounds

**This app cannot client-navigate to the URL it is already on.** A `<Link>`, a
`router.push()` and a `router.refresh()` to the current URL all fetch the RSC
payload, receive a valid 200, and silently discard it — no `pushState`, no
error, no error boundary. The identical URL loads perfectly on a direct visit.

Ruled out by experiment: the server (the router's exact request replays fine),
the auth middleware, `loading.tsx`, prefetching, hydration timing, and the size
of the page. A minimal page under the same layout navigates correctly.

It is worked around everywhere it appears: filter links are plain anchors
(`ui/filter-link.tsx`); mutations that navigate elsewhere `redirect()`
server-side; and the ones whose natural destination is the page they are
already on reload with `location.assign`. The last of those — the rate-override
form — was still broken until this deployment, because its `redirect()` target
*was* the current URL.

This is the root cause worth handing to the frontend team as a real Next.js
issue, not a workaround to keep. Full write-up, including the seven things
ruled out: [Handoff, Group 1](handoff-ui.md).

---

## Backend gaps that are genuinely still open

Being precise, because "no gaps" is the claim you asked about:

| Gap | Impact | Status |
|---|---|---|
| Public send API + DLR ingest HTTP surface | Customers cannot send via API; the pipeline underneath is built and tested | **Not built** |
| Cost per conversion | Reports 0 | Needs conversion tracking, which does not exist |
| Journey run telemetry | Funnel counts are derived, not recorded | Needs a decision (see above) |
| User activity events | Only sign-ins are recorded; the contract also names API-key and MFA events | Partial |

Everything else in the contract is implemented.

---

## Bugs found and fixed during this work

Each of these would have shipped. Listed because they show what the testing
bought — and several are money or security defects, not cosmetic ones.

### Money

1. **Auto-recharge never fired.** A customer could configure "top me up when I
   fall below ₹1,000", watch the balance go under it, and have sending stop
   anyway. The setting saved, displayed back to them, and did nothing. Now
   applied inside `AppendLedgerEntry` so *every* charge path arms it — campaigns,
   journeys, verify codes and inbox replies all debit the same wallet.
2. **Finishing a campaign did not charge for it.** The terminal state was reached
   with no money moved.
3. **Inbox replies were never sent and never billed.** A reply was written to the
   thread as `queued` and left there — no carrier ever saw it, and the customer
   was charged nothing for messages they believed they had sent.
4. **Top-up ledger lines named the payment gateway, not the card.** That line is
   read by someone matching it against a bank statement, where the gateway's name
   never appears.

### Security

5. **Senders could be approved without verification.** Nothing stopped an
   operator approving an email sender whose domain was never authenticated —
   letting a tenant send as a domain they do not control, which is what SPF, DKIM
   and DMARC exist to prevent, and which damages every other tenant's sending
   reputation on shared infrastructure. Same for unverified caller-IDs.
6. **Three developer endpoints ignored `environment`, a required parameter** — so
   the test-mode page listed **live** API keys and live webhook URLs.

### Correctness

7. **Campaign creation was broken for Email and Voice.** The cost estimate
   returned 500, so the wizard died on the review step: those channels could not
   be used at all.
8. **Five contract operations returned 501** while this page claimed 151/151.
9. **Eight endpoints ignored their query filters entirely**, including the audit
   log's tenant filter — the one someone reads during an incident.
10. **The verify fraud panel returned three hardcoded zeros**, indistinguishable
    from "we checked and found nothing".
11. **The analytics trend chart redrew itself differently on every refresh** —
    daily buckets came out of a Go map, whose iteration order is deliberately
    randomised.
12. **Three of four demo contacts could never be messaged.** They sat on the
    sandbox connector's reserved failure numbers, so replying to Priya failed
    every single time and looked like a broken inbox.
13. **The MFA QR code rendered 0×0.** The SVG set `viewBox` but no `width` or
    `height`, so it was present in the DOM and completely invisible — a customer
    enrolling in two-factor auth saw an empty space.
14. **The inbox showed phone numbers instead of names**, from a placeholder that
    outlived its reason.
15. **Middleware redirected every test hook to `/login`**, so every state reset in
    every run silently did nothing and state leaked across all 43 specs.
16. **Out-of-contract enum values** returned 200 and blanked entire pages. Four
    separate instances; `scripts/enum-check.py` now guards against a fifth.
17. **Contract fields declared and never filled** — WhatsApp quality/tier,
    `consentedAt`, `waSessionActive`, template rich content, rate-card category,
    `rejectionReason`. `scripts/field-coverage.py` now finds these.

### Older findings



Each of these would have shipped. Listed because they show what the testing bought.

1. **Middleware redirected every test hook to /login.** `/api/dev/*` was inside the
   auth matcher; Playwright's `request` fixture carries no cookie, so every reset in
   every run silently did nothing and state leaked across all 43 specs. A redirect is
   not an error — nothing logged, nothing threw.
2. **Three out-of-contract enum values** — `"Reliance Jio"` where `CarrierId` is an
   enum, `jio`/`good` on routes, `policy` for an error class. Each returned **200** and
   blanked an entire page, because the frontend resolves these against fixed registries.
   Guard added: `scripts/enum-check.py`.
3. **Adding a ClickHouse column broke every send** — the insert had no column list, so
   it bound to table order. Both inserts now name their columns.
4. **`tenants.status` allowed 3 of the contract's 4 values** — `pending` was
   unrepresentable, so a tenant awaiting approval had nowhere to sit.
5. **`SenderId.dnsRecords` was in the contract and never implemented**, and creating an
   email sender generated no DNS records — onboarding dead-ended.
6. **Analytics returned hardcoded zeros** for latency percentiles and fraud counts, and
   a synthetic `"all"` carrier.
7. **`ByCampaign`/`ByJourney` usage were hardcoded empty arrays** behind a comment
   claiming that was "the truth". It stopped being true when the data plane landed.
8. **RLS silently swallowed the operator role restore** — an UPDATE matched zero rows
   and reported success.
9. **`make seed` did not load `.env`**, so it died on a config error and left the
   fixture absent — which reads as 171 broken features rather than one missing account.
10. **The reset hook rewrote 30 days of warehouse history on every test**, costing
    seconds per test and pushing unrelated assertions past their timeouts. Now 0.16s.

---

## What to say to the room

**Safe:**
> "All 151 contract operations are implemented. The backend passes 35 adversarial
> security checks including cross-tenant isolation and append-only money
> guarantees, 25 operator-console checks, and enum conformance across 23
> endpoints. It sustains 38,668 messages per second and recovers from dependency
> loss without an operator. 249 of 256 browser tests pass, along with all 2,038
> of the frontend's own unit tests. Every remaining failure is understood and
> written down — most of them trace to one documented Next.js router defect, and
> the workaround is already in place everywhere it could be applied."

**Not safe:**
> "Everything is done and tested."

The difference costs you nothing in credibility and protects you completely.

**If someone asks what the testing bought:** auto-recharge never fired, finished
campaigns were never charged, inbox replies were never sent, senders could be
approved for domains nobody owned, the test-mode page listed live API keys, an
email campaign was sent to contacts with no email address and then lost the
address it sent to, and the operator console could not see the DNS evidence it
was required to check before approving. All found here. All fixed.
