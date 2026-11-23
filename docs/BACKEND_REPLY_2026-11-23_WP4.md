# Backend reply, 23 Nov 2026: WP4 WhatsApp on the Meta Cloud API

Commit `31ec59f` (local, not pushed). Migration `00087_whatsapp_cloud_api`, ClickHouse
`010_provider_billing`. Status of every row: the test is written; tests that need a database **have not run** (relay-aws unreachable, see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

**Code-complete on the fake only.** Every Meta call goes through `connector.WhatsAppProvider`
(exchange code, subscribe, register, account, submit template); the only implementation is
`FakeWhatsApp`. The real Graph API client is not built (below).

The two breaks you predicted are resolved: `SenderId.messagingTier` is gone (`messagingLimit`,
`maxUniqueContacts24h`, and ours `uniqueContactsUsed24h`), and `NewConversationReplyRequest.body` is a pointer.

Template rules in Meta's order (`internal/domain/whatsapp`) ran and pass: `TestValidateInMetasOrder`,
`TestVariablesSkipMediaHeaders`.

## Acceptance (Batch 3 section 5)

| Row | Test | Ran? |
|---|---|---|
| 1 Campaign on a sender with payment pending: 422 onboarding_incomplete | `TestAWhatsAppCampaignWaitsForSetup` | no (DB) |
| 2 Template send with a variable missing: refused before Meta | gate (variables) and `TestWhatsAppRepliesKeepTheCustomerWindow` (reply: variables_missing) | no (DB) |
| 3 The same status webhook twice: one delivered step, one charge | **no WhatsApp-specific test**. Statuses claim `wa:<wamid>:<status>` in `provider_event_receipts` before applying, the same claim the email test proves | not done |
| 4 ACCOUNT_DISCONNECTED: next send refused; capability sandbox with a reason | `TestADisconnectedWhatsAppAccountStopsSending` | no (DB) |
| 5 Signup at the cap: 409, and the code still works once the cap lifts | `TestEmbeddedSignupRefusesBeforeSpendingTheCode` (the cap is checked before the code is exchanged) | no (DB) |
| 6 A used code: 422, no second sender | same | no (DB) |
| 7 133016: blocked, registrationAttemptsLeft 0 | `TestTheLastRegistrationAttemptBlocksTheNumber` | no (DB) |
| 8 A template Meta has not approved is never sent | `TestAWhatsAppCampaignNeedsMetasApprovalAndRoomInTheLimit`, `TestWhatsAppRepliesKeepTheCustomerWindow` | no (DB) |
| 9 Free text outside the window: outside_customer_window, no Meta call | `TestWhatsAppRepliesKeepTheCustomerWindow` | no (DB) |
| 10 Only an inbound message reopens the window | same (a template reply leaves `windowExpiresAt` null; an inbound one opens it) | no (DB) |
| 11 Read without delivered: delivered and read, charged once | **no test**; `read` is applied as delivered and read in one report | not done |
| 12 Only delivered or read is charged; other terminal states release the hold | shared settlement path; **no WhatsApp-specific test** | not done |
| 13 providerBilling copied from pricing, never recomputed | **no test**; copied field for field in `applyWhatsAppStatus` | not done |
| 14 No endpoint returns a token, a secret or a PIN | `TestEmbeddedSignupRefusesBeforeSpendingTheCode` (signup and GET sender bodies) | no (DB) |
| 15 Two tenants never share a number | same (unique index on `wa_phone_number_id`; the second tenant gets 422) | no (DB) |

Also covered: the daily limit (251 people on TIER_250: 422 `messaging_limit_reached`, nothing
created), exactly one of body or templateId, a closed window written as an explicit `null` (absent
means "the server does not say"), template create (pending, Meta's id, 409 on name and language),
Meta's approve, flag, category and quality events landing in `waState` (`TestWhatsAppTemplatesFollowMeta`).

## Decisions and guesses you should know about

- **Meta webhook field names** for `phone_number_quality_update` (`event`, `current_limit`),
  `template_category_update` (`previous_category`, `new_category`) and
  `message_template_quality_update` (`new_quality_score`) are from our memory of Meta's docs, not
  read for this build (your dossier did not read them either). `FLAGGED` is taken as quality red and
  `UNFLAGGED` as green. Live acceptance must confirm all of these.
- A WhatsApp template that never went to Meta (`waContent`, no `waState`) is judged by our own
  status, as your mock does. Row 8 holds for every template Meta has reviewed.
- A WhatsApp conversation reply needs one ready WhatsApp number in the tenant; replies do not yet
  choose which number when there are several.
- Creating a template in Meta's model needs a connected number (422 `onboarding_incomplete`
  otherwise), because Meta reviews it on the tenant's own account. Your mock does not refuse this.
- `template.paused` carries `pausedUntil` only when Meta gave one; we do not invent it.

## Not done

- The real Meta Graph API client, the scheduled template sync, inbound messages from Meta into the
  inbox, and sending through Meta (`POST /<PHONE_NUMBER_ID>/messages` with `biz_opaque_callback_data`).
- The go-live check steps for WhatsApp (Batch 3 §3.11).
- Tests for rows 3, 11, 12, 13.
- `messaging_limit_reached`, `template_not_approved`, `variables_missing` on these two routes and
  the 429 on template create are not declared in the contract (status file).
- Live acceptance.
