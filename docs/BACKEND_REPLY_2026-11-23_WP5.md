# Backend reply, 23 Nov 2026: WP5 RCS carousels and rich suggestions

Commit `2f4d047` (local, not pushed). ClickHouse `008_message_fallback_reason.sql`. Status of every row: the test is written; tests that need a database **ran on 27 Nov** on the Hostinger server and pass (see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

Content rules (`internal/domain/rcs/content.go`) run in your order (undeclared field, structure,
limits, then the regime's URL rule) and ran and pass: `TestRCSContentLimits`,
`TestRichFeaturesNameWhatAHandsetMustRender`, `TestWithoutPostbackDataBlanksOnlyThatField`,
`TestWithoutPostbackDataToleratesSpacing`.

## Acceptance (Batch 4 section 4)

| Row | Test | Ran? |
|---|---|---|
| R1 carousel of 2 cards: 201, kind carousel | `TestACarouselTemplateIsAcceptedWithinItsLimits` | no (DB) |
| R2 1 card and 11 cards: 422 "2 to 10 cards" | same, plus `TestRCSContentLimits` | rule: **yes, pass**; HTTP: no (DB) |
| R3 card title of 201: 422 naming "Card N title" | same | as R2 |
| R4 twelve suggestions: 422 naming 11 in all | `TestRCSContentLimits` | **yes, pass** |
| R5 open_url with ftp://: 422 | `TestRCSContentLimits` | **yes, pass** |
| R6 latitude 91; neither coordinates nor search: 422 each | `TestRCSContentLimits` | **yes, pass** |
| R7 calendar end equals start: 422 | `TestRCSContentLimits`, and over HTTP `TestACarouselTemplateIsAcceptedWithinItsLimits` | rule: **yes, pass**; HTTP: no (DB) |
| R8 only variable in a card button URL: 201, variables lists it | same (postbackData is not walked) | no (DB) |
| R9 estimate skips people lacking the column | **no carousel-specific test**; the same variable walk is covered for other shapes by `campaign_variable_skip_test.go` | not done |
| R10 no `{{` reaches the carrier | **no carousel-specific test**; same as R9 | not done |
| R11 handset without RICHCARD_CAROUSEL, fallback on: carried by SMS, never read | `TestAHandsetThatCannotRenderACarouselGoesBySMS` | no (DB, skips locally) |
| R12 no fallback: failed, rich_content_unsupported, layer handset, cost 0 | `TestWithNoFallbackAnUnrenderableCarouselFailsUncharged` | no (DB, skips locally) |
| R13 the message's failure layer and timeline | `TestAnUnrenderableMessageIsTheHandsetsFailure` | no (DB) |
| R14 carousel to a carrier with no card API: the registration sentence, not 500 | **no test**; the registration handler refuses card buttons and the new kinds | not done |
| R15 operator launches with a customer token and with none: 401 | `TestOperatorsSeeLaunchStatesPerCarrier` | no (DB) |
| R16 per carrier the four counts sum to the agents with a launch row | same | no (DB) |
| R17 approve one launch, read again | **no test** | not done |
| R18 a carrier without credentials: a row, approved 0, never reachable true | **no test** | not done |

## Not done

- Tests for R9, R10 (carousel-specific), R14, R17, R18.
- Live acceptance. No carrier we hold supports carousels end to end that we have exercised.
