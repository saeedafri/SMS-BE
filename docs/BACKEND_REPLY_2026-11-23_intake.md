# Backend reply, 23 Nov 2026: intake (STEP 0 and STEP 1, before any code)

Answers `PROMPT_FOR_BACKEND_AGENT.md`: "reply with the list of packages you found in the delta and any
row you already believe is wrong, before writing code". No handler has been written yet.

## State measured

| What | Value |
|---|---|
| Frontend | `e5613f1f` (fast-forwarded from `upstream/master` today) |
| Backend | `3f9ad83`, working tree clean |
| `contract-delta.cjs` | 174 real differences, same as your count. Copy: `docs/frontend-handoff/CONTRACT_DELTA.md` |
| `diff-backend-spec.cjs` | 201 real differences (baseline) |
| `make generate` | **fails** before any of our code compiles. Blocker 1 below |

## Packages found in the delta

| Package | Differences | New paths |
|---|---|---|
| WP1 channel truth | 15 | 4 |
| WP2 failure reasons | 26 | 3 (4 operations) |
| WP3 Email on SES | 31 | 6 (7 operations) |
| WP4 WhatsApp | 47 (3 of them "only in theirs") | 4 |
| WP5 RCS carousels | 34 | 1 |
| WP6 SMS | 21 | 5 (7 operations) |

## Blocker 1: the contract does not generate (needs a change in `openapi.json`)

`FailureSource` gained `rcs_vendor`. oapi-codegen v2.8.0 (the version our generated file was made
with) names that constant `RcsVendor`, which collides with the existing schema type `RcsVendor`:

```
internal/gen/api/control.gen.go:6992:6: RcsVendor redeclared in this block
	internal/gen/api/control.gen.go:1133:2: other declaration of RcsVendor
internal/gen/api/control.gen.go:2388:14: RcsVendor (constant) is not a type   (and 8 more)
```

It is the only collision (`go build -gcflags=-e` lists no other). An overlay in our repo fixes the
build but shows up in your own gate, so the fix belongs in the contract:

```
$ node scripts/diff-backend-spec.cjs --backend ../SMS-BE     # with a local overlay
1 real difference(s):
/components/schemas/FailureSource/x-enum-varnames: only in theirs
```

**Ask:** add this to `components.schemas.FailureSource` in `openapi.json`. It changes nothing in the
TypeScript types and gives Go unambiguous names:

```json
"x-enum-varnames": ["FailureSourceGate", "FailureSourceSmppSubmit", "FailureSourceSmppReceipt",
                    "FailureSourceRcsVendor", "FailureSourceWhatsapp", "FailureSourceEmail",
                    "FailureSourceVoice"]
```

With that in place (tested via the overlay, then reverted), the whole repo compiles except the two
breaks WP4 predicted, nothing else:

```
internal/api/senders.go:74-76   gen.SenderId_MessagingTier, gen.WaMessagingTier, sender.MessagingTier undefined
internal/api/support.go:274,294,311,346   ReplyToConversation: request.Body.Body is now *string
```

## Rows we believe are wrong

| # | Row | Why | What we will build unless you say otherwise |
|---|---|---|---|
| W1 | Batch 5 brief §2.5, row **S21**, and the WP6 "Build" line name the key `DND_REGISTER_PROVIDER` | Your D-K says name it `DND_MODE`, so the two contradict each other | `DND_MODE`. Please change S21 and the WP6 Build line |
| W2 | WP-S B1: "a plain `operator` gets 403 on every money route" | None of these 11 operations declares a 403 in `openapi.json`: `suspendTenant`, `reinstateTenant`, `throttleTenant`, `creditTenantWallet`, `recordTenantPayment`, `voidTenantPayment`, `setTenantCreditLimit`, `updateDefaultRate`, `createRateOverride`, `editRateOverride`, `removeRateOverride`. The server can still answer 403 (an `errForbidden` sentinel maps to 403 with the standard envelope), but the screens are then handling a status the contract never promised | Add the gate with a 403 now. Please declare `403` on those 11 operations |
| W3 | WP-S A4: "stop returning the voice code; answer 501" | Your contract requires it: `VoiceCallResult` has `required: ["code"]`, and the description says it is "revealed directly in the mock response so the UI can display it". The 501 contradicts the contract | Answer 501 `not_implemented` on `POST /v1/sender-ids/{id}/voice-call`. Please declare the 501 there (or remove the operation) and drop `VoiceCallResult.code` |

