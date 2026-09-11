# Steps 4–6 are in, and the thing you could not test is now testable

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 11 September 2026
**Re:** your `REPLY_TO_BACKEND_2026-09-11.md`

Everything in your §6 list is done except the retention sweep, which we agreed
is a separate slice. Two of the answers are not the ones you asked for, and both
are better; one of them is a correction to a number you gave us today.

Read §1 and §6 if you read nothing else. §1 is the one that changes what a
handset shows. §6 is a finding about your own §7 that turns a nicety into a
control.

---

## 1. Steps 4–6 — per-tenant agent resolution

Regenerated from `4532037` first. Both predicted silent changes were silent:
`MULTI_USE` vanished from the generated enum and the build stayed green, and
`rcsAgentId` appeared on two request structs with nothing reading either. §4 and
§5 are what we did about the second one.

### 1.1 One commit, not three, and we are telling you rather than pretending

You asked for step 4 as a behaviour-identical commit "worth reviewing
carefully". It is not one, because steps 5 and 6 landed on top of it and the
intermediate state was never a thing anyone ran.

Reconstructing it afterwards would have produced a commit we never tested,
presented as the one we did. The three steps are separable by *file* if that
helps a review: step 4 is `internal/connector/*` and `cmd/control-api/main.go`,
step 5 is `internal/store/rcs_agents.go` and `rcsAgentFor`, step 6 is
`internal/domain/messaging/gate.go`.

### 1.2 The agent is a parameter now, and the comment defending the old way was wrong

`AirtelRCS.AgentID` and `ViRCS.BotID` are gone. The identity travels per call:
`Capability(ctx, agentID, msisdn)`, `Reachable(ctx, agentID, msisdns)`,
`RegisterTemplate(ctx, agentID, spec)`, and `Submission.AgentID` on the send.

Per **submission**, not per connector, and not per batch. One batch can hold two
senders belonging to the same tenant with different agents, and a connector-held
identity cannot express that at all — the second message would go out under the
first one's brand, silently. There is a test that sends two messages in one
batch under two agents and reads both off the wire.

The interface's own comment used to argue the opposite: *"The agent identity is
configuration held by the implementation, not a parameter: a caller that had to
supply it could supply the wrong one."* That was correct while one agent served
the deployment and is exactly wrong now. It has been replaced rather than
deleted, with the reason it stopped being true.

### 1.3 Resolution, and there is nothing left to fall back to

At send: `sender.rcs_agent_id` → the agent's `carrier_agent_id` for the carrier
this message routes over. Three conditions, all re-checked at send rather than
trusted from registration:

- the launch is `approved` and carries an id;
- the agent is not `suspended` or `archived`;
- the agent belongs to this tenant — enforced by RLS, so a stolen id reads as
  "not found" rather than as a leak.

It goes through the send path's existing hot cache, like the sender and the
tenant status. Misses are cached too, or a campaign refusing every message pays
a round trip per message to keep refusing it. The TTL is the blast radius and it
is the same one already accepted for a suspended tenant: an agent suspended at
`t` keeps sending until `t+TTL`.

**The negative test you asked for is there, and it is stronger than the one you
specified.** You asked that a tenant with an agent never resolve to the config
value. The dangerous case is not "no agent" — it is an agent that *exists and
cannot be resolved*, so the test covers three of those: a launch still pending,
a launch on a different carrier than the route chose, and an agent suspended
after its sender was registered. All three must refuse.

We mutated `rcsAgentFor` to fall back on error and confirmed all three go red.
A test that cannot fail is the one thing worse than no test here.

### 1.4 Step 6 refuses at the gate, not at the connector

The connectors *also* refuse an empty agent, without calling the carrier. But
leaving it there would have reported our own refusal as `carrier_rejected`, and
this codebase already has a written rule against that — it is the reason
`ErrCarrierTemplateNotApproved` is separate from `ErrTemplateNotApproved`:
telling a customer the carrier refused them sends them to argue with Airtel
about an agent Airtel has never been asked to review.

So: `rcs_agent_not_resolved`, at the gate, before any money moves. `status:
"rejected"`, `costMinor: 0`, carrier never called.

**One deliberate exception.** The agent is required only where a real RCS
gateway is configured. With none — which is this deployment today — RCS goes to
the sandbox, no handset sees a brand, and demanding an agent would refuse every
RCS send on a deployment that has not got carrier credentials yet.

**What this breaks, so you hear it from us:** every RCS sender now needs an
agent. There is no shared identity left to fall back to. Six of our own e2e
tests failed on this change, which is the correct number — they modelled the
world you are replacing.

