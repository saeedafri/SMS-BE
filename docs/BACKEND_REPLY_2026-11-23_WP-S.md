# Backend reply, 23 Nov 2026: WP-S integrity and security

Commit `fffd42a` (local, not pushed). Status of every row: the test is written; tests that need a database **ran on 27 Nov** on the Hostinger server and pass (see `BACKEND_REPLY_2026-11-23_status.md`). Script outputs are in that file. Evidence from the deployment: none yet.

| Your id | What we did | Test | Ran? |
|---|---|---|---|
| B1 operator roles | `requireOperatorAdmin` on all 11 routes: credit wallet, record and void payment, credit limit, default rate, create, edit and remove override, suspend, reinstate, throttle. A plain `operator` gets 403 `forbidden` | `TestPlainOperatorIsRefusedEveryMoneyAndStandingRoute` (one request per route) | no (DB) |
| A4 voice code | `RequestVoiceCall` answers 501 `not_implemented`; the code is never returned | `TestVoiceCallNeverReturnsTheCode` | no (DB) |
| B3 SSRF | The webhook HTTP client dials through `net.Dialer{Control: refuseInternal}`, so the check runs on the resolved address at dial time. Refused ranges now include 0.0.0.0/8 and 100.64.0.0/10 beside private, loopback and link-local | `TestAHostnameThatResolvesInternallyIsNeverDialled`, `TestOnlyPublicAddressesMayBeDialled` | **yes, pass** |
| B2 `/metrics` | Stays public (your call was ours). `Metrics.Snapshot()` carries no tenant identifier. New: every unmatched path now shares one key, `unmatched`, instead of creating a permanent key per probed URL | `TestUnmatchedPathsShareOneMetricsKey` | **yes, pass** |
| B7 dev endpoints | `APP_ENV=production` with `ENABLE_DEV_ENDPOINTS=true` refuses to start | `TestDevEndpointsRefuseToStartInProduction` | **yes, pass** |
| B8, B9 | Not read: the live values of `SIGNUP_INVITE_CODE` and `OPERATOR_IP_ALLOWLIST` are on the server, which is down | none | not done |

Also: the campaign scheduler's incident text no longer carries an id.

## Not done

- Live acceptance (a plain operator gets 403 on every money route live): waits for the server.
- B8, B9: read the production values when the server is back.
- The 403 on those 11 routes and the 501 on voice-call are not declared in the contract (status file).