Not wrong, noted so nobody is surprised:

- **D-K default.** Our plan 09 made `operator` the India default; your D-K keeps `deny` until X4 is
  answered. We follow D-K: `deny` stays the default and `operator` is a configuration switch.
- **D-A.** We build the compensation as a command that first prints the list (tenant, message count,
  amount) and posts compensating ledger entries only when asked to. Running it against the live
  ledger waits for the founder's yes on that printed list.

## WP-S: your findings, checked against our source

| Item | Your finding | Checked | Detail |
|---|---|---|---|
| B1 operator roles | `requireOperatorAdmin` has no call site | **Confirmed, and wider** | Its only callers are in `send_cap_gate_internal_test.go`. All 11 routes in W2 use plain `requireOperator`; `SuspendTenant` reaches it via `applyTenantChange` (`operator.go:594`). The gate came in at `93c4e05` with "the gate has no caller until the console route lands", and that route never landed |
| A4 voice code | returned in the 200 body | **Confirmed** | `senders.go:254`. And it matters: `voice_verified` is the only proof-of-ownership check before an operator can approve a voice caller ID (`senderReadyForApproval`, `operator.go:2476`). So anyone can claim any number |
| B3 SSRF | literal private IP check only | **Confirmed** | `webhook.go:141`; `HTTPClient` has no dialer of its own. On AWS a hostname that resolves to `169.254.169.254` (instance metadata) passes. `isInternal` also misses `100.64.0.0/10` and `0.0.0.0/8`. Fix: check every resolved address at dial time with `net.Dialer.Control` |
| B2 `/metrics` | public; does `Snapshot()` carry tenant data? | **Matched routes: no tenant id. New finding below** | Requests that match no route are recorded under their **raw path**: chi v5.3.1 gives an empty route pattern on a 404 (tested with a probe), and `logging.go:54` then falls back to `r.URL.Path`. Every distinct unknown path becomes a permanent entry in the `byRoute` map. Anyone, without signing in, can grow server memory without limit and have any text echoed back on a public page. Also, `RecordIncident("held_until_wrong", <campaign id>)` puts a campaign id on it. We will put every unmatched path under one key and drop the id, and keep `/metrics` public (your "your call") |
| B7, B8, B9 | no boot-time refusal of dev endpoints in production | **Confirmed in code; live values not read** | `config.go` has no notion of "production" at all (only `SMPP_ENVIRONMENT`). The live values could not be read today: the server is unreachable (below) |

## The VIDEOCON question (does the operator DND/DLT-scrub our binds?)

**Not known.** Nothing in our repo, our research or our notes answers it either way. It is item X4 in
our plan 09, and the founder owns it. No code can find out. Two ways to settle it:

1. Ask VIDEOCON in writing whether preference scrubbing on DLT applies to traffic on our binds, and
   which DLR error code a DND rejection comes back with.
2. Test it: with `DND_MODE=operator` on the test tenant, send one promotional SMS to a number known to
   be DND-registered. A DND rejection receipt from VIDEOCON means they scrub. That is a real message
   to a real handset, so it waits for the founder to pick the number and approve the send.

## What stops the build today

1. **Blocker 1** above (needs `x-enum-varnames` in your `openapi.json`).
2. **The AWS server `3.111.226.170` is unreachable since at least 23 Nov** (ports 22, 80 and 443 time
   out; the rest of the internet answers). `make test` runs on that server and `make test-local`
   tunnels to its databases, so no test can run and nothing can be verified live. The last deploy
   (`3f9ad83`, 3 Nov) succeeded. The founder is checking the project's state.

## Order once both are cleared

WP-S (B1, A4, B3, the `/metrics` fix, B7 boot guard), then WP1, WP2, WP6, WP5, WP3, WP4, each with its
own `BACKEND_REPLY_<date>_<package>.md` as the README asks.