The sixth is worth the paragraph, because it is the shape this failure takes
everywhere. Five broke in the package where agents live and said so. The sixth
was a campaign test two packages away whose only complaint was **`carrier saw 0
submissions, want 2`** — a true statement that names neither agents nor the
gate. The refusal happens before the carrier is called, so a test that asserts
what the carrier received sees an empty list and nothing else. If one of your
own fixtures goes quiet rather than red after you wire `rcsAgentId`, that is
where to look: the sender has no agent, and the gate is doing its job.

**And one thing step 6 quietly required.** `RCS_AIRTEL_AGENT_ID` and
`RCS_VI_BOT_ID` were *mandatory* at boot: a deployment selecting Airtel without
one refused to start. That was right while the agent was a connector
credential, and it would have made the end state of this work unreachable — a
deployment where every tenant owns its agent and nothing shared is left would
have failed config validation. They are now optional, and there is a test
asserting a carrier starts without one. Worth knowing if you ever read that env
var as required in a runbook.

### 1.5 Webhook attribution, and a Vi detail that would have failed silently

Inbound is attributed by the carrier's agent id through
`rcs_launch_carrier_identity`. Two things found on the way:

**Vi sends TWO agent identifiers and only one of them is ours.** The Pub/Sub
envelope carries `attributes.business_id`, which Vi's §3.8.2 states plainly:
*"business_id -> botId provided by the platform"*. That is the botId we put on
every request and therefore what `carrier_agent_id` holds. The payload *also*
carries its own `agentId`, which is Google's RBM address
(`…@rbm.goog`) — a value we never send and never store.

Matching on `agentId` would have looked right in review and found nothing, on
every event, forever. The only symptom would be inbound RCS that is never
attributed to anyone. We match on `business_id` and fall back to `agentId`
trimmed, because Vi's own examples carry a leading space in it.

**The delivery path was never at risk.** Your original §4.3 said "without it one
customer receives another's delivery receipts". A delivery report resolves
through the carrier reference *we* stored at submit, which carries the tenant
with it — so that path was already right and we have not changed it. Inbound is
the one with no message of ours to look up, and that is the one now closed.

---

## 2. `template_media` — raised, but not to the number you gave us

You asked for 2 MB. We are shipping 2 MB **for images and 10 MB for video**,
because a single number cannot say what Vi says.

Vi Template Management **p94**, all three standalone rich-card orientations:
*"max file size of 2MB … If you are uploading a video, the max file size is
10MB."* Vi's card limits are per **medium**, not per surface.

And `RcsCard.mediaUrl` in your own contract is described as *"https URL of the
card image **or video**"*. So at a flat 2 MB we would have gone on refusing
every legitimate card video at a fifth of its allowance — while telling the
customer the limit is 2 MB, which is true of images and false of the file they
are holding. That is the §2 failure again with a different number.

Your reasoning for dropping the carousel cap is right and we have taken it: 1 MB
is the carousel figure (p95), `RcsContent` declares text and card only, so
nothing this product can produce lands on a carousel. When a carousel exists it
becomes two purposes, not one smaller number — by then both surfaces are
reachable and a single limit is wrong for one of them whichever value it takes.

**So the rule about client limits cuts both ways.** Yours may stay at 1 MB and
be safe. If you raise it, raise it to 2 MB for images and 10 MB for video, not
2 MB flat — a flat 2 MB would now be *stricter* than the server for video,
which is safe, and wrong, and will have someone re-encoding a file that was
fine.

**A finding while doing it.** `TestTheCarrierSizeLimitsAreTheOnesEnforced` — the
test that proves the 50 KB logo limit — had been **skipping itself**. Its
fixture built a 224×224 image from `x*y, x^y, x+y`, which looks like noise and
compresses beautifully: 35 KB against a 50 KB limit, so the test called
`t.Skip` and reported SKIP, which reads as a pass in every summary we have. It
now uses genuinely random pixels and fails loudly if the fixture ever stops
straddling the boundary. Worth checking whether anything in your suite is built
the same way.

---

## 3. `rcsAgentId` on sender registration — wired, and proved stored rather than echoed

Accepted on `POST /v1/sender-ids`. Refused with 422 and a reason when:

- the channel is not RCS — *"An RCS agent can only be attached to an RCS
  sender."* Without this the `sender_ids_agent_is_rcs` constraint refuses the
  write as a 500 with nothing a customer could act on.
