# RCS go-live: sending the first message through Trustsignal (Sigmo)

State as of 1 Oct 2026. The code is live on the AWS server (commit 3128554).
What is left is account setup at Sigmo and a few steps in Relay.

---

## 1. What Relay can do today, per RCS carrier

India's RCS networks are Jio, Airtel and Vi. BSNL has no RCS API. Relay has an
adapter for each network directly, plus Google's test platform, plus
Trustsignal, which reaches all three networks through one account.

| Carrier | Send | Create templates by API | Read template status | "Can this phone take RCS?" | Delivery / read / reply webhooks | Credentials it needs |
|---|---|---|---|---|---|---|
| **Trustsignal (Sigmo)** | yes | yes: text and rich card, with buttons | yes | **no** (the API has none) | yes | API key (**we have it**); bot id per brand |
| Airtel | yes | text only (a card is refused with a clear message) | yes | yes | yes | base URL, customer id, sub-account id, auth token; agent id per brand |
| Vi | yes | no API: create in Vi's portal, paste the code into Relay | no | yes | yes | base URL, token URL, client id, client secret; bot id per brand |
| Jio | yes | not needed: Jio reviews the agent, not each template | not needed | yes | yes | assistant id + secret per brand |
| Google RBM (test only) | yes | not needed | not needed | yes | yes | service account JSON; agent id |

Every carrier plugs into the same flow: accounts, agents and launches, carrier
template approval, the send path, the wallet hold and delivery settlement. A
new carrier account is one command on the server (§4 step 1), with no deploy.

**We are going live on Trustsignal.** The others are ready for whenever their
contracts and credentials arrive.

---

## 2. What we need from you, or from Sigmo

These block the first send. The API key works, but the Sigmo account is
currently empty: **0 bots, 0 templates, 0 SMS senders, 0 credits.**

### 2.1 Credits on the Sigmo account (blocker)
- RCS balance is **0**. Every send will be refused until the account is topped up.
- Please also get Sigmo's **per-message RCS price**. Relay charges tenants
  **₹0.35 per RCS message** today (`pricing_rates`, IN/RCS). That price has to
  sit above Sigmo's cost.

### 2.2 An approved RCS bot (blocker; carrier approval takes days)
A bot is the sender identity shown on the phone: name, logo, colour. Create it
in Sigmo (RCS → RCS Settings → Bots), or ask Sigmo to create it. It needs:

| Field | Rule |
|---|---|
| Bot name | what the user sees at the top of the chat, max 40 characters |
| Brand name | the company behind it |
| Description | max 100 characters |
| Logo | **224 × 224 px**, JPG/PNG, max **90 KB** |
| Banner | **1440 × 448 px**, JPG/PNG, max **360 KB** (the logo sits on its bottom centre) |
| Colour | hex, contrast at least 4.5 : 1 against white |
| Phone, email, website | at least one of each, with labels |
| Terms URL, Privacy URL | public pages |
| Message type | **promotional**, **transactional** or **otp**. Templates must match it: a promotional template under a transactional bot is rejected |
| Languages | e.g. English |

**Decision for you:** whose brand is the first bot (Textify, or a customer),
and which message type.

**Then send me the bot's `bot_id`** once its status is **active** (Sigmo → RCS
→ Bots).

### 2.3 The webhook in Sigmo (needed for delivery reports)
Sigmo → RCS → RCS Settings → Webhooks → **Add Event Webhook**:

- URL: `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/rcs/trustsignal/<token>`.
  The token is sent to you separately; it is a secret and is kept out of this repo.
- Events: **Delivery Status**, **Template**, **User Response**. Agent Delivery
  Status is optional.

Without it, messages send but stay at "sent" for ever, and template approvals
never reach Relay. You told me not to change anything in the portal, so either
add it yourself or tell me to.

Also ask Sigmo **which IP addresses their webhooks come from**. The endpoint is
protected only by the secret token today; with their IPs we can allow those
addresses alone.

### 2.4 Test phones
One or two numbers on **Android with Google Messages and RCS chats turned on**.
They must be opted in to RCS as contacts in the sending tenant.

### 2.5 Which tenant sends
Name the Relay tenant for the first send. Its registration must be approved and
its wallet funded. Live today: **Acme Retail** has one approved RCS agent
("Live v3") and an RCS sender `PR8825` attached to it.

