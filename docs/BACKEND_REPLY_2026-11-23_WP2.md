# Backend reply, 23 Nov 2026: WP2 failure reasons

Commit `22a5b28` (local, not pushed). Migration `00081_failure_reasons.sql`: your 128 mock rows
(read from `src/mocks`, ids fr-0001 to fr-0128) plus 19 of ours (fr-0129 to fr-0147); `00086` adds
fr-0148 `sending_paused`, fr-0149 `variables_missing`, fr-0150 `messaging_limit_reached`. Status of every row: the test is written; tests that need a database **have not run** (relay-aws unreachable, see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

Resolution order (route, vendor, channel and source exact, prefix family, unmapped) is a pure
function with mutation-style tests: `TestResolutionOrder`, `TestAnUnknownCodeIsUnmappedAndKeepsTheRawCode`,
`TestOurOwnRefusalCarriesNoProviderCode`, `TestContextSaysWhichSourceAFailureCameFrom` (**ran, pass**).

## Acceptance (Batch 1 section 6)

| Row | Test | Ran? |
|---|---|---|
| 1 Every terminal non-delivered message has a failure with a title | `TestEveryFailedOrRefusedMessageSaysWhyAndWasNotCharged` | no (DB) |
| 2 Every MessageRefusalCode has a dictionary row | `TestEveryRefusalCodeHasADictionaryRow` | no (DB) |
| 5 A PATCH shows on the next read, version up by exactly one | `TestARuleTakesACodeOutOfTheQueueAndAnEditShowsOnTheNextRead` | no (DB) |
| 6 A code leaves the unmapped queue once a rule matches it, not before | the same test | no (DB) |
| 9 failure.charged is false for every failed or rejected message | `TestEveryFailedOrRefusedMessageSaysWhyAndWasNotCharged` | no (DB) |
| 10 message.failed webhook data.failure equals the log's failure | `TestTheFailedWebhookCarriesTheLogsFailure` | no (DB) |

Also: a bad edit writes nothing (`TestEditingARuleJudgesTheRecordFirstAndWritesAllOrNothing`), an OTP's
provider code is withheld (`TestAOneTimeCodesProviderCodeIsWithheld`), the layer filter
(`TestTheLayerFilterNarrowsTheLog`).

## Not done

- `sending_paused`, `variables_missing` and `messaging_limit_reached` have dictionary rows but are
  not in `MessageRefusalCode` (status file).
- Live acceptance.
