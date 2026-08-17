# Handoff: changes made in the frontend repo

**Nothing here is committed.** Every change below is sitting uncommitted in
`SMS-UI` for you to review, adjust and land as you see fit. This page exists so
you can read the reasoning before you read the diff.

```bash
cd SMS-UI && git status --short     # 80 files
```

They fall into four groups: **one product bug we found and fixed**, **spec
fixes** (tests that could not pass against any real backend), **two dev-mode
gates that were too narrow**, and **the dev bridge** that lets the suite drive a
real server at all.

---

## Changes from running against the real backend

Everything above was found against MSW. Running the same suite against a live
API surfaced five more, and they split cleanly into one product bug and four
tests that were only ever passing by luck.

### `admin/rates/add-override-form.tsx` + `lib/operator/actions.ts` — product bug

Adding a tenant rate override saved, returned 201, and the table went on showing
the default rate.

`createOverride` ended with `redirect("/admin/rates?tenantId=…")` — which is the
URL the operator is **already on**, because the page's own tenant picker put
them there. That is the same App Router behaviour as the filter links: the
payload is fetched and discarded. The action now returns `{status:"done"}` and
the form reloads with `location.assign`, matching `environment-toggle.tsx`.

`assign()`, not `reload()` — `reload()` re-submits the POST and would file the
override twice.

### Four specs that could not survive real latency

None of these are product bugs. They are assumptions that only hold when the API
answers in the same process.

| Spec | What it assumed | Why it only failed for real |
|---|---|---|
| `billing.spec.ts` | Clicking **Save** finishes before the next line runs | It is a server action; the dev hook won the race and charged the wallet while auto-recharge was still unconfigured. Now waits for the response. |
| `operator-audit.spec.ts` | `locator.count()` waits | It is one of the few Playwright calls that does **not**. It counted the loading skeleton's zero rows. Now waits for a row first. |
| `operator-support.spec.ts` | A customer session already exists | Nothing in that file signs the customer in — it relied on whatever spec ran before. Alone, it hung on a sign-in page waiting for a "Subject" field. Now signs in explicitly. |
| `operator-support.spec.ts` | 30s is enough | Two sign-ins, ~12 navigations and a reload — roughly 3× any other test. Raised to 120s rather than split, because the point is that the whole loop holds together. |

The `inbox.spec.ts` duplicate-message failure was **ours, not yours**: the
backend seed used the exact string the spec posts as its probe, so the locator
matched two elements. Fixed in the seed; the spec was right.

---

## Pointing the app at the live backend

The backend is deployed. Only `.env.local` changes — and it is gitignored, so
this is a local switch, not a commit.

Every backend call in this app is made **server-side** by Next.js, with the
session token attached from an httpOnly cookie (`src/lib/api/fetchers.ts`,
`src/lib/operator/fetchers.ts`). The browser never talks to the API directly —
`useMe` and `useWalletBalance` go through same-origin `/api/*` route handlers
for exactly that reason. So switching backends needs **no CORS work and no
cookie changes**: one URL.

```bash
# a tunnel to the VPS's loopback :8080
ssh -i ~/.ssh/id_ed25519 -f -N -L 127.0.0.1:18080:127.0.0.1:8080 root@31.97.186.223
```

```ini
# SMS-UI/.env.local
NEXT_PUBLIC_USE_MOCKS=false
NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:18080
```

The tunnel rather than `https://sms-api.saqibsaeed.cloud`, deliberately: nginx
returns **403** for `/v1/dev/*` on the public hostname, and the browser suite
needs those hooks to force states a real carrier would take hours to produce.
The tunnel bypasses nginx, so the suite gets them and the internet does not.

For a demo — where you want the real URL and no tunnel — use the public
hostname instead. Everything except the `/v1/dev/*` hooks works identically.

!!! note "`NEXT_PUBLIC_*` is inlined at build time"
    `next dev` re-reads it on restart, but a built app freezes whatever was set
    when `next build` ran. A production build for the demo must have the public
    hostname set at build time, not at start time.

---

## Group 1 — A real product bug: filter links did not navigate

**This is the one to read even if you skip the rest.** It is a genuine defect in
the shipped product, not a test problem, and it affected every filtered screen
you have.

**Symptom.** On `/analytics`, `/developer/logs`, `/developer/verify/{id}/analytics`,
`/admin/audit`, `/admin/usage` and the operator queues, clicking a filter chip,
a range tab, a "Clear filter" link or a pagination link did **nothing**. The URL
never changed. No console error, no error boundary, nothing in the network tab
that looked wrong. Navigating to the identical URL by hand always worked — which
is exactly why it read as broken pages rather than broken links.

### What we found, in the order we found it

**First, a real bug in the hrefs.** Every filtered view built its links as a
bare query string:

