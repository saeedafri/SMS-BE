# RCS: Jio, and operators in the database — 16 Sep 2026

Pull `main`, run `make generate` (nothing in the contract changed yet — see
**What we need from the contract** below).

## What changed

**1. Jio Business Messaging (JBM) is integrated.** Built from Jio's "User and
API Integration Guide v2.3": OAuth token, plain-text send, capability, batch
reachability and the callback envelope. `internal/connector/rcs_jio.go`.

**2. RCS operators moved out of the environment into the database.** Until now a
deployment named ONE carrier in `RCS_VENDOR` and needed a deploy to change it.
Now `rcs_connections` holds an account per operator, exactly as `connections`
holds SMPP binds:

- several operators live at once — Jio **and** Airtel **and** Vi;
- enabled, disabled and re-credentialled without a deploy, picked up within a
  minute;
- every secret sealed with the same key SMPP bind passwords use, and never
  returned by any endpoint.

**3. A message goes out through an operator its agent is launched on.** With
several accounts configured, the send path takes the first operator in the
corridor's **route priority** that the sender's agent has an approved launch on.
An agent launched nowhere we can reach is refused `rcs_agent_not_resolved`, as
before — it is never quietly sent through another operator, because a brand only
exists on the networks it is launched on.

**4. Operators that hold no template are sent the rendered text.** Airtel and Vi
hold the approved template and render it themselves, so a campaign sends no
body. Google and Jio review the *agent*, not the message, and were refusing
those sends `body_required`. They now receive the template's own text, rendered
from the same contact values that fill a carrier-held template. Billing is
unchanged: cost is still priced from what the caller sent.

## Nothing in the API changed

No request or response shape moved. `POST /v1/rcs/capabilities` answers the same
way; it just picks the operator per agent now instead of using the one carrier
in the environment. `vendor` in its response can now be `jio`.

## What we need from the contract

**`RcsVendor` is missing two values.** It is `["airtel", "vi"]`, but the backend
has been answering `google` for a while and now answers `jio` too:

```json
"RcsVendor": {
  "type": "string",
  "enum": ["airtel", "vi", "jio", "google"],
  "description": "The carrier a registration or a capability answer belongs to. Lowercase because it names the vendor integration, not the network in a delivery report (CarrierId)."
}
```

If your client validates that enum, a Jio capability check fails on a value the
backend is entitled to send. Please add both.

## Optional: an operator-console screen

RCS accounts are managed from the server today, deliberately — the values are
operator credentials and a shell is a stronger boundary than an endpoint:

```
operator-admin rcs-connection list
operator-admin rcs-connection add jio "Jio JBM"            # prompts for hosts
operator-admin rcs-connection add-assistant <id> <assistant-id>
operator-admin rcs-connection enable <id>
operator-admin rcs-connection test <id> <assistant-id> +919876543210
```

If you want the same screen SMPP connections have, say so and we will design the
endpoints together — `GET/POST /v1/operator/rcs-connections`, plus enable,
disable and test, with secrets write-only exactly as the bind password is. We
have not added them to the contract unilaterally.

## One thing worth knowing about Jio

Jio issues a client id and secret **per assistant** — per customer brand — not
one login for the whole account. Airtel and Vi issue one account credential and
take the agent per call. So a Jio account holds a secret for each assistant, and
launching a brand on Jio is two steps:

```
operator-admin rcs-connection add-assistant <connection-id> <assistant-id>
operator-admin rcs-launch <agent-uuid> JIO <assistant-id>
```

The assistant id is both the credential's client id and the launch's
`carrier_agent_id`, so a delivery report can still be traced to one tenant.

Jio's webhook goes to `/v1/carrier-webhooks/rcs/jio/<RCS_WEBHOOK_TOKEN>`, and
Jio does not sign its callbacks, so the 40 source IPs in §5 of their guide
belong in `RCS_WEBHOOK_IP_ALLOWLIST`.
