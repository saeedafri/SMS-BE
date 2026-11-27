# Handoff to UI, 27 Nov 2026: the API has moved to the Hostinger server, and WP-S to WP6 are live

## The API address changed

| | |
|---|---|
| Old | the address in your `.env.example` (unreachable since 23 Nov, now retired) |
| New | **`http://189.72.144.98:8080`** — the only deployment; every push to SMS-BE `main` deploys here |

Set `NEXT_PUBLIC_API_BASE_URL=http://189.72.144.98:8080` wherever you point at the real API
(`.env.example` still names the old host).

**It is plain HTTP for now.** That server's ports 80 and 443 belong to software that is not
Relay's, so there is no TLS yet. A page served over HTTPS (Vercel) cannot call an `http://` API from
the browser: it is blocked as mixed content. Calls your Next.js server makes (the BFF routes) are
not affected. Until there is an HTTPS address, run the dashboard against it locally, or proxy
browser calls through your server. We will send a follow-up when there is an HTTPS address.

Demo logins are the seeded ones: `founder@acme.test` / `relay-dev`, operator
`ops@relay.internal` / `relay-ops-dev`.

## Your 24 Nov handoff (WP-S, WP1 to WP6) is deployed and tested

The database tests ran for the first time on 27 Nov. They found 11 failures; all are fixed, and the
full suite is 1349 pass, 0 fail. The ones you can see from the UI:

- **Email campaigns now send.** Every live email was refused `carrier_template_not_approved`
  before (`3dc692f`).
- **All 24 `WebhookEventType` values can be subscribed to.** The API accepted only the original 14,
  so `message.bounced`, `message.complained`, `message.unsubscribed` and the seven WhatsApp
  `sender.*` / `template.*` events were refused `validation_failed` (`3dc692f`).
- **A link-shortener refusal names the host** (`bit.ly is a URL shortener. …`), like every other
  approved-link refusal.

Against the deployed API on 27 Nov: all 267 contract operations called signed out and signed in,
no 5xx (voice-call answers 501 by design); 107/107 documented GET routes exist and 38/38 key-scope
rules hold.

Your mocks can come off package by package, against the address above.

## One question back

A test-key send on a **sandbox** WhatsApp channel is still refused `onboarding_incomplete` when
that number's WhatsApp setup is unfinished (WP4's rule runs before WP1's). Should a simulated
send skip the setup check? Until you say, it does not.
