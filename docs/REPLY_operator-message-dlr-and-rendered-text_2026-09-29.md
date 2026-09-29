# Reply: operator message text and carrier receipt (29 Sep 2026)

Answers `HANDOFF_TO_BACKEND_2026-09-29-operator-message-dlr-and-rendered-text.md`.
Not deployed yet: A1-A12 are unverified against the live API.

## Decisions (§6)

1. **One-time codes: `renderedText` is `null`.** A Verify message's text is never stored
   (`SendRequest.OneTimeCode`), so there is nothing to mask and nothing to leak. The receipt's
   `text:` field is blanked out of `dlr.raw` for those messages; the rest of the line is untouched.
2. **Retention:** the columns are on the `messages` row (`db/clickhouse/006_message_text_and_dlr.sql`),
   so they go with the row under the same tenant retention and the 365-day table TTL. No new table.
3. **Audit entry (`messages.view`):** not built. It needs the enum value agreed first; say if wanted.
4. **§3.4:** `campaignId` is a query parameter on `GET /v1/operator/campaigns`.

## Behaviour worth knowing

- `templateName` is joined from `templates` at read time, so it is the template's *current* name
  and null if the template was deleted. It is not a send-time snapshot as §3.2 describes.
- `dlr` is the receipt that settled the message; a replay does not overwrite it. RCS gets `stat`
  (`DELIVERED`/`READ`/`FAILED`), `doneAt` and `receivedAt`, and `raw` null. The reconciler's
  expiry records `EXPIRED`; the sandbox records `DELIVRD`/`UNDELIV`/`REJECTD`.
- `dlr.errorCode` is null when the receipt carried no `err` (RCS).
- Rows written before this deploy return `renderedText: null` and `dlr: null`.
- The export has the seven new columns appended at the end.
- The routes are still mounted directly, not through the generated contract, so `openapi` is not
  regenerated here. Fields stay optional on your side until the deploy is confirmed.
