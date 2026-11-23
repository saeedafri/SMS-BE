# Backend reply, 23 Nov 2026: where every package stands

Read this first. Each package has its own reply beside it (`BACKEND_REPLY_2026-11-23_<package>.md`).

## The one fact that limits every reply

The AWS server (`relay-aws`, 3.111.226.170) has not answered on ports 22, 80 or 443 since
23 Nov (`ssh: connect to host 3.111.226.170 port 22: Operation timed out`, re-checked after WP4).
The test databases and the deployment both live there. So, for **every** package:

- The code is written and committed **locally**. Nothing is pushed or deployed.
- Every acceptance row has a test, or is marked "no test" below. Tests that need Postgres or
  ClickHouse **have not run yet**: locally they skip (no `TEST_DATABASE_URL`), and `make test` runs
  them on the server. Pure unit tests (domain rules, config, webhook dialer) **have run and pass**.
- There is no request and response from the deployment for any row yet. Each reply says which
  test will produce it and leaves the evidence column "not run".
- Every channel is at most **code-complete on the fake**. None is live-verified.

When the server is back: apply migrations 00080 to 00087 and ClickHouse 007 to 010 to the test
databases, run `make test`, fix what fails, run the mutation checks, push, confirm the restart,
then run each row against the public API and fill in the evidence.

## Commits (local, on `main`, not pushed)

| Package | Commit |
|---|---|
| Intake docs | `0191fb5` |
| WP-S and the codegen fix | `fffd42a` |
| WP1 channel truth | `99b75c8` |
| WP2 failure reasons | `22a5b28` |
| WP6 SMS | `8087ce2` |
| WP5 RCS | `2f4d047` |
| WP3 Email | `83c9f33` |
| WP4 WhatsApp | `31ec59f` |

## Both scripts, at `31ec59f`, frontend `e5613f1f`

```
$ node scripts/contract-delta.cjs --backend ../SMS-BE
- 0 real differences. 0 further differences are description text only and are not listed
exit=0

$ node scripts/diff-backend-spec.cjs --backend ../SMS-BE
paths 212 theirs / 212 ours, schemas 372 / 372
codegen normalisation, not reported: {"operationIdCase":267,"requiredFalse":202,"refSibling":5,"zeroMinItems":1}
no real difference: their build compiled our contract as it stands
exit=0
```

No operation answers 501 any more except `POST /v1/sender-ids/{id}/voice-call`, on purpose (WP-S A4).
Thirty-nine operations still have their generated 501 stub, and every one is shadowed by a
hand-mounted route registered after the generated ones (checked by mapping each stub to its path).

## Codegen: fixed on our side, no contract change needed

`make generate` now renames the clashing constant after oapi-codegen runs
(`RcsVendor` → `FailureSourceRcsVendor`, only in the `FailureSource` enum). The embedded spec is
unchanged, which is why `diff-backend-spec` reads 0. The `x-enum-varnames` ask in the intake reply
is no longer needed.

## Not declared in the contract, but answered (please declare, or tell us to change)

| Where | What we answer | Why |
|---|---|---|
| 11 operator money and standing routes | 403 `forbidden` for a plain `operator` | WP-S B1 |
| `POST /v1/sender-ids/{id}/voice-call` | 501 `not_implemented` | WP-S A4; the contract still requires `VoiceCallResult.code` |
| `POST /v1/campaigns` | 422 `sending_paused`, `messaging_limit_reached`, `template_not_approved`, `onboarding_incomplete` | Batch 2 §3, Batch 3 §3.6 |
| `POST /v1/conversations/{id}/reply` | 422 `outside_customer_window`, `template_not_approved`, `variables_missing`, `onboarding_incomplete` | Batch 3 §3.9 |
| `POST /v1/templates` | 429 `rate_limited` (100 WhatsApp templates an hour) | Batch 3 §3.7; your mock answers it too |
| `PATCH /v1/operator/tenants/{id}/channels/{channel}` | audit actions `tenant.channel_enable`, `tenant.channel_disable` | not in `AuditAction` |

## VIDEOCON and DND (your X4)

Still unknown. Nothing in our code or our records says whether VIDEOCON scrubs our binds against
the DND register or DLT. `DND_MODE` stays `deny` (your D-K) until Saeed has the answer from them.
