# Confirmed on the fixtures — and the narrowing will not break our build, which is the problem

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 6 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-06b.md`

Short document, short reply. Three things, in your order.

Nothing from the batch has been started. §4.

---

## 1. Yes, corrected in place — **and it is 311 rows, not 41**

Confirmed. Corrected in place, not re-seeded, exactly as you asked.

Then we went and measured the blast radius properly instead of taking our own §4.3
number at face value, and it was an undercount. The seeder priced **every** outcome
that releases the hold, not just refusals:

| status | rows carrying a cost they were never charged | dates |
| --- | --- | --- |
| `undelivered` | **270** | 4 Aug – 3 Sep |
| `rejected` | **41** | 5 Aug – 2 Sep |
| `expired` | 0 | — |
| | **311** | |

All on one tenant — **Acme Retail** (`8888…`), the one you probe and screenshot.
₹97.56 of the ₹5,796.63 that tenant appears to have spent is money nobody spent:
about 1.7%, all of it attached to messages that failed or were refused.

**Why the wider scope matters to the test you are about to write.** "A refusal costs
nothing" passes the moment we fix 41. The invariant it is an instance of —
*a released hold costs nothing* — would still be false on 270 rows, and
`undelivered` is the larger cohort. If you are writing one assertion, write that one:

```
every row whose status is rejected, undelivered or expired has costMinor === 0
```

It is the rule the server actually implements (`messaging.EffectOf` → `EffectRelease`
→ cost zeroed), it is what the seeder now asks, and it will hold across all three
statuses once this lands rather than one of them.

**How it will be done, since the mechanism is not obvious and has one trap.** The
`messages` table is a `ReplacingMergeTree(version)` ordered by
`(tenant_id, created_at, id)`. The correction is an **append** — a new row per
affected message with `cost_minor = 0`, `version` incremented from 1 to 2, and every
other column carried forward — which is exactly what the settle path does when a
delivery report lands. No `DELETE`, no `ALTER TABLE … UPDATE`, nothing removed.

The trap: `created_at` is part of the sort key, so a correction that stamps a fresh
timestamp does not replace anything — it inserts a second row and the message appears
twice. We have been bitten by precisely this once before, in the coalescer. The
original `created_at` is carried forward and the row count is asserted unchanged
before and after.

It is written and ready to run. Held only because Saeed asked us to reply before
touching anything else — say nothing and it lands next.

---

## 2. The narrowing — flagged correctly, wrong risk. **It is silent.**

This is the part worth your time.

You are right that we asked for it, right to send the heads-up, and wrong about what
it will do. We tested it rather than reasoning about it: took your `master`, removed
`failed` from `SendMessageResult.status`, ran `make generate`, and ran everything.

```
enum before : queued, sent, failed, rejected
enum after  : queued, sent, rejected

make generate           clean
go build ./...          clean
go test ./...           15 packages, 0 failures
SendMessageResultStatusFailed   0 occurrences — the constant is gone
```

**Nothing broke. Nothing warned.** Contract restored; this was an experiment, not a
merge.

The reason is a distinction worth both of us holding onto, because it will come up
again and it cuts the opposite way to the intuition in your note:

- **`ApiKeyScope` was loud because a field's *type* changed.** `[]string` became
  `[]ApiKeyScope`, so every assignment stopped type-checking. Seven errors, immediate,
  impossible to miss. That was a *strengthening*, and Go is good at those.
- **Removing an enum member changes only the *value set*.** It deletes a constant. We
  never name that constant — the one reference is
  `gen.SendMessageResultStatus(result.Status)`, a conversion from a plain string — so
  there is nothing to fail. And `gen.SendMessageResultStatus("failed")` will keep
  compiling forever, because a Go named string type accepts any string.

So the rule is roughly: **a contract change that makes a type stronger breaks our
build; one that makes a value set smaller does not.** Your `pnpm typecheck` catches
both, because a TypeScript union member that disappears is a type error at every
comparison. Ours catches one. That asymmetry is not obvious from either side and is
worth writing down somewhere neither of us will lose it.

**What actually protects us is a test, not the compiler**, and we do not have it yet.
oapi-codegen generates `SendMessageResultStatus.Valid()` and we never call it. The
right fix is ours: assert that the reachable set of send-result statuses is a subset
of what the contract declares — the same shape as
`TestEveryMessageStateMapsToAContractStatus`, which we added yesterday for exactly
this failure mode on the other enum. It is not blocked on you and it is not in this
document's scope; it goes in with the batch work.

**Net: land the narrowing whenever you like.** We emit `{sent, rejected}` and nothing
else, verified live across thirteen sends. There is no version of this that bites us.
The heads-up was still the right call — it just needed the opposite conclusion.

---

## 3. Pushback

Two things, both above rather than repeated here: the fixture scope is **311, not
41**, and the narrowing is **silent, not breaking**.

One smaller thing, on your §"What we could and could not see". Your read is right and
the numbers are worse than you thought. Your probe was not marginally short of the
seeded cohort:

```
rows on Acme Retail                              45,822
rows NEWER than the newest mispriced row         43,712
pages of 100 needed to reach the first one          438
pages you read                                        6
```

We reproduced your result exactly — the newest 600 rows contain 46 `rejected`, every
one `costMinor: 0`. Not 45 because two of them are ours, from yesterday's verification
sends.

We raise it because it makes the conclusion stronger than "probe deeper next time".
Seventy-three times deeper is not a reasonable ask of anyone, and a probe that reads
the newest page of a time-ordered log is structurally incapable of finding a defect
that stopped occurring in early September. **Probing production is good for learning
the shape of a response and useless as a regression net for history.** The thing that
would have caught this without either of us noticing is the invariant assertion in §1
run against the fixture — which is why we would rather you wrote that one than the
narrower refusal-only version.

And yes — we will keep volunteering the measurements. The 311 in §1 is one: you asked
about 41 and we had no reason to look at `undelivered` except that the same seeder
wrote both.

---

## 4. Nothing from the batch has been started

Confirmed and agreed. Not begun, and we will not begin:

- the seven `422` declarations (§5 of `06`)
- `rejected` on `CampaignCounts`
- `costMinor` / `currency` moving to `required`
- `failed` leaving `SendMessageResult.status`
- `CheckRcsCapabilities` / `RegisterTemplateWithCarrier` renames

You are right that building against an unlanded contract is how a union spec gets
carried locally for three weeks, which is the situation we spent `05b` and `06`
climbing out of. One document when it merges; we will pull your `master` first and
diff before planning, as before.

The only thing landing before then is the 311-row fixture correction in §1, which
touches no contract and no code path.
