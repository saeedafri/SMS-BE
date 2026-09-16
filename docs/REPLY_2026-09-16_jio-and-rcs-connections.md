# Jio RCS, and RCS operators in the database — 16 Sep 2026

## What was asked

> yes build Jio with database RCS connections

Two things: the Jio adapter, and RCS operator credentials managed like SMPP
binds instead of through the environment — several operators at once, changed
without a deploy.

## What was built

**`internal/connector/rcs_jio.go`** — Jio Business Messaging, from the "User and
API Integration Guide v2.3".

| Guide | Built |
|---|---|
| §6.1 `GET /v1/oauth/token` | Token per assistant, cached for its hour, re-minted after a 401 |
| §6.2.1 `POST …/assistantMessages/async` | Plain-text send, `messageTrafficType` on promotional traffic, TTL as `"120s"` |
| §6.2.4 `GET …/capabilities` | One handset, features included; 404 means not reachable |
| §6.2.5 `POST /v1/messaging/usersBatchGet` | Batch reachability, and one-at-a-time under Jio's floor of 500 |
| §6.2.1 errCode table | All 20 rows mapped to our error vocabulary |
| §7 callbacks | `STATUS_EVENT` / `USER_EVENT` / `USER_MESSAGE` / `SERVER_EVENT` |

Two details worth naming. `SEND_MESSAGE_SUCCESS` is **not** delivery — it is JBM
accepting a send it already answered 201 to — so it settles nothing; delivery is
`MESSAGE_DELIVERED`, or `MESSAGE_READ` when the delivery event is skipped. And
Jio issues a client id and secret **per assistant**, not one login per account,
so its secrets are a map keyed by assistant id and tokens are cached per
assistant.

**`db/migrations/00053_rcs_connections.sql`** — one row per RCS operator
account: vendor, environment, hosts and account ids in `settings`, every secret
in one sealed blob encrypted with `CONNECTION_ENCRYPTION_KEY`, created disabled,
at most one active per operator per environment. Reloaded every minute, exactly
like the SMPP binds, and an account whose credentials changed is rebuilt while
the others keep the tokens they have cached.

**Choosing the operator.** With several accounts live, a message takes the first
operator in the corridor's route priority that the sender's agent has an
approved launch on. Not the first configured, and never a silent reroute: a
brand only exists on the networks it is launched on, so an agent launched
nowhere reachable is still refused `rcs_agent_not_resolved`.

**Rendered text for operators that hold no template.** Airtel and Vi hold the
approved template; Google and Jio review the agent instead and were refusing
campaign sends `body_required`. They now get the template's own text, rendered
from the same contact values a carrier-held template is filled with. Billing is
untouched.

**`operator-admin rcs-connection`** — list, add, add-assistant, drop-assistant,
enable, disable, test. Secrets are prompted for, never arguments, never printed.

## Tests

Red first on `origin/main`, then green, then mutated.

| Test | Proves |
|---|---|
| `TestJioSendsPlainTextUnderTheAssistantWithABearerToken` | path, query, bearer, `content.plainText`, `PROMOTION` |
| `TestJioMintsOneTokenPerAssistantAndMintsAgainAfterA401` | 20 sends mint one token; a 401 drops it |
| `TestJioRefusesWithoutAnAssistantSecretABodyOrAnAgent` | three refusals never reach Jio |
| `TestJioReadsTheErrCodeOfARefusedSend` | errCode 24 → `recipient_opted_out`, 28 → throttled, 403, 502 |
| `TestJioCapabilityAndReachability` | 404 is an answer; under 500 goes one at a time; batch keeps the caller's order |
| `TestJioWebhookEvents` | 8 envelopes, including "accepted is not delivered" |
| `TestTheRCSRouterSendsEachMessageThroughItsOwnOperator` | per-operator dispatch; unknown operator refused |
| `TestTheRCSRouterKeepsAnUnchangedGatewayAndItsTokens` | unchanged accounts are not rebuilt |
| `TestAnEmptyRCSRouterIsNoGateway` | no accounts leaves RCS on the sandbox |
| `TestAnRCSMessageGoesThroughAnOperatorItsAgentIsLaunchedOn` | launch decides, then route priority |
| `TestAMessageForAnOperatorWithNoAccountIsRefusedRatherThanRerouted` | no silent reroute |
| `TestAnOperatorThatHoldsNoTemplateIsSentTheRenderedText` | `"Hi Priya, welcome."` reaches Jio |
| `TestAnEnabledJioAccountBecomesAnRCSOperatorWithinAReload` | disabled is not loaded; enabled is, with Jio's hosts by default |
| `TestAnUnusableAccountIsReportedAndLeftOut` | one broken account does not take the reload down |
| `TestRCSSecretsAreSealedAtRest` | the secret is not readable in the database |
| `TestOnlyOneAccountPerOperatorCanBeActive` | the unique index holds |
| `TestTheEnvironmentsCarrierStillWins` | a deployment configured the old way is untouched |

Mutations that turned tests red: ignoring route priority in `rcsPath`, and
`rcsText` never rendering the template.

## Rolling it out on production

Production already has the corridor — `IN/RCS`: JIO priority 1, AIRTEL 2, VI 3 —
so Jio is first the moment an account exists. Nothing is configured in the
environment, so the database path is live as soon as this deploys.

```
ssh relay-aws
cd /opt/relay && set -a && . ./.env && set +a

./operator-admin rcs-connection add jio "Jio JBM"          # hosts default to Jio's
./operator-admin rcs-connection add-assistant <id> <assistantId>   # prompts for the secret
./operator-admin rcs-connection enable <id>
./operator-admin rcs-connection test <id> <assistantId> +91XXXXXXXXXX
./operator-admin rcs-launch <agent-uuid> JIO <assistantId>
```

Then give Jio the webhook `https://<host>/v1/carrier-webhooks/rcs/jio/<RCS_WEBHOOK_TOKEN>`
and put their 40 source IPs (guide §5) in `RCS_WEBHOOK_IP_ALLOWLIST` — Jio does
not sign its callbacks, so the address list is the other half of the
authentication.

## Still open

- **The contract's `RcsVendor` enum is `["airtel","vi"]`** and the backend now
  answers `jio` (and has answered `google` for a while). Asked for in
  `HANDOFF_TO_UI_2026-09-16-rcs-operators.md`.
- **No console screen for RCS accounts.** The CLI is the way in today, and the
  contract is the frontend team's, so the endpoints were not added unilaterally.
- **Nothing has been sent through Jio for real** — that needs an assistant id
  and secret from Jio, and a launched assistant.
- **`UNSUBSCRIBE` is logged, not acted on.** Jio refuses promotional traffic to
  an opted-out user itself (errCode 24), so nothing is charged, but the event
  could suppress the contact on our side too.
