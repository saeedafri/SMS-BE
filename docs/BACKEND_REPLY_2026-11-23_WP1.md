# Backend reply, 23 Nov 2026: WP1 channel truth

Commit `99b75c8` (local, not pushed). Migration `00080_channel_truth.sql` (tenant_channels,
channel_checks, sandbox_refunds), ClickHouse `007_message_simulated.sql`. Status of every row: the test is written; tests that need a database **ran on 27 Nov** on the Hostinger server and pass (see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

## Acceptance (Batch 1 section 6)

| Row | Test | Ran? |
|---|---|---|
| 3 A simulated message never has costMinor > 0 | `TestATestKeySendOnASandboxChannelIsSimulatedAndFree` (cost 0 in the log and no balance change) | no (DB) |
| 4 live implies a real connector, sandbox implies none | `TestAChannelIsLiveOnlyWithARealConnector`, `TestASandboxChannelOnlySimulatesTestKeySends` | **yes, pass** |
| 7 PATCH for an unknown tenant with a bad body is 404, not 422 | `TestAnUnknownTenantIs404WhateverTheChannelBodySays` | no (DB) |
| 8 GET /v1/operator/channels holds no configuration value | `TestOperatorChannelsListKeysByNameOnly` | no (DB) |

Also covered: a switched-off channel refuses campaign create, API send and test send at cost 0 and
writes two audit rows (`TestASwitchedOffChannelRefusesEverySend`); the go-live check stops at the first
step that cannot pass (`TestGoLiveCheckStopsAtTheFirstStepThatCannotPass`); a second check while one
runs is refused (`TestASecondGoLiveCheckWhileOneRunsIsRefused`, ran, pass).

## Decision D-A (refunding sandbox charges)

`cmd/sandbox-refund` prints tenant, message count and amount and changes nothing. `-apply` posts
compensating ledger entries. It has not been run against the live ledger and will not be until the
founder says yes to the printed list.

## Not done

- Go-live check steps per engine are generic; the WhatsApp step list in Batch 3 §3.11 is not built.
- Audit actions `tenant.channel_enable` and `tenant.channel_disable` are not in `AuditAction`.
- Live acceptance.