```tsx
return qs ? `?${qs}` : "?";        // ← wrong
```

`next/link` does not resolve a query-only href against the current route; it
resolves it against the app root. Each view now builds an absolute path from a
`BASE_PATH` constant (or, for the two dynamic routes, a path passed in as data).

**That fixed the hrefs and did not fix the navigation.** Clicking still did
nothing. So we kept going.

**What the router actually does.** The click reaches the anchor with the correct
href and `defaultPrevented === false`. The router issues its RSC request. The
server answers **200** with a well-formed 33KB flight payload — we captured the
router's exact request, replayed it header-for-header, and got a valid response.
The router then discards it: **no `pushState`, no error, no navigation.**

`useRouter().push(href)` fails in exactly the same way. So this is the App Router
refusing the navigation, not `Link`'s click handling.

### Ruled out, so nobody repeats the search

| Suspect | Verdict | How we checked |
|---|---|---|
| The server / our API | **Not it** | Replayed the router's exact request; valid 200 |
| The auth middleware | **Not it** | Excluded the route from the matcher; no change |
| `loading.tsx` / Suspense | **Not it** | Added one to a minimal page; still fine |
| Prefetching | **Not it** | `prefetch={false}` on every chip; no change |
| Number of links | **Not it** | Three copies of the same chip row navigate fine |
| Hydration timing | **Not it** | Deterministic at 0ms, 1s, 3s and 8s settle |
| A specific child component | **Not it** | Bisected the tree; no single component explains it |

A minimal page with the same links, under the same layout, with the same data
fetch, navigates perfectly. The large real views never do.

### The fix

A new `src/ui/filter-link.tsx` — a plain `<a>`:

```tsx
export function FilterLink({ href, className, children }) {
  return <a href={href} className={className}>{children}</a>;
}
```

Every filter chip, "Clear" link and pagination link across ten view files now
uses it. **Verified on all six affected screens**, 3/3 at every timing.

**Why a full navigation is the honest trade.** These views are server-rendered
and re-fetch everything when a filter changes, so the client transition was
saving very little. A filter that reloads the page is strictly better than a
filter that does nothing. Every anchor behaviour is preserved exactly:
middle-click, ⌘-click, "open in new tab", "copy link address", crawlers.

Cross-route links (a fraud card into the logs explorer, a campaign name into its
detail page) still use `next/link` — those always worked and still do.

**When you upgrade Next.js**, retry `next/link` here: it is a one-line change
inside `FilterLink` rather than an edit across ten views. That is the main reason
it is a component and not ten inline `<a>` tags.

!!! note "We reported this wrongly twice before"
    First as "~9 tests blocked on a frontend component bug we didn't want to
    guess at" — it was ours to investigate. Then as "fixed" once the hrefs were
    absolute — they were necessary but not sufficient, and we had not re-measured
    before saying so. The write-up above is what the evidence actually supports.

**Unit tests updated:** eight href assertions across five `*.test.tsx` files
expected the old `"?"`-style values and now expect the absolute paths. Nothing
was weakened.

---

## Group 2 — Spec fixes

Each of these was a test that asserted something only a mock could do. None
weakens an assertion; each corrects one.

### 2a. Specs that never signed in — 5 files

`operator-routes`, `operator-audit`, `operator-rates` and `operator-usage`
contained **no sign-in call at all**. `operator-rates` said so in a comment:

> *The operator console auto-seeds its own dev session cookie via middleware — no
> operator login step needed.*

True under MSW. Against a real backend every `/admin/*` route redirects to
`/operator/login`, so the tests failed on a missing button 30 seconds later
rather than on the missing session that actually caused it.

**Change:** added an `operatorSignIn(page)` helper — copied from
`operator-tenants.spec.ts`, including its generous timeout — and called it at
the top of every test that opens an `/admin/*` route.

`mfa.spec.ts` had the same shape one level down: every test begins with
`enrollMfa(page)`, which opened `/settings/security` directly. The file *does*
call `signIn(page)` — but at lines 80, 97, 116 and 147, to exercise the login
challenge, long after enrolment had to happen. **Change:** `enrollMfa` now signs
in first.

**Why we could not fix this from the backend.** The operator console is a
separate identity system by design: an operator has no tenant and works across
all of them, so a tenant session must never reach `/admin/*` and vice versa. Two
of our adversarial security checks exist to prove exactly that. Making those
routes answer an anonymous visitor would delete the property those checks verify.

### 2b. `auth.spec.ts:8` — rewritten

It asserted that an anonymous visitor to `/overview` is auto-seeded a session and
lands authenticated as Acme Retail. Against a real backend that is an
authentication bypass: anyone typing the URL would be handed a live session for a
real customer's account.

**Change:** rewritten to assert the redirect, which is what the backend does:

```ts
await page.goto("/overview");
await expect(page).toHaveURL(/\/login/);
```

