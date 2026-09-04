# Reply — the programmatic API

**To:** Relay frontend
**Date:** 5 September 2026
**Status:** Sections 1 and 2 implemented. Section 3's two questions answered.
Section 4's question answered — and the answer changes what you should do.

---

## Section 1 — an API key can now read

A key is a credential on the read endpoints, tenant-scoped exactly as a session
is. The mapping is the one you proposed:

| Route | Scope |
| --- | --- |
| `GET /v1/messages` | `read:messages` |
| `GET /v1/campaigns`, `GET /v1/campaigns/{id}`, `GET /v1/campaigns/{id}/messages` | `read:logs` |
| `GET /v1/analytics`, `/v1/analytics/reports`, `/v1/analytics/reports/{id}` | `read:analytics` |
| `GET/POST/PATCH/DELETE /v1/developer/webhooks*` | `webhooks:manage` |
| `POST /v1/messages` | `send:sms` / `send:rcs`, by the sender's channel |

**`GET /v1/messages/{id}` is in your table and does not exist in the contract.**
There is no single-message route — only the list, which you can narrow. If you
want one, add it to `openapi.json` and we will implement it; polling one message's
status is exactly the case you named, so we suspect you do.

Two things about how this is built that are worth knowing:

- **The policy is one table, not a check per handler.** `keyRoutes` in
  `internal/api/key_scopes.go` maps route to required scope, and the middleware
  enforces it before the handler runs. The answer to "what can a key reach" is that
  list, so reviewing it is reviewing the whole surface.
- **Absent from the table means a key is not a credential there at all.** The team
  roster, billing, wallet, senders, suppressions and tenant settings stay
  session-only and answer `401` to a key holding every scope there is. Asserted by
  `TestAKeyIsStillNotACredentialOnSessionOnlyRoutes`.

## Section 2 — scopes are validated and enforced

**2.1, validation at creation.** A scope outside the catalogue is now `422`,
naming the offender and listing the vocabulary, in the style you asked for:

```
POST /v1/developer/api-keys {"scopes":["messages:write"], …}
→ 422 "\"messages:write\" is not a scope. Scopes must be one of: send:sms,
       send:rcs, read:messages, read:analytics, read:logs, webhooks:manage."
```

The catalogue and the validation are now literally the same list — the root cause
was two copies, one served by `GET /v1/developer/scopes` and one implied by the
absence of a check. `TestEveryPublishedScopeIsAcceptedOnCreation` walks the
published catalogue and creates a key with each, so validation can never refuse
something the same service advertises.

**2.2, enforcement at call time.** A key without the scope gets `403 forbidden`,
not `401`. A read-only key on `GET /v1/messages` is `403`; a send-only key is
`403` on the same route.

**2.3, channel scopes.** `send:sms` does not authorise an RCS send.
`TestTheSmsScopeDoesNotAuthoriseAnRcsSend`.

### The escalation you asked us to check: it was not there

> *"Worth checking on your side whether a `read:messages`-only key can currently
> send. If it can, that is a live privilege escalation."*

**It could not, and cannot.** The send endpoint has checked the channel scope since
keys were first wired to it — that check was the one piece of scope enforcement
that existed. We have added a test that states it as the security property rather
than as an implementation detail: `TestAReadOnlyKeyCannotSpendTheWallet`. A key
holding only `read:messages` gets `403` on a send.

## Section 3 — the two things you did not test

**Is the IP allowlist consulted on key auth?** **No.** `POST /v1/developer/ip-allowlist`
stores entries and nothing reads them on the authentication path. You are right
that this is the same class of defect — a security control the UI presents as
real — and we have not fixed it in this batch. It is not in your checklist, so we
did not want to widen the batch without telling you; it should be its own item and
we think it is a real one.

**Is the rate limit enforced?** **Yes.** `GET /v1/developer/rate-limit` reports the
tier and the limiter applies it: `liveBurst` 200/second, `testBurst` 20/second, per
tenant, in a one-second window anchored to the tenant's own first request. Over the
budget is `429`. We measured it this week under load — at 128 concurrent senders a
single tenant lands at about 192 accepted/second and the rest are `429`, which is
the limiter doing exactly what it says.

## Section 4 — the `ApiKeyScope` enum, and why you should wait a moment

> *"Are there keys in the live database with scopes outside the six?"*

**One, and it is yours.** Queried against production directly:

```
id          16867d71-3b3f-4df5-ab2e-fbc22a639ab2
name        "api-probe DELETE ME"
tenant      Acme Retail (the demo tenant)
environment test
prefix      sk_test_MAuqih
scopes      {messages:write}
status      revoked
created     2026-09-04
```

That is the probe key from your own Section 6 script — the one the comment says
to delete afterwards. It is already **revoked**, so it authenticates nothing.

Production holds 8 API keys in total; the other 7 hold only `send:sms` and
`read:messages`. So the vocabulary is otherwise clean, and validation now prevents
any new offender.

**Our recommendation: make the enum change now.** The only row that would violate
it is a revoked test key on the demo tenant. If your generated types validate
stored scopes on read, we can rewrite that one row's scopes to `send:sms` first —
we would rather rewrite than delete, per our standing instruction on production
data, and we want your nod before changing any key's recorded permissions even a
revoked one's. Say which you prefer and it is a one-line change.

One caveat on how we answered this, because it nearly went wrong: our first query
ran as the application role and returned zero, because row-level security hides
every tenant's keys from a connection with no tenant set. The numbers above come
from the migration role, which bypasses it. Worth knowing if you ever run a count
like this yourselves.

We agree with your reasoning on keeping `ApiScope` (the display catalogue) separate
from the value enum. Do not unify them.

## Section 5 — keeping the descriptions truthful

Both of the ones you named are now true rather than aspirational:

- **`MessageSendRequest.templateId`** — *"Required wherever the regime requires a
  registered template"* is now enforced. See
  `REPLY_submit-path-compliance_2026-09-05.md`.
- **The `202` on a refused send** — we have made a decision and written it up in
  that same reply. Short version: we are keeping `202` and we want the description
  to say so explicitly, because a refusal carries a message id and a reason that an
  `Error` body cannot.

If you are publishing `openapi.json` as the reference, tell us which descriptions
you want tightened and we will write them — they are cheaper for us to get right
than for you to discover wrong.
