# Trustsignal RCS and WhatsApp APIs, extracted from their Postman collection (7 Oct 2026)

Source: https://postman.trustsignal.io (collection `44814789-313cef39-b563-4eda-9126-02e15b12000a`, 121 requests).
Sigmo (sigmo.ai) is Trustsignal's panel, so these are the APIs behind the screens we compared against.
No key is stored in this file.

## Common

- **Auth: `?api_key=<key>` in the query string, for every product.** No header auth. A missing key answers
  `400 {"errors":[{"code":"101","codeMsg":"API_KEY_MISSING"}],"success":false}`. The key therefore lands in any URL
  log; our connector strips the URL from every error for that reason.
- Hosts: RCS `https://rcsapi.trustsignal.io`, WhatsApp `https://wpapi.trustsignal.io`,
  SMS `https://sms.trustsignal.io`, email `https://eapi.trustsignal.io`, sub-accounts `https://auth.trustsignal.io`.
- Balance check (read-only, SMS host): `GET /v1/accounts/credits?api_key=` returns remaining per route.
  There is no documented credits endpoint on the RCS or WhatsApp hosts.

## RCS (`rcsapi.trustsignal.io`)

| Call | Request |
|---|---|
| Send one | `POST /api/v1/rcs/with_fallback` `{"to":"+91...","template_id","rcs_variables":{...},"sms_fallback":{"sender","message","template_id","route"},"ttl":"30s"}` |
| Send many | `POST /api/v1/rcs/bulk_with_fallback` `{"receiver":[{"to","rcs_variables","sms_message"}],"template_id","sms_fallback":{"sender","template_id","route"},"ttl"}` |
| List bots | `GET /api/v1/bot?limit=50&page=1`; one: `GET /api/v1/bot/:id` |
| Create bot | `POST /api/v1/bot` (name, bot_type Domestic/International, brandname, desc, number[], email[], website[], terms_url, privacy_url, message_type, logoimageurlrcs, bannerimageurlrcs, colorCode, development_platform, languages_supported, otherCarriercheck, plab/elab/wlab labels) |
| Create template | `POST /api/v1/template` types `text_message`, `rich_card` (standAlone), carousel, text+media; each carries `botId` |
| List / get templates | `GET /api/v1/template?limit=&page=`, `GET /api/v1/template/:id` (status `approved`, ...) |

Limits from their docs: template name 20 characters, header 200, body 2000, button label 25. Rich-card image 3:1 up to 2 MB,
video 10 MB; carousel image 5:4 up to 1 MB, video 5 MB.

**`sms_fallback` is listed as a REQUIRED parameter** (`sender`, `message`, `template_id`, `route` = promotional,
transactional or otp) on both single and bulk sends. The response carries `results.transaction_id`, which every
delivery webhook quotes back.

## WhatsApp (`wpapi.trustsignal.io`)

Trustsignal is a WhatsApp BSP here: it owns the Meta relationship, so **no Meta credentials are needed on our side**.

| Call | Request |
|---|---|
| Send one (template) | `POST /api/v1/whatsapp/single` `{"sender":"<WABA phone>","to","template_id","sample":{"header","bodyvar":[...],"buttonsvar":[...],"media","caption"},"pr1".."pr5"}` -> `results[{to,transaction_id,cost}]` |
| Send many | `POST /api/v1/whatsapp/bulk` `{"sender","template_id","receivers":[{"to","sample":{...}}]}` |
| OTP | `POST /api/v1/whatsapp/otp` `{"sender","to","template_id","sample":{"otp"}}` |
| Reply inside the 24 h window | `POST /api/v1/whatsapp/agent-reply` `{"reply":{...},"sender","to","message_type":"text|media|list|button|cta|product|catalog|location|flow|..."}` (also `typing-indicator`, `mark-read`) |
| Templates | `POST /api/v1/template` `{"wabaid","name","category","lang","components":[HEADER,BODY,FOOTER,BUTTONS...]}` (Meta component format; text, variables, image, video, document, carousel, location, GIF, product, LTO, authentication); `GET /api/v1/template[/:id]`; update `POST /v1/user-templates/update/{id}`; delete `POST /api/v1/template/delete/{id}`. A new template starts `pending` and Meta decides. |
| WABAs | `GET /api/v1/wabas` -> `waba_records[{waba_id,name,messaging_limit,status}]` |
| Webhook | `POST /api/v1/webhook` `{"webhooks":{"message_status":[url],"agent_message_status","click","template","phone","embedded","user_response"}}`; `GET /api/v1/webhook` |
| Media | `POST /api/v1/media_id_url` (by URL), `POST /api/v1/mediaid` (form-data) |

Callbacks are Meta-shaped, wrapped as `{"value":{...},"webhook_type":...}`:
- **Delivery**: `value.statuses[{id (wamid), status: sent|delivered|read|failed, recipient_id, timestamp, errors[{code,title}]}]`
  plus `metadata.phone_number_id`. Failure codes are Meta's (e.g. 131026 undeliverable, 131049 ecosystem engagement).
- **Inbound reply** (what a keyword chatbot needs): `value.messages[{from, id, type, text...}]`, `value.contacts[]`,
  `metadata.phone_number_id`, so the tenant is found from the sending number.
- **Template**: `webhook_type: message_template_status_update` with `event` APPROVED/REJECTED and `reason`, plus category and quality updates.
- **Phone / WABA**: quality update, flagged/unflagged, display-name rejection, embedded-signup outcomes.

Business-initiated WhatsApp needs an approved template; free-form text only works inside the 24 h window (`agent-reply`).

## What Relay does today against this

- **RCS**: connector exists (`internal/connector/rcs_trustsignal.go`), uses the same endpoints (`with_fallback`, `/api/v1/template`, `?api_key=`)
  and webhooks. Registration is `operator-admin rcs-connection add trustsignal`, which prompts for the key and stores it encrypted in
  `rcs_connections`. **The running API does not read the key from `.env`.**
- **`sms_fallback` is NOT required in practice.** Their docs list it as required, but a live send without it was accepted
  (8 Oct 2026). Relay keeps sending without it and runs its own fallback-after-failure, so there is no second SMS.
- **The send reply is `result` (singular) with `phone` and `transaction_id`, not the `results`/`to` their example shows.**
  Our connector trusted the example and refused every accepted send until this was fixed (commit after 7ca4a80); the
  carrier had delivered the message and Relay recorded it failed with no charge. Treat their examples as hints, not contracts.
- **Variable names**: their example fills RCS templates with `custom_param0`; we send our template's own variable names. Their templates
  use `[Name]` placeholders and a `csparams` map for tracked URLs. Not yet proven which keys the send accepts.
- **WhatsApp**: no connector yet. Everything needed is above (sender = WABA phone, Trustsignal's template id, `sample` body variables,
  Meta-shaped callbacks including inbound replies). Template creation and approval sync are the larger part.

## RCS end-to-end test checklist (needs the key and a registered bot)

1. `operator-admin rcs-connection add trustsignal` (prompts for the key; created disabled), then enable it in the console.
2. In Trustsignal's panel: a bot with a `bot_id`, an approved RCS template, credits on the account, the handset added as a tester if the bot is not live.
3. In Relay: register the template with that carrier template id (`POST /v1/templates/{id}/carrier-registration`), launch the agent on TRUSTSIGNAL.
4. Set `RCS_WEBHOOK_TOKEN` on the server (it was missing once) and register Relay's webhook URL in Trustsignal.
5. Send one message to one handset and read the delivery state from `GET /v1/messages/{id}`.
