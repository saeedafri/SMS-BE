# Handoff to UI — sign-in backoff changed, and a new 429 on every route (15 Sep 2026)

Both are live on `sms-api.saqibsaeed.cloud` (`141ad7c`). No contract change: no new
status, no new error code, no new field.

## 1. Sign-in backoff (founder's decision)

Was: customers five wrong passwords then 15 minutes; operators three then 30 minutes.
Now, on **both** surfaces:

| Wrong passwords | Lock |
| --- | --- |
| 3 | 30 seconds |
| 3 more | 1 minute |
| 3 more | 2 minutes, then 4, 8, 16 … |
| Ceiling | 1 hour (customer), 4 hours (operator) |

- A success resets the count; the ladder itself resets after 24 hours with no lock.
- The right password during a lock is still refused (`too_many_attempts`), unchanged.
- The per-IP rule is unchanged (20 failures for customers, 10 for operators).
- The message now says the wait in seconds, minutes or hours:
  `Too many sign-in attempts. Try again in 30 seconds.` Your `sign-in-refusal.ts`
  shows `err.message` verbatim, so nothing to change there.

**Please update:** `src/mocks/login-limits-state.ts` still encodes five attempts and
15 minutes, and any test asserting "15 minutes" copy.

Live, just now:

```
attempt 1-3  unauthenticated | Incorrect email address or password.
attempt 4    too_many_attempts | Too many sign-in attempts. Try again in 30 seconds.
(after the lock) attempts 1-3 refused again, 4th: Try again in 1 minute.
```

## 2. Abuse guard: a 429 any route can now answer

An address over `ABUSE_IP_PER_MINUTE` (1200 in production) is banned — 15 min, then
1 h, 4 h, 24 h within a day. A credential (session or API key) over
`ABUSE_TOKEN_PER_MINUTE` (6000) is refused for the rest of that minute and never
banned. Both answer:

```
HTTP 429  Retry-After: <seconds>
{"error":{"code":"rate_limited","message":"Too many requests. Try again in 15 minutes."}}
```

`rate_limited` is already in the contract (`sendMessage`, `createVerification`), so
this is the same code on more routes. **Please make sure a dashboard fetch treats a
429 on any route as "wait and retry", using `Retry-After`, rather than as a crash.**

Exempt: `/healthz` and `/v1/carrier-webhooks/*`. Never banned: addresses in
`ABUSE_IGNORE_CIDRS`. Redis unreachable fails open.

**The ban falls on the real user, not on you** — but only once ask 42 is switched on.
Until the dashboard sends `X-Relay-BFF-Token` and `X-Relay-Client-IP`, every dashboard
request counts against your Vercel egress address. At today's volume 1200/minute is
far away, but it is the reason to finish ask 42 soon: say who sets the token in Vercel
and we will set both sides together.

## 3. Also live (server only, nothing to change)

- nginx: 30 requests/second and 100 connections per address on every route, 429 with `Retry-After`.
- fail2ban: an address nginx refuses 20 times in a minute is firewall-banned 1 hour;
  a path scanner (non-`/v1/` 404s) 1 day; three bans in a day, 1 week. Requests
  carrying the dashboard's BFF header never count toward these.
