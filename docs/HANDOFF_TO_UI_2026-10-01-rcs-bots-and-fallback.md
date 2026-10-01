# RCS on Trustsignal: SMS fallback notice, and bot onboarding coming (1 Oct 2026)

Follows `HANDOFF_TO_UI_2026-10-01-trustsignal-rcs.md`. We meet Trustsignal
(Sigmo) on 2 Oct; the full checklist is `docs/TRUSTSIGNAL_REQUIREMENTS.md`
(and `.pdf`) in SMS-BE.

**No contract change in this handoff.** One small UI change (§1), and
advance notice of form changes that will come with a contract update (§2).

---

## 1. Please do: SMS fallback notice for Trustsignal agents

**What we found.** A campaign's SMS fallback is chosen per recipient
**before** sending, from the carrier's "can this handset take RCS?" answer
(`POST /v1/rcs/capabilities`). Trustsignal has no such check (that route
answers 503 for a Trustsignal agent, as the previous handoff says). So on
Trustsignal every recipient is sent RCS, and a handset without RCS ends as
`failed` / `errorCode: unreachable_handset` (not charged) and **gets no SMS**.

Nothing in the API changes. Please add an **information notice** in the
campaign builder when both are true:

- the campaign's RCS sender is attached to an agent (`rcsAgentId`) whose only
  approved launch is Trustsignal: in the agent's `carrierLaunches[]`, the only
  entry with `status: "approved"` has `carrier: "TRUSTSIGNAL"`;
- the user has configured an SMS fallback.

Suggested text: *"SMS fallback isn't available on this RCS carrier yet.
Recipients whose phones can't receive RCS will be reported as failed and
won't be charged."*

Information style, not an error: the campaign still sends. We will remove the
need for it once fallback works after a failure (backend, after the call).

## 2. Coming next: brands create their RCS bot in Relay (no action yet)

Brands will fill in their bot in Relay; Trustsignal approves it; the
`bot_id` comes back to Relay (the launch's `carrierAgentId`). The RCS agent
form will grow. **Expected new fields** (final list depends on Trustsignal's
answer on 2 Oct; we'll send the contract change then):

| Field | Rule |
|---|---|
| Brand name | text |
| Description | max **100** characters (today unlimited) |
| Brand colour | contrast ≥ 4.5 : 1 against white (validation) |
| Phones, emails, websites | **several**, each with a **label** (today one each, no label) |
| Languages | list, e.g. English, Hindi |
| Use-case description | text |
| Sample messages | 2–3 texts |
| How users opt in / opt out | text |
| Expected monthly volume | number |
| Verification documents | several uploads (today one) |

Unchanged: name (≤ 40), logo 224×224 (≤ 50 KB), banner 1440×448 (≤ 200 KB),
terms and privacy URLs, use case OTP / TRANSACTIONAL / PROMOTIONAL.

Launch status will then move on its own (pending → approved / rejected with
Trustsignal's reason) instead of waiting for an operator command.

Please don't build §2 until the contract change arrives; reply with any
form-design questions.
