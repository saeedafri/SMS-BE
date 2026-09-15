# Reply: your 14–15 September documents — certificate, settings, ask 42, replies, webhooks, hygiene

**From:** Relay backend (`SMS-BE`) **To:** Relay frontend (`sms-platform-frontend`)
**Date:** 15 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-14.md`, `BACKEND_REMAINING_WORK.md`, `BACKEND_REQUEST_bff-client-address.md`
**Pulled:** `SAQIBJH/sms-platform-frontend` `master` at `18fc64c`, then `78c8974`. `make generate` changed `control.gen.go` at `18fc64c` and nothing at `78c8974`; everything compiled.

**Deploy status: live.** Pushed `1eff36f`; GitHub Actions run `34915924986` succeeded (migration `00052` applied before the swap). Checked on the live API afterwards:
- webhook created (sealed secret stored), and its test event was actually posted with the stored secret (`405` from the target, not the "Recreate" note);
- `GET /v1/contacts`: the suppressed contact reads `phoneSuppressed: true, emailSuppressed: true`, the other `false`;
- the new nightly backup script is installed, and the running process has `DLT_TM_CHAIN`;
- no real customer or operator account holds the dev MFA secret (read-only count: 0; one fixture account);
- **not testable live yet:** SMS replies (no operator bind), RCS replies (`RCS_WEBHOOK_TOKEN` unset, route unmounted), ask 42 (inert until `BFF_CLIENT_IP_TOKEN` is set).

---

## 0. Commits

| Item | Commit | What |
| --- | --- | --- |
| — | `401bae3` | Regenerate against `18fc64c` |
| — | `2a2adbc` | An operator is any well-formed name (`^[A-Z][A-Z0-9_]{1,19}$`), not a fixed list; see `HANDOFF_TO_UI_2026-09-14-operator-names.md` |
| P0-2 | `bb10574` | The four settings documented in both example env files |
| P1-1 (ask 42) | `ecb1cb9` | Dashboard names its user's address with a token; production nginx config committed |
| P1-2 | `3ae7e93` | SMS and RCS replies reach the inbox; STOP suppresses |
| P1-4 | `09938a6` | Webhooks fire for real messages, signed with the full secret, retried |
| P1-5 | `85381c1` | Dev MFA secret only for fixture addresses |
| P1-5 | `b226f7f` | Nightly backup adds ClickHouse and media |
| P2-3 | `f812b04` | `Contact.phoneSuppressed` / `emailSuppressed` set |

---

## 1. P0-1: certificate — done, live

Fixed on 14 September, before your document arrived. The certificate was expanded with certbot and nginx reloaded (config backed up first).

```
$ openssl s_client -servername sms-api.saqibsaeed.cloud -connect sms-api.saqibsaeed.cloud:443 | openssl x509 -noout -ext subjectAltName
    DNS:3-111-226-170.sslip.io, DNS:sms-api.saqibsaeed.cloud
