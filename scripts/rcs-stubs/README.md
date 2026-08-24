# RCS carrier stubs

Stand-ins for Airtel IQ and Vi RBM, so the RCS path can be exercised without a
commercial agreement with either.

These are not mocks in the "return a fixed blob" sense. They reproduce the
vendor behaviours that actually cost work to get right, transcribed from the two
API specifications — the ones a naive stub would smooth over and let a real bug
through:

**Airtel** (`airtel.py`, port 9099)

- capability discovery is a **GET carrying a JSON body**
- every response is an envelope whose `success` flag is authoritative, so a
  failure can arrive with HTTP 200
- an **unreachable handset is a FAILED envelope wrapping a Google 404**, not an
  empty answer — the case that makes every non-RCS subscriber look like an
  outage if read naively
- the bulk reachability endpoint **refuses lists under 500 numbers**
- template registration returns `PENDING` and never approves itself; a send
  against an unapproved template is refused

**Vi** (`vi.py`, port 9098)

- OAuth2 `client_credentials` against a **separate host**, Basic-authed, and the
  token endpoint **refuses past five mints** — an uncached client fails loudly
  here instead of quietly exhausting the real 60-per-minute account budget
- capability discovery at the **Google-style path** (§3.5), answering **200 with
  `{}`** for a handset with no RCS rather than an error
- send takes the **caller's own `messageId`**, which is what makes Vi's delivery
  webhooks correlatable at all
- **no template API**, because Vi has none

In both, a number ending in an even digit is reachable and an odd one is not, so
either case can be asked for on demand.

## Running

```bash
python3 scripts/rcs-stubs/airtel.py     # :9099
python3 scripts/rcs-stubs/vi.py         # :9098
```

Then point the API at one — never both, since the API serves a single RCS
carrier and refuses to start with two configured and no `RCS_VENDOR`:

```bash
# Airtel
export RCS_AIRTEL_BASE_URL=http://127.0.0.1:9099/gateway/airtel-xchange
export RCS_AIRTEL_AUTH_TOKEN=ZmFrZTpmYWtl        # base64 of fake:fake
export RCS_AIRTEL_AGENT_ID=relay_local_agent
export RCS_AIRTEL_CUSTOMER_ID=Profile_local
export RCS_AIRTEL_SUBACCOUNT_ID=sub-local
export RCS_WEBHOOK_TOKEN=local-webhook-token

# Vi
export RCS_VENDOR=vi
export RCS_VI_BASE_URL=http://127.0.0.1:9098
export RCS_VI_TOKEN_URL=http://127.0.0.1:9098/auth/oauth/token
export RCS_VI_CLIENT_ID=vi-client
export RCS_VI_CLIENT_SECRET=vi-secret
export RCS_VI_BOT_ID=OsQ0GwNvUdLTV9Bd
export RCS_WEBHOOK_TOKEN=local-webhook-token
```

Each stub exposes a small `/_control` surface that has no counterpart in the
real vendor API, for driving a test past something that would otherwise take a
day: `POST /_control/approve-template` and `GET /_control/templates` on Airtel,
`GET /_control/state` on Vi.

The browser specs `e2e/rcs-capabilities.spec.ts` and
`e2e/rcs-carrier-template.spec.ts` in the frontend repo run against these, and
skip themselves cleanly when no carrier is configured.
