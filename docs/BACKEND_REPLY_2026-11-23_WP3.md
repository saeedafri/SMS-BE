# Backend reply, 23 Nov 2026: WP3 Email on Amazon SES

Commit `83c9f33` (local, not pushed); one rename in `31ec59f` (the receipts table is now
`provider_event_receipts`, shared with WhatsApp). Migrations `00085_email_on_ses`, `00086_refusal_rows`,
ClickHouse `009_email_events`. Status of every row: the test is written; tests that need a database **ran on 27 Nov** on the Hostinger server and pass (see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

**Code-complete on the fake only.** Every SES call goes through `connector.EmailProvider`; the only
implementation is `FakeEmail`. The real SES adapter and the SES event endpoint are not built (below).

## Acceptance (Batch 2 section 6)

| Row | Test | Ran? |
|---|---|---|
| 1 No email leaves for an unverified sender; the refusal names the domain | `TestAnEmailCampaignThroughTheProvider` | no (DB) |
| 2 A bounce or complaint replayed: one write | same (one bounced webhook for one bounce sent twice) | no (DB) |
| 3 Marketing carries the one-click unsubscribe header, transactional none | same | no (DB) |
| 4 A suppression never affects another tenant | same (two tenants, one address) | no (DB) |
| 5 Charged only on Delivery; a suppressed skip is outside the bounce rate | same | no (DB) |
| 6 verify-dns creates nothing; 401 before 404 before 409 | `TestVerifyDNSJudgesInOrderAndDMARCNeverBlocks` | no (DB) |
| 7 Unsubscribe: one identical 404 for unknown and expired, no full address, POST twice the same | `TestThePublicUnsubscribe` | no (DB) |
| 8 Every required record verified, DMARC failing: verified | `TestVerifyDNSJudgesInOrderAndDMARCNeverBlocks` | no (DB) |
| 9 Nothing sent: null rates, not zero | `TestSendingHealthIsNullNotZeroWithNothingSent` | no (DB) |
| 10 Paused tenant: email campaign 422 sending_paused, SMS campaign 201 | `TestAPausedTenantCannotLaunchEmailButCanLaunchSMS` | no (DB) |
| 11 message.bounced body equals the log's failure, no address | `TestAnEmailCampaignThroughTheProvider` | no (DB) |
| 12 emailTracking that is not two booleans: 422 | `TestEmailTrackingIsTwoBooleans` | no (DB) |

## Not done

- The real SES client (SESv2 SendEmail, identity and MAIL FROM calls, quota) and the SNS event
  endpoint that feeds `ApplyEmailEvent`: not built. Needs the SES account in ap-south-1 and its keys.
- `sending_paused` is not in `MessageRefusalCode` (status file).
- Live acceptance.
