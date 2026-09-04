# Reply — template binding on the submit path

**To:** Relay frontend
**Date:** 5 September 2026
**Status:** Built and deployed. Items A and C of the DLT spine turn out to have
been done already. Section 4 answered — with a decision, as asked.

---

## Section 3 first, because it was your blocker: A and C are already built

> *"`Template.registrationId` and `Template.dltCategory` — **Not built.** Live
> templates return neither field."*

**Both are built and have been for some time.** `POST /v1/templates` accepts
`registrationId` and `dltCategory`, stores both verbatim, and returns them on
create and on read.

What your probe saw is real and is also correct: **both keys are omitted entirely
when the value is null**, and every seeded template has null in both. Neither is
in `Template.required` in the contract — they are declared as optional, nullable
strings — so omitting them is contract-legal and your generated type already has
them as `registrationId?: string | null`. There was nothing to see because the
fixtures carry no DLT ids, not because the fields do not exist.

If you would rather have the keys always present with `null`, that is a
requiredness change on your side and we will emit them; say so and it is a
one-line change here.

`TestATemplatesDltIdentifiersRoundTripThroughTheApi` now asserts the whole
round trip through the API rather than through the columns, so this cannot be
in doubt again:

```
POST /v1/templates  {"registrationId":"1207161234567890123","dltCategory":"TRANSACTIONAL", …}
→ 201 with both fields returned exactly as sent
GET  /v1/templates  → both fields still exactly as sent
```

**Item D, confirmed from our side as you asked:** nothing generates a registration
id, and approval never overwrites a supplied one. There *was* a database trigger
that minted `REG-<HEADER>-0001` the moment a sender or registration reached
`approved`; it was removed, and `TestASuppliedDltIdSurvivesApprovalByteForByte`
guards the sender path against its return. The regulator issues these to the
customer; we only ever store what they hand us.

So the ordering you proposed — finish A and C, then build Section 2 — collapsed to
just building Section 2, which is done.

## Section 2 — the gate

Implemented in the shared gate, so it applies to `POST /v1/messages`, to campaign
fan-out and to the batched send path identically. A rule that is weaker on one
dispatch path is not a rule.

- **2.1** A send to a country whose regime requires a registered template, with no
  `templateId`, is refused with `registered_template_required` at `costMinor: 0`.
- **2.2** The body must be a legal instantiation: the template's registered text is
  split on its `{{variables}}`, and the submitted text must carry the remaining
  fixed segments in order, **anchored at both ends**. Not a substring check —
  `"Hi Priya, your order has shipped. WIN FREE CASH NOW"` is refused against
  `"Hi {{first_name}}, your order has shipped."`, which is the case your document
  names.
- **2.3** Template must belong to the caller's tenant and to the sender being used,
  and be approved. All three refuse.
- **2.4** Driven by the regime, not a country branch. `RequiresRegisteredTemplate()`
  is a method on the regime interface; India returns true, the US and the stubs
  return false. Adding the UAE is an entry in that package.
- **2.5** Refused before charging. The binding check sits before the balance check,
  so a refusal never debits and never reports `insufficient_balance` for what is
  actually a template problem.

`internal/domain/messaging/template_binding_test.go` covers the matcher itself —
seventeen cases including variables at either end, repeated fixed segments,
unfilled placeholders and unclosed braces. `internal/api/submit_template_binding_test.go`
covers it end to end through the API.

### One limitation, stated rather than hidden

**An RCS template's registered text lives in `rcs_content`, not in `body`.** We
resolve it from there, and the binding works on RCS. But if a channel ever carries
its content somewhere we cannot read, the body check is skipped for it — the
template requirement still stands, the text comparison does not. Refusing every
send on a channel because we could not find its registered text would be an outage
dressed up as compliance.

### What this changes for you, immediately

**Every India send now needs a `templateId`.** The demo tenant's existing
integrations that send bare bodies will start coming back `rejected` with
`registered_template_required`. That is the intended behaviour and it is the whole
point of the document, but it will look like a regression the first time someone
hits it, so it is worth saying out loud.

## Section 4 — the `202`, decided

**We are keeping `202`, and we want the contract to say so explicitly.**

You said you did not mind which, and preferred `422`. We went the other way, for
one concrete reason: **a refused send is a recorded message with an id.** A gate
refusal writes a real row — status `rejected`, the reason, zero cost — so the
customer can ask "why didn't this arrive" and get an answer, which is the opaque
behaviour this product exists to replace. The response carries that id, the
failure code, the segment count and the currency. An `Error` body carries none of
it, and a client that got a `422` would have a message in its log it could not
look up.

Your objection is right though, and it is not answered by keeping the status: an
SDK or a retry policy keying on the HTTP status alone will record a refusal as a
success. So:

- Please write the endpoint description as **"202 means the request was
  well-formed and we have decided. Read `status`: `sent`, `queued` or `rejected`.
  A `rejected` result is terminal and carries `errorCode`."**
- We would rather that than a status code that collapses "we refused it" and "you
  sent us nonsense" into one — a malformed body is still `422`, and that
  distinction stays meaningful.

If you disagree after reading that, say so and we will change it — it is a small
change and it is your contract. But make the call knowing the id goes away with it.

**`currency: ""` is fixed.** Refusals that never got as far as a rate — no rate for
the corridor, content the country bans, template not found — now report the
regime's currency for the sender's country, falling back to the tenant's. The
contract makes `currency` a required non-null string, so `null` was not available
without a change on your side; if you would rather have `null`, declare it nullable
and we will emit that instead. `TestARefusalStillNamesACurrency` guards it.

## Section 5 — what you are not asking for

Agreed on both, and noted as ours to raise when the operating model is decided:
TRAI time-band enforcement and DND/NCPR scrubbing are both real, both need the
telemarketer-versus-aggregator decision first, and our suppression list is a
tenant-level opt-out that must not be mistaken for the national registry. We have
not touched either.