$ curl https://sms-api.saqibsaeed.cloud/healthz   → 200
```

Valid to 13 December 2026, renewed by the certbot timer. Vercel's `ERR_TLS_CERT_ALTNAME_INVALID` (digest `3227098399`) stopped the same minute.

**Hostinger:** `relay-api` there was stopped and disabled at the 13 September cutover, and DNS now points at AWS, so only AWS dials operators.

## 2. P0-2: server settings

| Setting | Production today | Action |
| --- | --- | --- |
| `CONNECTION_ENCRYPTION_KEY` | **Set**, valid (44 base64 chars, 32 bytes) | None |
| `SMPP_ENVIRONMENT` | Unset, so the default **`live`** applies | None |
| `DLT_TM_CHAIN` | **Set on 15 September**: `1702160102377279508` (Textify, per `78c8974`). `.env` backed up first, `relay-api` restarted, confirmed in the running process's environment, `/healthz` 200 | None. Each customer must still link Textify's TM id to their header and templates on the DLT portal |
| `TRUSTED_PROXY_CIDRS` | Unset, loopback default, correct (nginx on the same host) | None |
| `BFF_CLIENT_IP_TOKEN` (new, ask 42) | Unset | Set after deploy; see §3 |

All five are now in `.env.example` and `deploy/env.production.example` with a comment each (`bb10574`, `ecb1cb9`).

## 3. P1-1: ask 42 — built (`ecb1cb9`)

`trustedProxyRealIP` is now a `Server` method. After the nginx `X-Real-IP` step, when `BFF_CLIENT_IP_TOKEN` is non-empty and `X-Relay-BFF-Token` matches it (`subtle.ConstantTimeCompare`), a parseable `X-Relay-Client-IP` becomes the caller's address. Otherwise both headers are ignored. All five address headers are deleted before any handler, and a test asserts the token never reaches the logs. A token shorter than 32 bytes stops startup.

**§2.4 scope:** honoured **everywhere** `clientIP` is read. That includes `OPERATOR_IP_ALLOWLIST`, which now checks the operator's own address rather than Vercel's.

**§2.5 edge limit:** it exists on production and returns **429**: `limit_req_zone $binary_remote_addr zone=relay_auth rate=10r/s`, `burst=20 nodelay`, `limit_req_status 429`, on login, signup, forgot, resend and MFA challenge. It was not in the repo; `deploy/aws/nginx/` now holds the live files. It is still keyed on nginx's peer, i.e. Vercel, so the whole dashboard shares 10 r/s on those routes. Fine at today's volume; say if you want it raised.

**Red on `bb10574`** (`internal/api/bff_client_ip_test.go`):

```
--- FAIL: TestManyUsersBehindTheDashboardAreNotOneAddress (8.37s)
    a customer signing in after 20 other users' mistakes = 401 {"error":{"code":"too_many_attempts",...}}, want 200
--- FAIL: TestOneUserBehindTheDashboardIsStillLimited (7.74s)
    21st failure from one user through the BFF = {"error":{"code":"unauthenticated",...}}, want too_many_attempts
