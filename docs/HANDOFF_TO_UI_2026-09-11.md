# The real carrier numbers, and steps 1–3 built — your logo limit is 40x too generous

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 11 September 2026
**Re:** `BACKEND_REQUEST_rcs-agents.md`, your `master@9a829c3`

**Read §1 first even if you read nothing else.** Your provisional media limits are
wrong in the dangerous direction, by 25x and 40x, and every one of them is a file
Airtel refuses after a customer has exported it.

Steps 1, 2 and 3 are built: the model, the nine operations, the operator queue,
and object storage with signed expiring URLs. §3.

**Option (a)**, and §5 says why the alternative is worse than you argued.

One thing you shipped in the same push and did not mention: **`segmentsPerMessage`
became a range**, which broke our build. §6. Not a complaint — it is the loud
direction working exactly as your `§3.1` predicted — but it arrived unannounced
in a document about RCS agents.

---

## 1. The numbers, extracted from the two PDFs

Every figure below is verbatim, with the page. Where the two carriers disagree
the tighter one wins, because an asset has to satisfy whichever carrier ends up
serving it and nothing at upload time knows which.

| Purpose | Your provisional | **The real limit** | Dimensions | Source |
| --- | --- | --- | --- | --- |
| `agent_logo` | 2 MB | **50 KB** | 224 × 224 exactly ✓ | Airtel p6 |
| `agent_hero` | 5 MB | **200 KB** | 1440 × 448 exactly ✓ | Airtel p6 |
| `template_media` | 10 MB | **1 MB** image | none stated | Vi Template Mgmt p95 |
| `verification_document` | 10 MB | 10 MB — ours | none | no carrier sees it |

**Your dimensions were exactly right.** 224 × 224 and 1440 × 448 are Airtel's
numbers to the pixel. **Your file sizes were 40x and 25x too generous.**

That asymmetry is worth a moment: the dimensions were right because they are
widely published, and the sizes were wrong because they are not. A 1.8 MB logo
sails through the check you specified, is stored, is attached to an agent, and
is refused at submission days later — and nothing in the customer's hands
connects the two events.

### 1.1 Where the carriers disagree, in full

```
                        Airtel                    Vi
message image           5mb (p22)                 100 MB (RCS APIs p15)
message video           10mb (p22)                100 MB
rich-card image         not stated                2 MB   (Template p94)
CAROUSEL image          not stated                1 MB   (Template p95)
carousel video          not stated                5 MB   (Template p95)
agent logo              224x224, 50 KB (p6)       not stated anywhere
agent hero              1440x448, 200 KB (p6)     not stated anywhere
card dimensions         not stated at all         full per-orientation table
```

**`template_media` is 1 MB and that will look harsh.** It is Vi's carousel image
cap, and it is the tightest number either carrier states for any surface a
template can use. An asset uploaded "for template media" does not yet know
whether it will end up in a carousel, so 1 MB is the only bound that cannot
produce a rejection later. **If you would rather we took the purpose per
surface** — `template_media_card` at 2 MB and `template_media_carousel` at 1 MB —
declare them and we will follow; that is a contract change, not a guess, so it
is yours.

**One trap.** Airtel's hero is **1440 × 448**. Vi's standalone-vertical-short
card image is **1440 × 480**. Different assets. We very nearly wrote one
constant; a hero at 1440 × 480 is refused, and the test that catches it is named
after this.

### 1.2 `RcsAgentUseCase` — Airtel recognises three of your four

> Airtel p7: "Airtel IQ fully supports all standard Google RCS agent use cases:
> 1. PROMOTIONAL 2. TRANSACTIONAL 3. OTP"

**There is no `MULTI_USE`**, and Vi does not enumerate use cases at all — the
only value in either Vi document is `"trafficType": "advertisement"`, once, in an
example, with the allowed set never stated.

You said the carrier's vocabulary should win here outright. By that rule the set
is **{OTP, TRANSACTIONAL, PROMOTIONAL}**. We have **not** narrowed it
unilaterally — the contract declares four and we accept four, so an agent can be
drafted as `MULTI_USE` today and would be refused at Airtel submission. Drop it
and we will follow in the same change.

Related, since it is the same page: Airtel also constrains **agent name to 40
characters** and requires the brand colour to have **4.5:1 contrast against
white**. Neither is in the contract. Worth declaring — a 60-character display
name is refused at submission for a reason no screen currently mentions.

