# Handoff to UI, 24 Nov 2026: your backend handoff (WP-S, WP1 to WP6) is built, not yet live

Answers `docs/backend-handoff/PROMPT_FOR_BACKEND_AGENT.md`. Built against your contract at
`e5613f1f`; the contract was not changed by us.

## Read this before you switch any mock off

**Update, 27 Nov:** this is now deployed on the Hostinger server (`http://189.72.144.98:8080`) and
its database tests ran and pass; see `HANDOFF_TO_UI_2026-11-27_api-on-hostinger.md`. As first
written: nothing was deployed and no database test had run. Keep your mocks on for all six packages until we
send the follow-up that says each acceptance row ran against the deployment.

## Where the detail is (in SMS-BE)

| File | What |
|---|---|
| `docs/BACKEND_REPLY_2026-11-23_status.md` | Commits, both script outputs, what the contract does not declare |
| `docs/BACKEND_REPLY_2026-11-23_intake.md` | Rows we argued with before building (W1 to W3) |
| `docs/BACKEND_REPLY_2026-11-23_WP-S.md` | Security items |
| `docs/BACKEND_REPLY_2026-11-23_WP1.md` to `..._WP6.md` | Per package: acceptance row, its test, whether it ran, not-done list |

## Your two scripts, at `31ec59f`

```
contract-delta.cjs      0 real differences, exit 0
diff-backend-spec.cjs   paths 212 / 212, schemas 372 / 372, no real difference, exit 0
```

The codegen clash (`RcsVendor`) is fixed on our side in `make generate`; you do **not** need to add
`x-enum-varnames` (the intake reply asked for it; withdrawn).

## Asks for the contract (we changed nothing; please decide and we regenerate)

1. **Declare 403** on the 11 operator money and standing routes: `suspendTenant`, `reinstateTenant`,
   `throttleTenant`, `creditTenantWallet`, `recordTenantPayment`, `voidTenantPayment`,
   `setTenantCreditLimit`, `updateDefaultRate`, `createRateOverride`, `editRateOverride`,
   `removeRateOverride`. A plain `operator` now gets 403 `forbidden` there.
2. **Voice call**: `POST /v1/sender-ids/{id}/voice-call` answers 501 `not_implemented` and never the
   code. Declare the 501 (or remove the operation) and drop `VoiceCallResult.code`.
3. **Error codes we answer that no enum lists**: `sending_paused`, `messaging_limit_reached`,
   `template_not_approved`, `onboarding_incomplete` on `POST /v1/campaigns`;
   `outside_customer_window`, `template_not_approved`, `variables_missing`, `onboarding_incomplete` on
   `POST /v1/conversations/{id}/reply`. Every one has a failure-dictionary row.
4. **429 `rate_limited`** on `POST /v1/templates` (100 WhatsApp templates an hour), as your mock does.
5. **Audit actions** `tenant.channel_enable` and `tenant.channel_disable` (written by
   `PATCH /v1/operator/tenants/{id}/channels/{channel}`) are not in `AuditAction`.
6. **Batch 5 row S21** names `DND_REGISTER_PROVIDER`; we built `DND_MODE` per your D-K. Please change
   the row.

## Behaviour that may differ from your mock

- `Conversation.windowExpiresAt`: an instant while open, an explicit `null` when closed, absent for
  channels other than WhatsApp. Same on list, get, reply, close, reopen and read.
- Creating a template in Meta's model (`waDefinition`) on a number that is not connected answers 422
  `onboarding_incomplete`: Meta reviews templates on the tenant's own account. Your mock accepts it.
- A WhatsApp template with no `waState` (old `waContent` ones) is judged by our own `status`, as your
  mock does. One with a `waState` is sendable only when that reads `approved`.
- A WhatsApp reply needs one ready WhatsApp number in the tenant; we do not yet choose between several.
- `template.paused` carries `pausedUntil` only when Meta gave one.

## Not built

- The real Amazon SES client and its event endpoint (email runs on the fake only).
- The real Meta Graph API client, template sync, inbound WhatsApp into the inbox, and sending
  through Meta (WhatsApp runs on the fake only). The WhatsApp go-live check steps.
- Tests for RCS rows R9, R10 (carousel-specific), R14, R17, R18 and WhatsApp rows 3, 11, 12, 13.

## Still open on our side

VIDEOCON and DND (your X4): unknown. `DND_MODE` stays `deny` until Saeed hears from them.

Next from us: when the server is back, run the full suite, fix, deploy, run every acceptance row
against the public API, and send a follow-up handoff with the requests and responses.
