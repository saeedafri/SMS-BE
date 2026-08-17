#!/usr/bin/env bash
# Relay role-access evidence matrix.
#
# Gathers, asserts nothing. Every request goes to the LIVE API over TLS, with a
# real session token obtained by a real login — the same path a browser session
# takes. The point is to measure where the frontend's idea of access differs
# from the backend's, rather than to read either side's code and form an opinion.
#
# Writes are included deliberately: read access proves a screen opens, not that
# a button works. The caller resets demo state afterwards.
set -uo pipefail
B=${API:-https://sms-api.saqibsaeed.cloud}

tok() {
  curl -s --max-time 20 -X POST "$B/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$1\",\"password\":\"relay-dev\"}" \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("session",{}).get("token",""))'
}

OWNER=$(tok founder@acme.test)
ADMIN=$(tok priya@acme.test)
MEMBER=$(tok sam@acme.test)
for n in OWNER ADMIN MEMBER; do
  [ -n "${!n}" ] || { echo "FATAL: $n failed to log in" >&2; exit 1; }
done

# 403 = refused by authorization. 422 = authorization PASSED, body rejected.
# That distinction is what separates "this role is refused" from "this endpoint
# is broken", so the matrix keeps the raw code rather than a boolean.
probe() { # method path token [body]
  local m=$1 p=$2 t=$3 body=${4:-}
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 25 -X "$m" "$B$p" \
      -H "Authorization: Bearer $t" -H 'Content-Type: application/json' -d "$body"
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 25 -X "$m" "$B$p" -H "Authorization: Bearer $t"
  fi
}

row() { # area method path uirule [body]
  local area=$1 m=$2 p=$3 rule=$4 body=${5:-}
  printf '%-11s %-6s %-34s %-6s %-6s %-6s  %s\n' \
    "$area" "$m" "$p" \
    "$(probe "$m" "$p" "$OWNER" "$body")" \
    "$(probe "$m" "$p" "$ADMIN" "$body")" \
    "$(probe "$m" "$p" "$MEMBER" "$body")" \
    "$rule"
}

printf '%-11s %-6s %-34s %-6s %-6s %-6s  %s\n' AREA METHOD PATH OWNER ADMIN MEMBER "UI ALLOWS"
printf '%s\n' "-------------------------------------------------------------------------------------------------"

# --- settings: UI allows owner+admin ------------------------------------
row settings   GET   /v1/me                          "owner+admin"
row settings   PATCH /v1/tenant                      "owner+admin" '{"name":"Acme Retail"}'
row settings   POST  /v1/team/invite                 "owner+admin" '{"email":"audit-probe@t.test","role":"member"}'
row settings   GET   /v1/team                        "owner+admin"
row settings   GET   /v1/sessions                    "owner+admin"

# --- developer: UI allows owner+admin -----------------------------------
row developer  GET   /v1/developer/api-keys?environment=live "owner+admin"
row developer  POST  /v1/developer/api-keys          "owner+admin" '{"name":"audit-probe","environment":"live","scopes":["messages:write"]}'
row developer  GET   /v1/developer/webhooks?environment=live "owner+admin"
row developer  GET   /v1/developer/ip-allowlist      "owner+admin"

# --- compliance: UI allows owner+admin ----------------------------------
row compliance GET   /v1/registrations               "owner+admin"
row compliance POST  /v1/sender-ids                  "owner+admin" '{"header":"AUDITP","channel":"SMS","country":"IN"}'

# --- billing: UI allows OWNER ONLY --------------------------------------
row billing    GET   /v1/wallet/balances             "OWNER only"
row billing    GET   /v1/wallet/ledger               "OWNER only"
row billing    GET   /v1/wallet/auto-recharge        "OWNER only"
row billing    GET   /v1/wallet/payment-methods      "OWNER only"
row billing    GET   /v1/invoices                    "OWNER only"
row billing    PUT   /v1/wallet/auto-recharge        "OWNER only" '{"currency":"INR","enabled":false,"thresholdMinor":100,"topUpMinor":100}'
row billing    POST  /v1/wallet/topup                "OWNER only" '{"amountMinor":100,"currency":"INR","paymentMethodId":"00000000-0000-0000-0000-000000000000"}'