### 1.3 What neither document states

So nobody fills these in from Google's RBM docs later and believes they came
from a carrier: **video duration, codec, bitrate; Vi's agent artwork entirely;
Airtel's card dimensions, PDF size, thumbnail rules, and its upload endpoint's
size cap and MIME list; minimum dimensions; GIF frame limits; and what either
platform actually returns when a file is too large** — Airtel's full error
catalogue (p51–52) has no media-size entry at all.

Both carriers keep uploaded media for **60 days** (Airtel p29, Vi p51).

---

## 2. §7.1 — dimensions are read off the file

Enforced, and it is the piece your mock cannot check for you.

`image.DecodeConfig` reads the header rather than the pixels, so a 200 KB banner
costs a header parse. A purpose with an exact size refuses a file it cannot
decode, rather than storing something unmeasurable and deferring the refusal to
the carrier.

**We extended it one field over, because it is the same bug.** The `Content-Type`
on a multipart part is supplied by the client exactly as the dimensions were, so
we sniff it from the bytes too. A caller could otherwise store an executable by
calling it `image/png` — and that one is worse than a wrong dimension.

The refusal names both numbers, as you asked:

```
422  The image is 1000 x 1000 and this purpose needs exactly 224 x 224.
413  The file is 68 KB and the limit for this purpose is 50 KB.
```

Guard: five real PNGs at five real sizes, mutation-verified by recording the
dimensions and enforcing nothing — the exact shape of your mock. Red on three
cases, including the 1440 × 480 hero.

---

## 3. What is built

**Step 1 — the model and the six customer operations.** `rcs_agents` and
`rcs_agent_carrier_launches`, the lifecycle in your §3 with every refusal, the
paged list with `status`/`country`/`q` filtered in the WHERE of both queries.

**Step 2 — the two operator operations**, and agents are now the fourth source
in the approval queue, merged by age with the other three so a page-one full of
senders cannot bury an agent submitted weeks earlier.

**Step 3 — `POST /v1/media` and object storage.** Signed, expiring,
tenant-prefixed. §4.

Three details worth your eye:

- **`carrierLaunches` is derived from `routes`**, as you asked — not a copied
  table. A corridor configured tomorrow appears on every agent's launch screen
  with nothing to keep in step.
- **The launch guard order is country-then-reachability**, preserved for the
  reason you gave.
- **`state` refusals are in the UPDATE, not a read-then-write.** Two submissions
  racing would otherwise both read `draft`, both write, and the second would
  overwrite the first reviewer's queue row.

### 3.1 One limit in your own contract, found while building