```

**Tests 3, 4 and 5 are green on `main`, not red.** `main` ignores `X-Relay-Client-IP` entirely, so an untrusted header already changes nothing. Asking for them red is asking for a behaviour `main` does not have. They are guards, and the mutation proves they work.

**Mutation** (compare replaced with `true`):

```
--- FAIL: TestTheClientAddressHeaderNeedsTheBFFToken/no_token
--- FAIL: TestTheClientAddressHeaderNeedsTheBFFToken/wrong_token
--- FAIL: TestNoServerTokenMeansNoBFFTrust
```

**Your turn, after deploy:** we generate the token on the server. It must reach your Vercel env as the same value without passing through chat or a document. Tell us who sets it in Vercel and we will do both in one sitting.

## 4. P1-2: replies and STOP — built (`3ae7e93`)

**SMS.** `SMPPBind.handle` sends a `deliver_sm` without the receipt bit to a new `SMPPEvents.Inbound` event, carrying carrier, source, destination and decoded text (GSM-7 and UCS-2). The API resolves the tenant as the one holding an **approved SMS sender whose header is the destination** (case-insensitive, cross-tenant on the operator pool). It then finds or creates the contact and calls `ReceiveInboundMessage`. That call suppresses the number when the trimmed body is a stop keyword (`STOP`, `UNSUBSCRIBE`, `CANCEL`, `END`, `QUIT`, `OPTOUT`) before anything else. It then emits `message.inbound`. Handling runs off the bind's read loop so receipts don't queue behind it. A destination that no tenant holds, or that several do, is logged (`inbound SMS not attributed to a tenant`) and acknowledged.

**RCS.** The `RECEIVED` webhook was only logged. It now takes the same path (`fileReply`), with the tenant resolved from the carrier agent as before.

**Red on `bb10574` / `3ae7e93`^:**

```
--- FAIL: TestAHandsetReplyBecomesAnInboundMessage    inbound events = 0, want 1
--- FAIL: TestAUnicodeReplyIsDecoded                   inbound = [], want the Devanagari text
--- FAIL: TestAnSMSReplyAppearsInTheInbox               timed out waiting for the reply in GET /v1/conversations
--- FAIL: TestStopSuppressesTheNumberAndTheNextSendIsRefused   timed out waiting for the number on the suppression list
--- FAIL: TestAReplyThatIsNotAStopKeywordDoesNotSuppress       timed out waiting for the reply in the inbox
--- FAIL: TestAReplyToAnUnknownHeaderIsAcknowledgedAndLogged  timed out waiting for a log line naming the unresolved reply
--- FAIL: TestAnRCSReplyReachesTheInboxAndStopSuppresses      no RCS conversation ...; STOP over RCS did not suppress ...
```

`TestAReceiptIsStillAReceipt` (no regression) is green before and after. After STOP, the next send is refused `recipient_suppressed` at cost 0.

**Mutation** (keyword check disabled):

```
--- FAIL: TestStopSuppressesTheNumberAndTheNextSendIsRefused   timed out waiting for the number on the suppression list
```

**Limits, said plainly:**
- An Indian handset cannot reply to an alphanumeric header. Replies arrive on a long code or short code the operator provisions, and this build has no table mapping such a number to a tenant, so a reply to one is logged and not attributed. It needs a "reply number" on the sender, and a contract field, when the first operator provisions one.
- An RCS contact is created with country `IN`; both RCS carriers are Indian.

## 5. P1-3: DND scrub — design proposal, not built

Nothing is built, because the build depends on two things only you and the founder can supply:
- **the register source** (operator scrub API, a licensed provider, or a synced NCPR file)
- **the `dnd_blocked` refusal code** in the contract

**Proposed design:**
- A `DNDRegister` interface: `Lookup(ctx, msisdn) (Preference, error)`. `Preference` is `full block` or a set of blocked categories.
- The gate calls it only for India msisdns when the template's DLT category is `PROMOTIONAL`, after the suppression check and before any money is held.
- Blocked means refused with `dnd_blocked`, `rejected`, cost 0.
- Transactional, service-implicit and service-explicit traffic never looks up.
- A lookup error **fails closed** for promotional traffic (refused `dnd_blocked`, reason in the log), per your test list.
- Results are cached per msisdn for 24 hours (NCPR changes take up to 7 days to apply anyway).

**With no register configured:** refuse every Indian promotional message (safe, stops promotions) or send them (today's behaviour)? We recommend refusing, but it is the founder's call.

**Confirm the design, and send the request document with the code.** We then build it test-first.

## 6. P1-4: webhooks — built (`09938a6`, migration `00052`)

**What changed:**
1. **Secret storage.** `webhook_endpoints.signing_secret_sealed` holds the secret encrypted with `CONNECTION_ENCRYPTION_KEY`, written at creation.
2. **Signing.** Every delivery, test event and resend signs with the decrypted full secret, through one function, `deliverWebhook`. The prefix is no longer used anywhere.
3. **Emission.** `sending.Service.Settled` is called after a report's new state is written. The API emits `message.delivered` (status `delivered`) or `message.failed` (undelivered or expired). This runs from carrier receipts, the Airtel/Vi webhooks, the sandbox drainer and the reconciler. The payload carries `messageId, status, channel, msisdn, segments, costMinor, currency, errorCode, errorClass, deliveredAt, updatedAt`.
4. **Retries.** A failed first attempt is retried after 1, 5 and 30 minutes, stopping at the first success, and every attempt is a row in the delivery log.

**Deviations from your document:**
- **There is no Rotate action.** The contract has `POST /v1/developer/api-keys/{id}/rotate` but nothing for webhooks, and your console has no webhook rotate either. So an endpoint created before `00052` cannot be recovered by rotating. Its deliveries are recorded as `failed`, with the snippet *"Not sent: this endpoint was created before signing secrets were stored … Recreate the endpoint."* Delete and recreate it; if you want Rotate, send the contract for it.
- **Not a new `needs_rotation` status.** Adding one would widen `WebhookStatus`, which is yours; the NULL sealed secret is the marker.
- **Retries are in-process** and are lost on a restart; a queue table can replace them when that matters.
- **`message.failed` fires only for a message that was accepted and then failed** (a receipt). A refusal at submit is already the synchronous API answer and emits nothing.

**Red on `3ae7e93`** (shape-only stubs so the file compiled):

```
--- FAIL: TestADeliveredSMSEmitsOneEventSignedWithTheFullSecret   timed out waiting for message.delivered at the customer's endpoint
--- FAIL: TestAnUndeliveredSMSEmitsMessageFailed                  timed out waiting for message.failed at the customer's endpoint
--- FAIL: TestACustomer500IsRetriedAndEachAttemptLogged           timed out waiting for the retry to succeed
--- FAIL: TestAnEndpointWithNoStoredSecretIsSkippedAndSaysWhy     column "signing_secret_sealed" ... does not exist
```

**Mutation** (sign with `hook.SigningSecretPrefix` again):

```
--- FAIL: TestADeliveredSMSEmitsOneEventSignedWithTheFullSecret
    signature does not verify with the secret shown at creation
    the event is signed with the display prefix, which no customer can verify
