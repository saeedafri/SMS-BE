#!/usr/bin/env bash
# Adversarial end-to-end checks: the things that break systems in production.
#
# This is deliberately NOT the happy path — e2e-check.sh covers that. Every
# case here is an attempt to make the API leak data, corrupt money, accept
# something it must refuse, or fall over. A pass means the attack was REFUSED,
# not that it succeeded.
set -uo pipefail
cd "$(dirname "$0")/.."

# .env supplies DEFAULTS; anything already exported wins.
#
# This used to be a bare `set -a; . ./.env; set +a`, which clobbered an exported
# DATABASE_ADMIN_URL with the local development one. API was overridable and the
# database URLs were not, so running this against a deployed server — the one
# time you most want it — pointed the 32 HTTP checks at the remote API and the
# money checks at the LOCAL database. Those are the most important checks in
# this file: the ledger's append-only guarantee, verified against a database
# that had never seen the tenant under test. It reported three failures that
# said nothing about the server being examined, and it would just as happily
# have reported a PASS for an invariant it never tested.
_env_admin=${DATABASE_ADMIN_URL:-}
_env_db=${DATABASE_URL:-}
set -a; [ -f ./.env ] && . ./.env; set +a
[ -n "$_env_admin" ] && DATABASE_ADMIN_URL=$_env_admin
[ -n "$_env_db" ] && DATABASE_URL=$_env_db