`UpdateRcsAgentRequest`'s fields are plain nullable values, so `{"website":
null}` and a body that never mentions `website` arrive here identically. **There
is therefore no way to clear a field once set.** We have not invented a
sentinel — that would put a second meaning on a value your contract says is
simply absent. If clearing matters, the shape is `nullable` plus an explicit
presence convention, and it is yours to declare.

---

## 4. §5 — object storage, and the one decision we made without asking

**Filesystem-backed with HMAC-signed expiring URLs, not S3.**

Every requirement you stated is met: signed, expiring, tenant-prefixed, never a
public read, and an `https` URL a carrier can fetch. What it does not add is an
object-storage dependency and a bucket that does not exist on a deployment we
would then have to provision under time pressure.

The trade, stated plainly: **the bytes live on one machine's disk.** That is
fine while there is one API node and wrong the day there are two. Moving to S3
is a swap behind `Put`/`Open` — the signing, the URL shape and the API do not
change. It is marked in the source as such rather than left for someone to
discover.

- **The tenant id is inside the signature, not in the URL.** So a signature
  cannot be replayed against another tenant's prefix, and the URL does not
  disclose which customer an asset belongs to.
- **No session is required to read.** That is the point of signing: a carrier
  fetching brand artwork has no Relay login.
- **The read route is not in the contract**, deliberately — you documented the
  URL as opaque, so its shape stays ours and no client can come to depend on it.
- **Uploads are OFF unless a root, a signing key and a public base URL are all
  set.** Any one missing and the endpoint refuses rather than half-working; a
  store with no signing key serves identity documents to anyone who guesses.

**Retention is not built.** You said you had no opinion on the period but did
have one that there should be one. Agreed, and it is a scheduled sweep rather
than part of this slice — tell us the period for `verification_document` and it
goes in. The column and the delete path exist.

---

## 5. §8 — option (a), and one correction to your reasoning

**(a): accept `rcsAgentId` on sender registration, valid only when
`channel = RCS`.** Declare it and we will wire it.

Your argument for (a) is right. Your argument against (b) is right for a better
reason than you gave: you said deriving would "quietly re-impose the one-agent
rule". It is worse than that — **deriving has no answer at all** for the ordinary
case in your own §1, a retailer holding one agent for order notifications and
another for marketing. Both are `IN`, both are the same tenant. There is nothing
to derive from.

**Your note about the silent direction is exactly right and we acted on it.** A
brand-new *optional* field on a request body is invisible to `make generate` —
nothing has to read it. We have already been caught by the sibling case this
week: `WebhookEndpoint.createdAt` compiled, served, and would have returned
`0001-01-01T00:00:00Z` on every row. So when you declare `rcsAgentId` on that
body, assume nothing on our side noticed, and we will wire it deliberately.

The column exists now (`sender_ids.rcs_agent_id`, nullable, with a CHECK that
only RCS may carry one) and is already returned on `SenderId`. Only the write is
missing, and only because the contract has no way to send it.

We would add one condition to your proposal: **the agent must be
`verification_approved` at registration time**, which you suggested and we agree
with — and it should be re-checked at send rather than only at registration,
since an agent can be suspended afterwards.

---

## 6. §4.3 — the webhook discriminator, and the index is in

The unique index is `(carrier, carrier_agent_id) WHERE carrier_agent_id IS NOT
NULL`, the same treatment `templates_carrier_identity` got, with the paired
CHECK that the id is present exactly when the status is not `not_submitted`.

**The webhook does not yet read it, and that is step 5, not an oversight.** The
index is in now because it is the thing that must exist *before* two tenants can
collide, not after: adding a unique index to a table that already has duplicates
is a migration that fails at 3am.

---

## 7. `segmentsPerMessage`, which arrived in the same push

`Campaign` and `CampaignEstimate` lost `segmentsPerMessage` and gained
`segmentsPerMessageMin`/`Max`. Three compile errors, exactly where your §3.1 said
they would be. **Your reversal was right** — pairing the addition with the
removal is what turned a silent response-field addition into a loud one.

Built to your spec: both ends measured on substituted text, `ASSUMED_VARIABLE_CHARS`
matched at **20**, and `costMinorMin`/`Max` spanning both causes — the segment
spread and the channel spread — rather than quoting a range beside a single
exact price.

**Your §4 defect is fixed and it was worse than you measured.** The two estimate
paths now share one function. The campaign wizard's old `+1` heuristic was wrong
twice over, and the second way is the expensive one: `{{first_name}}` is fourteen
characters no handset receives *and* its braces are GSM 03.38 extension
characters charged at two septets each — eighteen phantom septets — while `+1`
could only ever move the bound by one segment when three long variables move it
by two.

We would only ask that a change like this comes with a line in the covering
message next time. It was found by the compiler, which is the good outcome; it
was found while reading a document about RCS agents, which is the avoidable part.

---

## 8. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against `master@9a829c3`. 189 operations, up from 180.

Mutation-verified: dimensions recorded but not enforced (**red**, three cases);
a tenant-scoped read replaced by an unscoped one plus a comparison (**red** —
"That agent belongs to another account" is distinguishable from "No such
agent"); the segment range back to the written count plus one (**red**, five
cases).

**One mutation was neutralised and we nearly recorded a green as evidence.** The
first attempt at the isolation mutation read through the operator pool, which
falls back to the tenant-scoped pool in tests — so the "leak" leaked nothing and
the test passed. It only became a real mutation when pointed at the admin pool.
A mutation that cannot reach the behaviour is a green that proves nothing, which
is the third time this week the check needed its own check.

---

## 9. Open

- **§1.2** — drop `MULTI_USE`; declare the 40-character name limit and the
  contrast rule.
- **§1.1** — split `template_media` per surface if 1 MB is too tight.
- **§3.1** — no way to clear an optional field.
- **§5** — declare `rcsAgentId` on sender registration and we will wire it.
- **Retention period** for verification documents.
- **Steps 4–6** — threading the agent id through the connectors, per-tenant
  resolution, retiring the fallback. Not started; they are the ones that make a
  real message go out under a real customer's brand.
- **WP2** and **the IP allowlist** — unchanged.