- the agent has not passed verification — the wording names which of the two
  states it is in.
- the agent belongs to someone else — *"No such RCS agent."* Deliberately the
  same answer as an agent that does not exist; anything else confirms that a
  given id belongs to somebody.

Allowed states are `verification_approved`, `launch_pending` and `live`. And as
you said, re-checked at send — §1.3.

The test reads the row back out of Postgres rather than trusting the response
body, because a handler can return what it was given and still never have
written it. That is the failure this field was most likely to have.

---

## 4. Merge Patch — taken, and it cost almost nothing

Explicit `null` clears, an absent key leaves the value alone. No `clearFields`.

You offered to take the uglier option if presence detection was expensive. It
was not, because **this codebase already had it and nobody had ever called it.**
The `rejectUnknownFields` middleware already read the raw body, already recorded
which keys the request carried, and already carried a `bodyMentions` helper with
a comment describing precisely this problem. The agent PATCH just had to be
added to its route list.

Three implementation notes worth having:

- **`displayName` and `useCase` cannot be cleared.** Neither is nullable in the
  table, and an agent with no name is not an agent. A null on either is ignored
  rather than refused; say if you would rather have a 422.
- **One side effect worth knowing about.** That middleware does two jobs, and
  registering the route turned on both: the agent PATCH now also **refuses
  unknown fields with a 422**, where it used to ignore them. That matches the
  other PATCH routes and matches what the contract declares, but if you are
  sending a field that is not in the schema you will hear about it now. The
  accepted set is exactly the twelve properties your PATCH body declares.
- The SET clause is fixed SQL with a clear-flag per column, not assembled from
  field names. `coalesce` carries two of the three cases and cannot carry the
  third — a NULL argument means "keep", so it can never also mean "clear" — and
  building the statement from strings would put caller-influenced text into SQL.

---

## 5. The two configuration items

### 5.1 Vi is configured. Jio stays, and that is not the same defect

`('IN','RCS','VI','Vi RCS Direct', priority 3)` is on production and in the
fixture. Your derivation was right and so was the finding.

**Jio is deliberately left.** Those are two different situations and only one of
them is a bug:

- **Vi absent** — a carrier we *can* reach, hidden. The launch screen could not
  offer the one Indian network we have a working adapter for. That was the
  defect and it is fixed.
- **Jio present** — a carrier we *cannot* reach, shown. That is the designed
  behaviour: `rcsAgentResponse` shows every carrier in the country at
  `not_submitted`, and a launch against one we hold no integration for is
  refused with *"We hold no JIO integration yet… Your agent is unaffected on
  every other network."* A customer whose reach looks short can see which
  network is missing. Hiding it would make an unsupported network
  indistinguishable from one that does not exist.

The cost on the Vi row is Airtel's figure standing in. Vi commercials are not
agreed. That column feeds operator margin screens and **never a customer's
bill** — `pricing_rates` does that — so the placeholder misstates our own
reporting and nobody's invoice.

**US/GB/AE resolve to one carrier each and that is correct-but-bleak.** Verizon,
EE, Etisalat — and we hold an adapter for none of them. Those screens will show
exactly one carrier, permanently `not_submitted`, forever, with an honest
refusal behind it. It is not a route-table gap; it is the true state of our
integrations outside India. Worth knowing before someone reads three identical
dead screens as three bugs.

### 5.2 `RCS_VENDOR` is still unset

Unchanged, and agreed it is the honest state. Noted so nobody reads the `503` as
a defect.

---

## 6. Your §7 — we agree, and the reason is stronger than either of ours

Option (a): `vendor` on the request, required when a code is attached. Declare
it and we will take it.

**But your framing undersells it, and so did ours.** You wrote that a field
which is sometimes wrong is worse than one that is honestly empty. For this
field, an honestly empty one is not merely unhelpful — it silently disables a
tenant-isolation control.

`templates_carrier_identity` is `UNIQUE (carrier_vendor, carrier_template_id)
WHERE carrier_template_id IS NOT NULL`. It is the index that stops two tenants
colliding on a carrier's template id and receiving each other's approvals.
Postgres treats NULLs as **distinct** in a unique index by default, so two rows
of `(NULL, 'same-code')` both insert happily. Verified on the deployment's own
PostgreSQL 16:

```
INSERT INTO nulltest VALUES (NULL, 'shared-code');   -- INSERT 0 1
INSERT INTO nulltest VALUES (NULL, 'shared-code');   -- INSERT 0 1
rows with the same code and a null vendor: 2
```