### 2c. `rbac-enforcement.spec.ts` — one assertion inverted, deliberately

The "cookieless request" test asserted that a bare `request` fixture **does**
receive a `relay_session` cookie, because the middleware's seed branch handed
one to any cookieless caller. We removed that branch (see 4a), so it now asserts
the fixture stays cookieless.

The assertion the test exists for is unchanged and still passes:

```ts
expect(body).not.toContain(SECRET_MARKER);   // no key material to an anonymous caller
```

The follow-up `expect(body).toContain("Restricted")` became
`expect(res.url()).toMatch(/\/login/)` — a real backend redirects rather than
rendering a role-gated page. **This is the stronger outcome**, but it is a
changed expectation, so it is worth your review.

### 2d. Operator credentials on the tenant login page — 3 tests

The rate-card tests in `email-`, `voice-` and `whatsapp-campaign-and-rates`
signed in at `/login` (the tenant page) with `ops@relay.internal`. Our tenant
login correctly answers **401** for an operator. **Change:** they now use
`/operator/login`.

### 2e. Two ids that are not valid UUIDs — 2 tests

`inbox.spec.ts` navigated to `cv000005-…` and `cv000003-…`. A UUID is
hexadecimal; `v` is not a hex digit, so Postgres rejects these outright and no
spec-compliant backend can hold them. MSW never noticed because it stores ids as
plain strings.

**Change:** the two constants now use the ids we seed, `cf000005-…` and
`cf000003-…`. Nothing else in those tests changed.

---

## Group 3 — Two dev-mode gates that were too narrow

### 3a. `src/lib/dev-codes.ts` (new)

`verify.spec.ts` asserts `getByText("Dev code:")` is visible. That hint was
wrapped in `NEXT_PUBLIC_USE_MOCKS === "true"`, so it rendered under MSW and
never against a real server — no response we send could put it there.

The gate existed for a good reason. A verification code and a TOTP code are
secrets: an API that returned them would let anyone verify any number or pass any
MFA challenge without seeing the handset. Our backend logs them and returns
nothing.

**Change:** a new module widens the gate to a second explicit flag.

```ts
export function showDevCodes(): boolean {
  return (
    process.env.NEXT_PUBLIC_USE_MOCKS === "true" ||
    process.env.NEXT_PUBLIC_SHOW_DEV_CODES === "true"
  );
}
```

A **function**, not a const — `NEXT_PUBLIC_*` is inlined at build time either
way, so the bundle is identical, but a module-level const is evaluated once on
first import, and the component tests set these vars per-test after that has
already happened. As a const, the first test to import the module decided the
answer for every test after it.

Five call sites now use it: `try-it-panel.tsx`, `mfa-card.tsx`,
`mfa-challenge-form.tsx`, `forgot-form.tsx`, and `verify-email-banner.tsx`.

### 3b. `verify-email-banner.tsx` — the same gate, missed the first time

This one still read `process.env.NEXT_PUBLIC_USE_MOCKS` directly, so the
"Verify now (dev)" link never rendered against a real server and the email
verification loop could not complete. It now calls `showDevCodes()` too.

!!! warning "These need a rebuild"
    `NEXT_PUBLIC_*` values are inlined at build time, so the flag has no effect
    until the app is rebuilt with it set:

    ```bash
    NEXT_PUBLIC_SHOW_DEV_CODES=true npx next build
    NEXT_PUBLIC_SHOW_DEV_CODES=true npx next start -p 3000
    ```

    It is set in `.env.local` in this working copy. Both flags default to off,
    so production ships without either.

**The backend now issues matching fixed values under `ENABLE_DEV_ENDPOINTS`**,
so these three shortcuts actually work end to end:

| Constant in `src/lib/auth/session-config.ts` | Value | Backend |
|---|---|---|
| `DEV_VERIFY_CODE` | `424242` | `verify.GenerateCode(length, dev)` |
| `DEV_RESET_TOKEN` | `dev-reset-token` | `RequestPasswordReset` |
| `DEV_VERIFY_TOKEN` | `dev-verify-token` | `ResendVerificationEmail` |

Each is armed only by a genuine request and stored hashed against the one user
who made it — knowing the string does nothing on its own. All three are
unreachable unless `ENABLE_DEV_ENDPOINTS` is set, which defaults to off and now
refuses to start on an unrecognised value.

**If you would rather not ship this at all**, the alternative is for the specs to
read the issued code from a dev endpoint instead of the page. We are happy to
expose one under `/v1/dev/*`.

---

## Group 3b — Two more gates with the same fault

Found by grepping for every remaining bare `NEXT_PUBLIC_USE_MOCKS` read after
the first pass missed them.

