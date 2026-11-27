# Handoff to UI, 27 Nov 2026 (later): the API's address is https://srv2130239.hstgr.cloud

This replaces the address in `HANDOFF_TO_UI_2026-11-27_api-on-hostinger.md`.

| | |
|---|---|
| **API** | **`https://srv2130239.hstgr.cloud`** — HTTPS, Let's Encrypt, renews itself |
| No longer | `http://189.72.144.98:8080` (port 8080 is closed to the internet now) and the old `sms-api` domain (that domain's account was deleted, so it cannot be pointed anywhere) |

Set `NEXT_PUBLIC_API_BASE_URL=https://srv2130239.hstgr.cloud` on Vercel and redeploy (a
`NEXT_PUBLIC_` value is baked in at build time), and in `.env.example`. Nothing else changes: same
routes, same contract, same demo logins (`founder@acme.test` / `relay-dev`, operator
`ops@relay.internal` / `relay-ops-dev`). The HTTPS problem in the earlier note is gone.

`srv2130239.hstgr.cloud` is the free name Hostinger gives the server. If a custom domain is bought
later, the API will move to it and we will say so.
