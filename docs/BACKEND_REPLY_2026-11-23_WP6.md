# Backend reply, 23 Nov 2026: WP6 SMS (approved links, quiet hours, link domains, lookup, DND)

Commit `8087ce2` (local, not pushed). Migrations `00082_approved_links`, `00083_campaign_quiet_hours`,
`00084_link_domains_and_lookups`. Config: `DND_MODE` (deny, operator, ncpr; default deny),
`APPROVED_LINK_COUNTRIES` (default IN), `LINK_DOMAIN_TARGET`. Status of every row: the test is written; tests that need a database **ran on 27 Nov** on the Hostinger server and pass (see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

Pure rules ran and pass: `TestApprovedLinks`, `TestQuietHoursAreTheHoursOfSilence`, `TestQuietHoursProblems`,
`TestLaunchMovesUntilNeitherRuleMovesIt`, `TestDNDModeDefaultsToDenyAndRefusesAnUnknownMode`,
`TestApprovedLinksAreOnForIndiaUnlessSwitchedOff`.

## Acceptance (Batch 5 section 4)

| Row | Test | Ran? |
|---|---|---|
| S1 to S7 approved links (unlisted, approved, pending, subdomain, www., listed shortener, {{link}} host) | `TestOnlyApprovedLinksMaySendInIndia` (one case per row, plus an API send refused at cost 0) | no (DB) |
| S8 held inside quiet hours: 201 scheduled, heldUntil the opening, counts 0 | `TestQuietHoursHoldALaunchAndRefuseAResume` | no (DB) |
| S9 outside: not held | same | no (DB) |
| S10 equal times, bad zone, undeclared key: 422 | same | no (DB) |
| S11 resume inside the window: 422 quiet_hours, still paused | same | no (DB) |
| S12 a URL, not a host: 422 | `TestALinkDomainIsUsableOnlyOnceActive` | no (DB) |
| S13 same host in capitals: 409 | same | no (DB) |
| S14 three validations: validating, issuing_certificate, active | same | no (DB) |
| S15 delete a domain a link uses: 409 | same | no (DB) |
| S16 short link on a domain not active: 422 | same | no (DB) |
| S17 lookup `nope`: 422 | `TestLookupsJudgeTheBodyFirstThenAvailability` | no (DB) |
| S18 batch of 101: 422 | same | no (DB) |
| S19 lookup off: 409 lookup_unavailable | same | no (DB) |
| S20 SMS setup has dnd_register | `TestTheDNDRegisterIsAStepAndAKeyByNameOnly` | no (DB) |
| S21 the DND key by name and presence only | same; it checks `DND_MODE`, see W1 in the intake reply | no (DB) |

## Not done

- S21 names `DND_REGISTER_PROVIDER`; we built `DND_MODE` per your D-K. Please change S21.
- No NCPR register is connected, so `ncpr` mode refuses promotional India SMS, as `deny` does.
- The lookup provider is a fake (`FakeLineLookup`); no real HLR vendor is wired.
- VIDEOCON scrub (X4): unknown.
- Live acceptance.