### 2.6 SMS fallback (optional)
Relay runs its own fallback: an RCS campaign can fall back to SMS through our
SMS route (today the VIDEOCON SMPP bind). It needs an SMS sender and a
DLT-registered SMS template on the campaign. **But with Trustsignal it never
fires:** Relay picks the fallback *before* sending, from the carrier's "can this
phone take RCS?" check, and Sigmo has no such API, so every contact goes RCS and
a phone without RCS fails as `nonrcs` with no SMS. Either use Sigmo's own
`sms_fallback`, or build an after-failure fallback in Relay (~1–2 days).

---

## 3. Credentials: have and need

| Item | Status |
|---|---|
| Trustsignal API key | ✅ have. It goes into the database encrypted (§4 step 1), never into files or the repo |
| Trustsignal base URL | ✅ `https://rcsapi.trustsignal.io` (the default) |
| Webhook token on our server | ✅ set on 1 Oct and the endpoint is live |
| Sigmo credits | ❌ 0 |
| Bot id | ❌ no bot yet |
| Approved template | ❌ we create it from Relay once the bot exists (§4 step 4) |
| Webhook configured in Sigmo | ❌ |
| Test phone numbers | ❌ |

---

## 4. What happens once §2 is done (Relay side, in order)

1. **Store the Sigmo account in Relay.** You run this, because it asks for the
   API key without echoing it and needs a real terminal:
   ```
   ssh -t relay-aws 'sudo bash -c "set -a; . /opt/relay/.env; set +a; cd /opt/relay && ./operator-admin rcs-connection add trustsignal Sigmo"'
   ```
   Press Enter at `baseUrl` to keep the default, then paste the API key. It
   prints a connection id. Then:
   ```
   ssh -t relay-aws 'sudo bash -c "set -a; . /opt/relay/.env; set +a; cd /opt/relay && ./operator-admin rcs-connection enable <id>"'
   ```
   The API picks it up within a minute. Its boot line then reads `operators=[TRUSTSIGNAL]`.
2. **Link the tenant's RCS agent to the Sigmo bot:**
   `operator-admin rcs-launch <agent-uuid> TRUSTSIGNAL <bot_id>`
   (the agent's verification must be approved in the operator console first).
3. **The RCS sender** must have that agent attached (`PR8825` already has one).
4. **The template.** Create it in the Relay dashboard, text or rich card with
   buttons; the operator approves it; then **Submit to carrier → Trustsignal**.
   Relay converts `{{name}}` to Sigmo's `[name]` and creates it under the bot.
   Sigmo's approval comes back through the webhook and unlocks sending.
   *Alternative:* create the template in Sigmo's portal and paste its template
   id into Relay ("attach code").
5. **Optional:** a route row RCS / IN / `TRUSTSIGNAL` in the operator console,
   so reports order it first. Sending works without it.
6. **First send.** One message to a test phone, then check it:
   - the submit is accepted and Sigmo returns a `transaction_id`, stored as the carrier reference;
   - the webhook reports `delivered`, then `read`;
   - it shows in Operator console → Live campaigns / messages, with its status and receipt.

A refused send shows why in the message's `errorCode`:

| errorCode | Meaning |
|---|---|
| `carrier_account_unfunded` | no Sigmo credits |
| `template_not_approved` | Sigmo doesn't know or hasn't approved the template |
| `carrier_unauthorized` | the API key was refused |
| `unreachable_handset` | the phone can't take RCS (Sigmo's `nonrcs`); the tenant is not charged |
| `carrier_failed` | the network failed it |

---

## 5. Open points, stated plainly

- **`sms_fallback`.** Sigmo's docs list it as required on every send. We leave
  it out, because Relay does its own fallback. If the first live send is
  refused for that reason, it is a small change.
- **No pre-send reachability check.** Sigmo has no such API. A phone without
  RCS gets `nonrcs` after the send. The UI shows "can't check before sending"
  rather than an error.
- **Template names.** Sigmo's naming rules aren't documented. A name it refuses
  comes back as a readable 422 with Sigmo's own words.
- **Carousels.** Sigmo supports them, but Relay's template model has no
  carousel yet. That needs a contract change first; the UI team has been told.