So "leave vendor null rather than guess" is safe in your mock, where nothing
enforces uniqueness, and would remove a real control here.

**And our live API does not return null on that path today — it guesses.**
`attachCarrierTemplate` fills `vendor` with `s.RCSCarrier.Vendor()`, the
deployment's single configured carrier. With one carrier configured that is
usually right and is exactly the guess you asked us not to make: a code pasted
from the *other* carrier's portal gets labelled with ours.

We are leaving the guess in place until the request field exists, because the
alternative — nulling it — is the worse of the two for the reason above. It is
the lesser evil, not a defensible answer, and it stops being either the day you
declare the field.

---

## 7. The rest of your list

**`MULTI_USE` dropped** — migration `00047` removes it from
`rcs_agents_use_case_known`. Production holds four agents, all `TRANSACTIONAL`,
so the tightening was clean. A row carrying it would have failed the migration
loudly, which is correct: an agent no carrier will accept should not be migrated
quietly forward. You were right that nothing on our side would have reported the
enum member leaving — the check constraint was the only thing that would have
noticed, and it still admitted the value.

**`displayName` 40 and the 4.5:1 contrast rule — we will own both.** You offered
to keep the contrast check client-side; keep it, as the fast one. Both are now
enforced server-side as well, at edit *and* at verification submission, which is
the moment Airtel would refuse. Two details:

- The name limit counts **runes, not bytes**. A name in Devanagari is inside 40
  characters and well over 40 bytes, and `octet_length` would have been a rule
  about our encoding rather than about the carrier's. There is a `char_length`
  constraint under it in the same migration.
- Contrast is WCAG 2.x relative luminance with the piecewise sRGB
  linearisation, not a gamma approximation — the approximation is wrong near
  black, which is the end of the range a contrast check has to be right about.
  If your numbers and ours ever disagree on a colour, that is where to look.

**Retention** — not in this slice, as agreed.

**A second account, so isolation is provable from outside:**

```
founder@northwind.test / relay-dev     Northwind Logistics (tenant aaaaaaaa-1111-…)
```

Northwind already existed as an operator fixture; what it lacked was a way in.
It now has an owner login, an approved IN entity, and a **live RCS agent with an
approved AIRTEL launch** (`northwind_airtel_agent`) — because an agent is the
one object where a mistake is invisible from our side and visible on a
stranger's handset, so the proof wants one on each side of the boundary.

Two tests cross that boundary now: an inbound event carrying one tenant's
carrier agent id resolves to that tenant and never the other, and the database
refuses outright when two tenants try to take the same carrier agent id.

---

## 8. Two gaps your own screens have already run into

Neither blocks anything. Both are places where an endpoint is tenant-scoped and
the screen calling it is agent-scoped.

### 8.1 `POST /v1/rcs/capabilities` takes no agent

Your `reach-check.tsx` sits on an agent's detail page, and its own comment says
why: *"an agent that has not launched on the subscriber's carrier makes a
perfectly RCS-capable phone unreachable, so the same list gives different
answers for different agents."*

That is exactly right, and the request body declares only `msisdns`. So the
answer that endpoint gives is about the deployment's shared agent and cannot be
about the one whose page the customer is looking at. We pass the shared agent
and have said so in the source rather than letting it read as per-tenant.

The field is missing, not unwanted — your screen already knows which agent it
means.

### 8.2 `POST /v1/templates/{id}/carrier-registration` takes no agent either

Same shape. A carrier scopes a template to one agent. A template registered
under the deployment's agent and sent under a customer's own is refused at the
gateway with **"Template not found"** — which is the failure your §6 described
from a different cause, arriving hours later with nothing connecting the two
events.

We are not deriving it. A tenant may hold several agents, and picking one would
be the reasoning you and we both rejected in §3.4 and §7.

This one is more urgent than 8.1, because it is the step that stops a real
message going out under a real customer's brand after steps 4–6. If you would
rather we proposed a shape, say so and we will send one.

---

## 9. What we would like next

1. Declare `vendor` on the carrier-registration attach request (§6). Small,
   and it restores a control rather than adding a nicety.
2. Decide on `rcsAgentId` for `POST /v1/rcs/capabilities` and for
   `POST /v1/templates/{id}/carrier-registration` (§8). The second is the one
   that blocks a real send.
3. Tell us whether a `null` on `displayName` or `useCase` in a PATCH should be
   a 422 rather than ignored (§4).
4. Confirm the single-carrier corridors are intended (§5.1).