API=${API:-http://localhost:8080}
pass=0; fail=0

check() { # name expected actual
  if [[ "$2" == "$3" ]]; then
    printf '  \033[32mPASS\033[0m  %-52s %s\n' "$1" "$3"; pass=$((pass+1))
  else
    printf '  \033[31mFAIL\033[0m  %-52s got=%s want=%s\n' "$1" "$3" "$2"; fail=$((fail+1))
  fi
}
code() { curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$@"; }

stamp=$(date +%s%N)
signup() {
  curl -s -X POST "$API/v1/auth/signup" -H 'content-type: application/json' \
    -d "{\"fullName\":\"$1\",\"email\":\"$1-$stamp@t.test\",\"password\":\"chaos-password-123\",\"orgName\":\"$1 $stamp\",\"country\":\"IN\"}" \
    | jq -r .token
}

# Baseline the 5xx counter: it is cumulative since process start, so asserting
# on the absolute value would fail for reasons that have nothing to do with
# this run. What matters is that the attacks below cause NO new server errors.
errors_before=$(curl -s "$API/metrics" | jq -r '.requests."5xx"')

ATTACKER=$(signup attacker)
VICTIM=$(signup victim)
AT="authorization: Bearer $ATTACKER"
VI="authorization: Bearer $VICTIM"

victim_tenant=$(curl -s "$API/v1/me" -H "$VI" | jq -r .tenantId)
victim_sender=$(curl -s -X POST "$API/v1/sender-ids" -H "$VI" -H 'content-type: application/json' \
  -d '{"header":"VICTIM","channel":"SMS","country":"IN"}' | jq -r .id)
victim_list=$(curl -s -X POST "$API/v1/contact-lists" -H "$VI" -H 'content-type: application/json' \
  -d '{"name":"Victim private list"}' | jq -r .id)

echo
echo "=== TENANT ISOLATION — the failure that ends the company ==="
check "read another tenant's sender by id"      404 "$(code "$API/v1/sender-ids/$victim_sender" -H "$AT")"
check "delete another tenant's list"            404 "$(code -X DELETE "$API/v1/contact-lists/$victim_list" -H "$AT")"
check "rename another tenant's list"            404 "$(code -X PATCH "$API/v1/contact-lists/$victim_list" -H "$AT" -H 'content-type: application/json' -d '{"name":"pwned"}')"
# 404 not 403 is deliberate: a 403 would confirm the id exists.
check "victim's list is untouched"              "Victim private list" \
  "$(curl -s "$API/v1/contact-lists" -H "$VI" | jq -r '.[] | select(.id=="'"$victim_list"'") | .name')"
check "attacker sees zero of victim's lists"    0 \
  "$(curl -s "$API/v1/contact-lists" -H "$AT" | jq '[.[] | select(.name=="Victim private list")] | length')"

echo
echo "=== AUTHENTICATION ==="
check "no token"                                401 "$(code "$API/v1/me")"
check "garbage token"                           401 "$(code "$API/v1/me" -H 'authorization: Bearer nonsense')"
check "empty bearer"                            401 "$(code "$API/v1/me" -H 'authorization: Bearer ')"
check "wrong scheme"                            401 "$(code "$API/v1/me" -H "authorization: Basic $ATTACKER")"
check "token as query param is not accepted"    401 "$(code "$API/v1/me?token=$ATTACKER")"

echo
echo "=== INJECTION ==="
# Parameterised queries mean these are stored as literal text, never executed.
sqli_name='x'"'"'); DROP TABLE contacts;--'
check "sqli in body is stored, not executed"    201 \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/v1/contact-lists" -H "$AT" \
     -H 'content-type: application/json' --data-raw "$(jq -nc --arg n "$sqli_name" '{name:$n}')")"
check "contacts table survived"                 t "$(psql "$DATABASE_ADMIN_URL" -tA -c 'SELECT count(*)>=0 FROM contacts')"
check "tenants table survived"                  t "$(psql "$DATABASE_ADMIN_URL" -tA -c 'SELECT count(*)>0 FROM tenants')"
check "sqli in query param"                     422 "$(code "$API/v1/messages?status=%27%3BDROP%20TABLE%20tenants%3B--" -H "$AT")"

echo
echo "=== MALFORMED INPUT ==="
check "broken json"                             422 "$(code -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-raw '{broken')"
check "wrong field type"                        422 "$(code -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-raw '{"name":12345}')"
check "json null body"                          422 "$(code -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-raw 'null')"
check "empty body"                              422 "$(code -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-raw '')"
check "deeply nested json"                      422 "$(python3 -c "print('{\"name\":'+'['*400+']'*400+'}')" | curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-binary @-)"
check "unicode and emoji accepted"              201 "$(code -X POST "$API/v1/contact-lists" -H "$AT" -H 'content-type: application/json' --data-raw '{"name":"日本語 🚀 ünïcode"}')"
check "malformed uuid in path"                  422 "$(code "$API/v1/campaigns/not-a-uuid" -H "$AT")"
check "valid uuid that does not exist"           404 "$(code "$API/v1/campaigns/00000000-0000-0000-0000-000000000000" -H "$AT")"

echo
echo "=== MONEY — the rules that must never bend ==="
check "negative topup refused"                  422 "$(code -X POST "$API/v1/wallet/topup" -H "$AT" -H 'content-type: application/json' --data-raw '{"amountMinor":-500000,"currency":"INR","paymentMethodId":"00000000-0000-0000-0000-000000000000"}')"
check "zero topup refused"                      422 "$(code -X POST "$API/v1/wallet/topup" -H "$AT" -H 'content-type: application/json' --data-raw '{"amountMinor":0,"currency":"INR","paymentMethodId":"00000000-0000-0000-0000-000000000000"}')"
check "topup with unknown payment method"       422 "$(code -X POST "$API/v1/wallet/topup" -H "$AT" -H 'content-type: application/json' --data-raw '{"amountMinor":100,"currency":"INR","paymentMethodId":"00000000-0000-0000-0000-000000000000"}')"
# Fund the victim so there IS a ledger row: an UPDATE matching zero rows fires
# no trigger and would pass this check while proving nothing.
psql "$DATABASE_ADMIN_URL" -q -c "INSERT INTO wallet_balances (tenant_id,currency,balance_minor) VALUES ('$victim_tenant','INR',1000) ON CONFLICT DO NOTHING" >/dev/null 2>&1
psql "$DATABASE_ADMIN_URL" -q -c "INSERT INTO wallet_ledger (tenant_id,currency,entry_type,amount_minor,balance_after_minor,description) VALUES ('$victim_tenant','INR','topup',1000,1000,'chaos fixture')" >/dev/null 2>&1
check "the fixture ledger row exists"           1 "$(psql "$DATABASE_ADMIN_URL" -tA -c "SELECT count(*) FROM wallet_ledger WHERE tenant_id='$victim_tenant'")"
ledger_update=$(psql "$DATABASE_ADMIN_URL" -tA -c "UPDATE wallet_ledger SET amount_minor=1 WHERE tenant_id='$victim_tenant'" 2>&1 | grep -c 'append-only')
check "ledger UPDATE refused even as owner"     1 "$ledger_update"
ledger_delete=$(psql "$DATABASE_ADMIN_URL" -tA -c "DELETE FROM wallet_ledger WHERE tenant_id='$victim_tenant'" 2>&1 | grep -c 'append-only')
check "ledger DELETE refused even as owner"     1 "$ledger_delete"

echo
echo "=== RESOURCE LIMITS ==="
check "oversized page limit is bounded"         200 "$(code "$API/v1/contacts?limit=999999" -H "$AT")"
check "negative limit does not crash"           200 "$(code "$API/v1/contacts?limit=-5" -H "$AT")"
check "unknown endpoint"                        404 "$(code "$API/v1/does-not-exist" -H "$AT")"
check "wrong method on real endpoint"           405 "$(code -X DELETE "$API/v1/me" -H "$AT")"

echo
echo "=== STILL ALIVE ==="
check "healthz ok after every attack"           ok  "$(curl -s "$API/healthz" | jq -r .status)"
check "readyz ready"                            ready "$(curl -s "$API/readyz" | jq -r .status)"
errors_after=$(curl -s "$API/metrics" | jq -r '.requests."5xx"')
check "no NEW 5xx caused by the attacks"        0   "$(( errors_after - errors_before ))"

echo
if [[ $fail -eq 0 ]]; then
  printf '\033[32mCHAOS RESULT: %d passed, 0 failed\033[0m\n' "$pass"
else
  printf '\033[31mCHAOS RESULT: %d passed, %d FAILED\033[0m\n' "$pass" "$fail"
fi
exit $(( fail > 0 ? 1 : 0 ))
