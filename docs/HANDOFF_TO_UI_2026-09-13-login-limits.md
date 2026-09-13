# Sign-in limits: what the UI needs to handle

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 13 September 2026

Password guessing is now limited on the server. **No contract change and no new status code**: every refusal uses a response the contract already declares. The UI needs two small changes and one wording check.

---

## 1. What the backend now does

| Surface | Rule | Response while locked |
|---|---|---|
| `POST /v1/auth/login` | 5 wrong passwords for one address within 15 min lock that address for 15 min. Each repeat lock within 24 h doubles it, up to 1 h. 20 failures from one IP (any addresses) block that IP for 15 min. | **401**, `error.code = "too_many_attempts"` |
| `POST /v1/operator/login` | 3 wrong within 15 min lock for 30 min, doubling up to 4 h. 10 failures from one IP block it for 15 min. | **401**, `error.code = "too_many_attempts"` |
| `POST /v1/auth/password/forgot` | At most 3 reset emails per address per hour | **204** as always. Extra requests are silently not sent. |
| Verification email (signup, `verify-email/resend`) | At most 3 per address per hour | **204** as always. Extra requests are silently not sent. |
| All sign-in endpoints (nginx) | More than 10 requests/second per client, burst 20 | **429** (plain nginx response, not our JSON envelope) |

Properties that matter for the UI:
- **A correct password is also refused while locked.** Repeating it doesn't help, so the UI must not suggest it will.
- **Unknown and real addresses get identical answers**, including the lock, so the page must not word anything as "this account exists".
- **A successful sign-in resets the counter.** Two typos followed by the right password never contribute to a later lock.
- **The 2FA challenge was already single-use.** A wrong code ends the attempt (`410 challenge_expired`), so there is nothing new there.

Example locked response (message text is final; the minutes vary):
```json
{"error":{"code":"too_many_attempts","message":"Too many sign-in attempts. Try again in 15 minutes."}}
```

---

## 2. What the UI must change

### 2.1 Show the lock message instead of "wrong password" — **required**
On both `/login` and `/operator/login`, a 401 is currently rendered as "Incorrect email address or password". Branch on `error.code`:

- `unauthenticated` → the existing wrong-credentials message, unchanged.
- `too_many_attempts` → show **`error.message` verbatim** (it carries the wait time). Suggested treatment: a non-dismissable warning banner, the password field cleared, and a **"Forgot password?"** link emphasised. Do not auto-retry.

If the mock (`src/mocks/handlers.ts`) models login, please add this branch too: for example 5 wrong passwords for one address return `too_many_attempts`, so the e2e can cover it.

### 2.2 Handle a plain 429 on sign-in pages — **required**
The nginx layer answers **429 with an HTML/plain body, not JSON**. Today that would probably surface as a JSON parse error. On any sign-in form, treat a 429, or any non-JSON error response, as: *"Too many requests. Please wait a moment and try again."*

### 2.3 Forgot-password and resend copy — **check only**
These still return 204 when the hourly cap is reached, and no email goes out. Keep the current neutral confirmation ("If an account exists for that address, we've sent a link"). For **Resend verification**, please disable the button for 60 seconds after a click and say "Sent. It can take a minute to arrive." That stops users hitting the 3-per-hour cap by accident.

---

## 3. Acceptance for the UI

1. Enter a wrong password 5 times for one address on `/login`. The 6th attempt, **even with the correct password**, shows "Too many sign-in attempts. Try again in N minutes."
2. The same on `/operator/login` after 3 wrong attempts.
3. A 429 from the edge shows the "wait a moment" message, not a crash or a raw parse error.
4. Resend verification is disabled for 60 seconds after each click.

Nothing to regenerate. `make generate` is unaffected, because the contract is unchanged.