- **`verify-email-banner.tsx`** — the "Verify now (dev)" link stood in for the
  emailed one and never rendered against a real server, so the email
  verification loop could not be completed at all.
- **`voice-verify-row.tsx`** and **`dns-record-row.tsx`** — "Call to verify" and
  "Check DNS" were both hidden outside mock mode. "Check DNS" in particular is a
  real customer-facing control that re-runs the lookup; there was no way to
  complete email sender onboarding without it.

All three now call `showDevCodes()`.

### One behaviour change worth your review

`voice-verify-row.tsx` now clears the displayed code after a **failed** confirm,
not only a successful one. That pairs with a backend change: a wrong code is now
burned rather than left valid.

The old behaviour left a six-digit code standing after a failed attempt, which is
brute-forceable by anyone holding a session — a million guesses is minutes of
scripted requests, and the prize is the right to place calls as a number you do
not own. Now one wrong guess costs a new verification call. Leaving the dead code
on screen would have offered the user a number guaranteed to be rejected.

---

## Group 4 — The dev bridge

Needed before a single spec could run against a real server, and the first place
to look if something behaves oddly.

### 4a. `src/middleware.ts` — the fix that unblocked everything

`/api/dev/*` sat inside the auth matcher. Playwright's bare `request` fixture
carries no cookie, so **every state reset in every spec silently 307'd to
`/login` and did nothing.** A redirect is not an error: nothing logged, nothing
threw, and state leaked across all 43 specs for an entire day of runs.

**Change:** `api/dev` added to the matcher exclusions. Verified 307 → 204.

### 4b. `src/app/api/dev/*/route.ts` — 9 proxies

Each forwarded to MSW and refused to run outside mock mode. **Change:** they now
forward to the real backend at `/v1/dev/*`, passing the `relay_session` cookie
as a bearer token.

### 4c. Two probe scripts, not part of the suite

- `e2e-probe-filter-links.mjs` — the reproduction for Group 1. Reads each link's
  own `href`, clicks it two ways, then navigates to that same href directly and
  fetches it server-side.
- `capture-screens.mjs` — regenerates the screenshots in this guide.

---

## What we verified before handing this over

```bash
cd SMS-UI
npx tsc --noEmit     # clean
npx vitest run src/  # 227 files, 2038 tests, all passing
npx playwright test  # 249 / 256
```

The browser suite was 194/256 when we started this pass.

---

## Still open, and why

### Two modelling divergences — a product decision, not a bug

We deliberately did **not** make these pass, because the only way to do so is to
report numbers the database disagrees with.

1. **Campaign progress.** MSW derives it from `sendStartedAt` + `sendDurationMs`,
   simulating a trajectory that advances with wall-clock time. We store real
   counts. Specs asserting on mid-flight progress cannot be satisfied by a
   stored status, whatever fixture we seed.
2. **Journey funnel.** `automation.spec.ts` expects `exitedSuppressed` to be
   exactly `2`. We derive it by counting list members who are genuinely
   suppressed — with this data that is `0`. MSW returns `2` from a simulation,
   and its own suppression fixtures are not even contacts, so no data change
   reconciles them.

**A funnel that reports 2 when the data says 0 is worse than a failing test.**
Tell us which behaviour you want and we will implement it.

### One contract gap: `LedgerEntryType` is missing `refund`

`components["schemas"]["LedgerEntryType"]` is `"charge" | "topup" |
"auto_recharge"`. The backend also writes **`refund`** rows — when a campaign
reserves funds for an estimated segment count and uses fewer, the difference is
released back.

This does **not** break your UI today: `billing-view.tsx` renders `{e.type}`
directly and treats anything that is not `"charge"` as a credit, so a refund
shows as a green `+` row, which is correct. But it is outside the declared
union, so a stricter consumer would reject it.

**Ask:** add `"refund"` to `LedgerEntryType` in the contract. We have not
touched `openapi.json` — that is yours.

---

## Reviewing the diff

```bash
cd SMS-UI
git diff src/app/\(dashboard\)/analytics src/app/\(admin\) \
         src/app/\(dashboard\)/developer/logs src/app/\(dashboard\)/billing \
         src/app/\(dashboard\)/campaigns          # group 1 — the navigation fix
git diff e2e/                                     # group 2 — spec fixes
git diff src/lib/dev-codes.ts src/app/\(auth\) \
         src/app/\(dashboard\)/_components         # group 3 — dev-code gates
git diff src/middleware.ts src/app/api/dev/       # group 4 — dev bridge
```

Every edit carries a comment explaining why it is there, so the diff should read
without this page next to it. If you disagree with any of them, revert that file
— none depends on another, except that the dev bridge is what makes the suite
able to run at all.

**Verification we ran on your repo before handing this over:**

```bash
npx tsc --noEmit     # clean
npx vitest run src/  # 227 files, 2037 tests, all passing
```
