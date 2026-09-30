# RCS through Trustsignal (Sigmo) (1 Oct 2026)

Relay now sends RCS through **Trustsignal**, the platform Sigmo (sigmo.ai)
resells. It is a fifth RCS carrier next to Airtel, Vi, Jio and Google. Unlike
them it is an aggregator: one account reaches every Indian network.

| Where it appears | Value |
|---|---|
| Vendor (template registration, capability answers) | `trustsignal` |
| Carrier (agent launches, routes, delivery reports) | `TRUSTSIGNAL` |

Nothing changes on the wire for the existing carriers. Everything below is
live on the backend now.

---

## 1. Contract: please add to `openapi.json`

**`RcsVendor` gains `trustsignal`.** It is used in three places:

- `POST /v1/templates/{id}/carrier-registration` request `vendor`. The backend
  already accepts `trustsignal`.
- `CarrierTemplateRegistration.vendor`, which can now read `trustsignal`.
- `RcsCapabilityReport.vendor`.

A 422 for an unknown vendor now lists `airtel, google, jio, trustsignal, vi`.

## 2. Templates: "Submit to carrier"

- Add **Trustsignal** to the carrier picker.
- **Rich card templates can now be submitted automatically to Trustsignal.**
  Text templates and single-card templates both go, with their buttons (reply,
  open URL, dial). Variables are converted for you: `{{first_name}}` becomes
  Trustsignal's `[first_name]`.
- **Airtel still takes text only.** A card submitted to Airtel answers 422:
  "Airtel takes text templates here; create the card in Airtel's portal and
  attach its code." Show that message as-is.
- **Carousels are not supported by any carrier here**, because Relay templates
  have no carousel shape. If you want carousels, that is a contract addition
  (`RcsContent` kind `carousel`); tell us and we'll wire Trustsignal's carousel
  API to it.
- After submitting, `carrierRegistration.status` is `pending`. Trustsignal's
  review result arrives by webhook and flips it to `approved` or `rejected`
  (with `rejectionReason`). Re-read the template to show the change; no new
  route is involved.
- A content refusal from Trustsignal (bot not active, bad media, …) comes back
  as **422 with their words** in `error.message`. Show it. A 502 means they
  could not be reached; offer a retry.

## 3. RCS agents

- An agent's `launches[]` now include a `TRUSTSIGNAL` row, with `reachable:
  true` once the Trustsignal account is enabled on the deployment.
- The Trustsignal bot id is the launch's `carrierAgentId`.

## 4. Reachability check (`POST /v1/rcs/capabilities`)

Trustsignal has **no way to check a handset before sending**. When the agent's
carrier is Trustsignal, the route answers **503**: "This agent's carrier cannot
check handsets before a send. RCS is attempted, and a handset that cannot take
it is reported as failed."

Show that as an information notice, not an error or outage banner. Sending is
unaffected.

## 5. Message status

No new values. Trustsignal's reports map onto the existing ones:

| Trustsignal says | Message shows |
|---|---|
| `delivered` | `delivered` |
| `read` | `read` (delivered + `readAt`) |
| `nonrcs` (handset cannot take RCS) | `failed`, `errorCode: unreachable_handset` |
| `failed` | `failed`, `errorCode: carrier_failed` |

New `errorCode` values a message can carry at send time:

- `carrier_account_unfunded`: the Trustsignal account is out of credits.
- `template_not_approved`: Trustsignal does not know or has not approved the template.
- `carrier_unauthorized`: the API key was refused.

Show these in the message drawer next to the existing codes.

## 6. Operator console

Nothing new is needed. The Trustsignal account is set up by the operator on the
server (`operator-admin rcs-connection add trustsignal …`), like the other RCS
carriers.