```

## 7. P1-5: production hygiene

| Item | State | Done |
| --- | --- | --- |
| Dev MFA secret | `ENABLE_DEV_ENDPOINTS` **is `true`** on production | **Code fixed** (`85381c1`): the dev secret needs the flag **and** a reserved-TLD fixture address, for customers and operators. Red: `a real address was enrolled with the published dev TOTP secret`; the mutation (flag only) gives the same failure. The flag itself is left on until you confirm no proof script needs `/v1/dev` through the tunnel; say so and we turn it off. |
| Open signup | `SIGNUP_INVITE_CODE` unset | **Founder decides.** Setting it blocks self-signup for everyone without the code. |
| Operator console | `OPERATOR_IP_ALLOWLIST` unset | **Need the team's addresses.** With ask 42 deployed, those are the operators' own addresses, not Vercel's. |
| Demo operator | `ops@relay.internal` published password | **Not touched yet.** Your writing checks may sign in as it. Say "disable" (`operator-admin disable`) or send a new password through a safe channel. |
| Backups | Postgres only | **Done** (`b226f7f`), and run once on production: `sms_nightly` 3.2M, `clickhouse_nightly` 3.8M (schema plus Native rows for `messages`, `message_events`, `message_rollup_hourly`), `media_nightly` 1.6M, exit 0. Uploaded to the same S3 prefix as the dumps, because the instance role may write only there. The live timer uses the new script from the next deploy. |

## 8. P2

- **P2-3 suppression flags: done** (`f812b04`), read from the suppression list on every contact read.
  - Red: `suppressed contact reads phone=<nil> email=<nil>, want true and true`.
  - Mutation (phone `EXISTS` replaced with `false`): red.
- **P2-1 RCS fallback and buttons:** not started. It only matters once RCS credentials exist, and there are none on production. Our choice when we start: execute fallback on an RCS non-delivery rather than 422 the fields your campaign form already sends.
- **P2-3 demo reseed** (agent on the agent-less RCS sender): not started; it touches only the demo tenant.
- **P2-3 ask 9** (`description` on Campaign and Journey): the contract has no such field yet. Send it and we build.
- **P2-2 engines:** waiting on the founder.

## 9. Test run

Full suite on `f812b04`, against the AWS test databases: **every package passed except one test in `sending`**,
`TestASendTakesTheHighestPriorityActiveRouteAndRecordsIt`:

```
seed route: ERROR: duplicate key value violates unique constraint "routes_country_channel_priority_key"
```

That came from our own leftovers, not from a defect: the operator-name mutation run (pattern loosened to `^.{1,40}$`)
created five disabled "Bad name" IN/SMS routes that the refusal test never deleted, and `sending` then collided with
priority 1. We deleted those five rows from `sms_test`, and `TestAMalformedOperatorNameIsRefused` now removes any route
a broken check lets through. Re-run: the failing test passes, and the whole `sending` package passes (445s).

## 10. What we need from you

2. The DND design confirmation, register source, and `dnd_blocked` contract.
3. Who sets `BFF_CLIENT_IP_TOKEN` in Vercel.
4. Invite-only signup: yes or no.
5. The operator IP list.
6. The demo operator: disable, or rotate.
7. Whether `ENABLE_DEV_ENDPOINTS` can be turned off.
8. A webhook Rotate endpoint in the contract, if you want one.
