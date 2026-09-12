# Ask 32 is in: the carrier is stated, the agent is derived, and the fallback is gone

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 13 September 2026
**Re:** your `BACKEND_REQUEST_rcs-agent-scoping.md` (ask 32)

Every item in your request is built, and your three decisions have answers. §2 has
the decisions. §4 is the one thing we added that you did not ask for, and it
exists because of this change rather than beside it.

---

## 0. Before anything else: "master" was the wrong master on our side

Your message said to pull `master` of `sms-platform-frontend`. Our checkout has
two remotes with that name:

```
origin    github.com/saeedafri/sms-platform-frontend   master = f2d1122  (09 Sep)
upstream  github.com/SAQIBJH/sms-platform-frontend     master = 585fce0  (12 Sep)
```

Ask 32 is only on `upstream`. `origin` has none of your last 39 commits, and a
`git pull origin master` answered *"Already up to date"*, which is true and
useless. We found it by searching every ref for the document. If anyone else
builds from `saeedafri/sms-platform-frontend`, they are working from 9 September.

We regenerated from `585fce0`.

---

## 1. What was built

### 1.1 The three compile breaks, exactly where you said

`make generate` broke the build in three places and nowhere else:
`gen.RcsCapabilityReportVendor` and `gen.CarrierTemplateRegistrationVendor` became
`gen.RcsVendor`, and `MessageLogEntry.Currency` became `gen.CurrencyCode`. Your
asymmetry reasoning held: loud, enumerated, and fixed in minutes.

### 1.2 `vendor` on the carrier registration — taken from the request, and the guess deleted

The line you quoted — `vendor = s.RCSCarrier.Vendor()` — is gone. The vendor is the
request's, validated against the generated `RcsVendor.Valid()`, so a new vendor in
the contract needs no edit here.

You were right that the guess was holding something shut. It still is, by a
different hand: `vendor` is required and never null, so
`UNIQUE (carrier_vendor, carrier_template_id)` keeps firing. We checked production
before deleting it — **zero** RCS templates carry a carrier code today, so there is
no null-vendor row already sitting under that index.

**Attach no longer needs a configured carrier.** It used to answer 503 on a
deployment without credentials. A code pasted from a portal already exists at that
carrier; recording it sends nothing, and the send is checked (§4). So attach now
works on the live deployment, where `RCS_VENDOR` is unset, and everything in §3 is
observable from your side.

### 1.3 The registration agent is derived from the template's own sender

Your §2 argument is right and we built it as written. The agent comes from
`template → sender → rcs_agent_id`, and the carrier's id for it is read by the
same query the send path uses (`store.CarrierAgentID`: launch approved, id present,
agent not suspended or archived). So a template cannot be registered under an agent
that could not send it.

The request body carries no agent, and the route now **refuses** undeclared keys
(your `additionalProperties: false`, enforced), so a hand-built body that tries to
name one gets a 422 rather than being quietly ignored.

### 1.4 `rcsAgentId` on capabilities

Required, 404 for another tenant's agent or an invented one (the same answer), 422
when the agent has no approved launch on the carrier this deployment checks through.

### 1.5 Agent creation refused where no carrier is integrated

422, naming where RCS **is** available:

> RCS is not available in US yet: none of its carriers has an RCS integration, so
> an agent created there could never launch. RCS agents can be created in IN.

Derived exactly as your `rcsCountries()` is: a country appears when one of its
routed carriers has an adapter. The adapter list is one map,
`connector.RCSIntegrations = {airtel: AIRTEL, vi: VI}`, so your registry and ours
now answer the same question the same way.

**"Integrated" means we hold an adapter, not that this deployment has signed
credentials.** Gating on credentials would refuse every country on a deployment
still waiting for its carrier contract — India included — and brand verification is
days of work a customer should be able to start before that.

It runs **before** the approved-entity check. Sending someone through a compliance
review for a country where no agent can launch, only to refuse them at the end, is
the failure this rule exists to prevent.

### 1.6 Your two open items from 09-11d

- **Create now refuses undeclared fields**, same as PATCH. `primaryColor` on create
  is a 422 naming the key.
- **A no-op edit leaves `updatedAt` alone.** And not only `PATCH {}` — we compare
  *values*, not which keys arrived. A form that re-saves every field unchanged is
  the ordinary case, and a key-presence check would still have stamped it. The
  column moves only when a stored value differs.

### 1.7 §6.3 — a null `displayName` or `useCase` is a 422

Refused, naming the field, and the record is read back to prove the refusal is not
wrapped around a write that happened anyway. Clearing an *optional* field still
works exactly as before; that test is untouched.

---

## 2. Your three decisions

### 2.1 Re-pointing a sender to a different agent — **refuse**. It already was; now it is proven.

`PATCH /v1/sender-ids/{id}` never declared `rcsAgentId`, and that route has been
behind the unknown-fields guard since it was added on 4 September (`c38d0dc`). So a re-point has always been a 422,
not a silent drop — but nothing asserted it, and a refused-by-accident guarantee is
one refactor from vanishing. There is a test now, and it reads the stored agent back
after the refusal.

Refuse is also the only option that holds by construction. Registrations are
derived from the sender's agent, so the agent being immutable is what keeps every
derived registration valid. Cascade and record both exist to repair a state that
refuse never allows.

**What this leaves open, and it is real.** See 2.2: production has one RCS sender
with no agent, and under "refuse" it can never get one.

### 2.2 An RCS sender with no agent — **required**. There is no fallback.

`POST /v1/sender-ids` with `channel: RCS` and no `rcsAgentId` is a 422:

> An RCS sender needs an RCS agent: the agent is the brand the handset shows, and
> without one no template can be registered with a carrier or sent.

Your corrected contract text is accurate, and we have made it more so than you
knew: the deployment-wide agent is not just unused, it is **deleted** (§5).

**The existing rows, surfaced rather than migrated, as you asked.** On production
today:

| | |
| --- | --- |
| RCS senders with no agent | **1** (status `approved`, predates agents) |
| RCS templates on that sender | **2** |
| RCS templates holding any carrier code | **0** |

With 2.1 as it stands, those two templates are permanently stuck: the sender cannot
gain an agent, so they can never register. The customer's only path today is a new
sender with an agent and the templates recreated under it.

**If you want a better path, it is small and we would build it:** let a PATCH
attach an agent to a sender that has **none**, once. That is not a re-point — no
registration can exist under a null agent (registration now requires one, and
production has zero legacy codes), so attaching strands nothing. Re-pointing a
sender that already has an agent stays refused. It needs `rcsAgentId` declared on
the sender PATCH, which is your contract to change. Say the word.

### 2.3 Agent creation in a country with no integrated carrier — **refused**, and it says where

§1.5. Today that is **IN only**, and it opens elsewhere with no code change on
either side.

---

## 3. The status codes, in the order they fire

So your mock can refuse in the same order we do.

**`POST /v1/rcs/capabilities`**

1. `401` no session
2. `400` no `msisdns`
3. `400` **no `rcsAgentId`** — 400 rather than 422, matching this endpoint's other
   malformed-body answers. The contract's 422 is kept for "no approved launch".
4. `400` every number malformed, or more than 10,000
5. `404` agent not found or not yours
6. `503` no carrier configured
7. `422` no approved launch on the carrier this deployment checks through

On production today (no carrier), 5 is observable and 7 is not: 6 answers first.

**`POST /v1/templates/{id}/carrier-registration`**

Two refusals fire *before the handler*, and therefore before the session check —
true of every route here, since authentication resolves an identity and leaves the
401 to the operation:

- `422` an undeclared key in the body (middleware)
- `422` no body at all (the binder, in its own words: *can't decode JSON body: EOF*)

Then, in the handler:

1. `401`, `403`
2. `422` **`vendor` missing, empty, or not `airtel`/`vi`**
3. `404` template
4. `422` not RCS · not approved in Relay
5. `422` the template's sender has no agent · that agent has no approved launch on
   the named carrier (suspended counts as none)
6. *attach:* `200`, no carrier configuration needed
7. *submit:* `422` already registered · `503` no carrier configured · `503` the
   configured carrier is not the one you named · `422` not a text template · `422`
   no category · `409` the carrier has no template API (Vi)

**`POST /v1/rcs/agents`**

An undeclared key is a `422` before the handler. Then: `401` · `422` display name
· `422` use case · `422` **country not available** · `409` no approved entity.

---

## 4. One thing this change made necessary: the send checks the carrier matches

Not in your request, and it had to be in this one.

The gate used to accept a template whose carrier status was `approved` without
asking **which** carrier approved it. That was safe only because the vendor was
guessed from the deployment's own gateway — so the stored vendor and the carrier
sending the message were always the same thing.

Stated by the customer, they need not be. A Vi portal code attached on a deployment
that sends through Airtel would quote Vi's template id to Airtel on every message,
and each would come back **"Template not found"** at the gateway, after the hold,
hours later, with nothing connecting it to the attach. That is the exact failure
your document opens with, arriving from the direction this change opened.

So the gate now treats a template registered with a different carrier than the one
carrying the message as `not_submitted` there — refused as
`carrier_template_not_approved`, before money moves, carrier never called. Nothing
in your contract changes; it is an existing refusal code reaching one more case.

---

## 5. The fallback agent is deleted, not just unused

After §1.2 and §1.4, nothing read `RCSFallbackAgentID` — `main.go` set it and no code
consumed it. We removed the field, the wiring, and the config: **`RCS_AIRTEL_AGENT_ID`
and `RCS_VI_BOT_ID` are no longer read.** A value left in an env file does nothing.

Removed rather than left optional on purpose. An unused optional value is one
misreading away from being used again, and when used it becomes some customer's
brand on a stranger's handset. "There is no deployment-wide fallback" is now true
because there is nothing there to fall back to.

---

## 6. How we know the new tests can fail

You asked specifically for a test that a request omitting `vendor` or `rcsAgentId`
is refused, because nothing in Go forces a handler to read a field. We wrote them,
then removed each guard and ran them again.

| Guard removed | Test |
| --- | --- |
| the `vendor` check | red |
| the `rcsAgentId` check | red |
| registering under the sender's agent (swapped for another) | red |
| capabilities asking about the named agent (swapped for another) | red |
| the send-side carrier match (§4) | red |
| `updatedAt` value comparison (always stamped) | red |

**The first one was not red the first time, and that is worth the paragraph.** With
the vendor check deleted, the test still passed. An empty vendor flowed on to the
launch lookup, found no carrier named `""`, and was refused there with a different
422. Right status, wrong reason — the same trap the limit-bounds test fell into
for ask 30, when two routes "passed" on a missing-parameter 422. The test now asserts
that the refusal is *for naming no carrier*, and went red under the mutation.

If your own test for `vendor` asserts only the 422, it may be passing on something
else too.

---

## 7. What we would like back

1. **The legacy agent-less sender** (§2.2) — the one-time attach, or confirm "create
   a new sender" is the path.
2. **Which remote is canonical** (§0), so the next ask does not start from
   9 September.

Retention of verification documents (your §3.6 of 11 September) is still the
separate slice we agreed, and is not in this change.
