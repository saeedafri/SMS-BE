#!/usr/bin/env bash
# End-to-end check: signs a real account up through the API, then drives the
# real Next.js UI with that session and asserts pages render with data that
# came from this backend.
#
# It asserts on CONTENT, not just status codes. A 200 proves a page rendered;
# only finding the organisation name we just created proves data actually
# flowed backend -> UI.
#
# Assumes the backend is on :8080 and the frontend on :3000, both already
# running. See docs/LOCAL_DEV.md.
set -uo pipefail

API=${API:-http://localhost:8080}
UI=${UI:-http://localhost:3000}
UI_DIR=${UI_DIR:-../SMS-UI}
COOKIE=relay_session

pass=0
fail=0

check() {
  local label=$1 condition=$2
  if [[ $condition == "true" ]]; then
    printf '  \033[32mPASS\033[0m  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n' "$label"
    fail=$((fail + 1))
  fi
}

echo "== preflight =="
health=$(curl -s --max-time 5 "$API/healthz" || echo '{}')
check "backend healthy" "$([[ $(echo "$health" | jq -r .status) == ok ]] && echo true || echo false)"
ui_code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$UI/login")
check "frontend serving" "$([[ $ui_code == 200 ]] && echo true || echo false)"

echo
echo "== signup through the API =="
stamp=$(date +%s)
email="e2e-${stamp}@example.test"
org="E2E Org ${stamp}"
signup=$(curl -s --max-time 10 -X POST "$API/v1/auth/signup" \
  -H 'content-type: application/json' \
  -d "{\"fullName\":\"E2E User\",\"email\":\"$email\",\"password\":\"e2e-password-123\",\"orgName\":\"$org\",\"country\":\"IN\"}")
token=$(echo "$signup" | jq -r '.token // empty')
check "signup returned a token" "$([[ -n $token ]] && echo true || echo false)"
if [[ -z $token ]]; then
  echo "  signup response: $signup"
  echo; echo "RESULT: $pass passed, $((fail + 1)) failed"; exit 1
fi

me=$(curl -s --max-time 10 "$API/v1/me" -H "authorization: Bearer $token")
check "GET /v1/me returns the new org" \
  "$([[ $(echo "$me" | jq -r .tenantName) == "$org" ]] && echo true || echo false)"
check "new user is an owner" \
  "$([[ $(echo "$me" | jq -r .role) == owner ]] && echo true || echo false)"

echo
echo "== login through the API =="
login=$(curl -s --max-time 10 -X POST "$API/v1/auth/login" \
  -H 'content-type: application/json' \
  -d "{\"email\":\"$email\",\"password\":\"e2e-password-123\"}")
check "login returns a session" \
  "$([[ $(echo "$login" | jq -r .kind) == session ]] && echo true || echo false)"

# Computed into a variable first: nesting this curl inside check's argument
# broke on the escaped quotes in the JSON body and silently reported a failure.
wrong_code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/auth/login" \
  -H 'content-type: application/json' \
  -d "{\"email\":\"$email\",\"password\":\"definitely-wrong\"}")
check "login rejects a wrong password (got $wrong_code)" \
  "$([[ $wrong_code == 401 ]] && echo true || echo false)"

echo
echo "== UI pages rendered with a real session =="
# /overview is the dashboard; / is a placeholder scaffold page.
for page in /overview /campaigns /billing /settings/profile /developer/api-keys /settings/team; do
  body=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI$page")
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -H "Cookie: $COOKIE=$token" "$UI$page")
  check "GET $page -> 200 (got $code)" "$([[ $code == 200 ]] && echo true || echo false)"
done

echo
echo "== data actually reached the UI =="
overview=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/overview")
check "organisation name from signup appears on /overview" \
  "$(grep -qF "$org" <<<"$overview" && echo true || echo false)"

profile=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/settings/profile")
check "signup email appears on /settings/profile" \
  "$(grep -qF "$email" <<<"$profile" && echo true || echo false)"

team=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/settings/team")
check "owner row from our database appears on /settings/team" \
  "$(grep -qF "$email" <<<"$team" && echo true || echo false)"

echo
echo "== capability endpoint the browser polls =="
me_via_ui=$(curl -s --max-time 20 -H "Cookie: $COOKIE=$token" "$UI/api/me")
check "UI /api/me proxies our backend with credentials" \
  "$([[ $(echo "$me_via_ui" | jq -r .tenantName) == "$org" ]] && echo true || echo false)"

echo
echo "== team management =="
invite=$(curl -s --max-time 10 -X POST "$API/v1/team/invite" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"email\":\"mate-${stamp}@example.test\",\"role\":\"member\"}")
check "invite creates a pending member" \
  "$([[ $(echo "$invite" | jq -r .status) == invited ]] && echo true || echo false)"
check "invited member has no name yet (contract: null)" \
  "$([[ $(echo "$invite" | jq -r .name) == null ]] && echo true || echo false)"
team_count=$(curl -s --max-time 10 "$API/v1/team" -H "authorization: Bearer $token" | jq '.members | length')
check "team now lists 2 members (got $team_count)" \
  "$([[ $team_count == 2 ]] && echo true || echo false)"

owner_id=$(echo "$me" | jq -r .userId)
demote=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X PATCH "$API/v1/team/$owner_id" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' -d '{"role":"member"}')
check "cannot demote the last owner (got $demote)" \
  "$([[ $demote == 422 ]] && echo true || echo false)"

echo
echo "== account security flows =="
change=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X PATCH "$API/v1/auth/password" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"currentPassword":"wrong-password","newPassword":"another-password"}')
check "changing a password needs the current one (got $change)" \
  "$([[ $change == 403 ]] && echo true || echo false)"

forgot_known=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/auth/password/forgot" \
  -H 'content-type: application/json' -d "{\"email\":\"$email\"}")
forgot_unknown=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/auth/password/forgot" \
  -H 'content-type: application/json' -d '{"email":"nobody-at-all@example.test"}')
check "password/forgot does not reveal account existence ($forgot_known vs $forgot_unknown)" \
  "$([[ $forgot_known == 204 && $forgot_unknown == 204 ]] && echo true || echo false)"

enroll=$(curl -s --max-time 10 -X POST "$API/v1/auth/mfa/enroll" -H "authorization: Bearer $token")
check "MFA enrolment returns a secret and a scannable QR" \
  "$([[ -n $(echo "$enroll" | jq -r '.secret // empty') && $(echo "$enroll" | jq -r .qrSvg) == *"<svg"* ]] && echo true || echo false)"
mfa_still_off=$(curl -s --max-time 10 "$API/v1/me" -H "authorization: Bearer $token" | jq -r .mfaEnabled)
check "MFA is not enabled until the code is confirmed" \
  "$([[ $mfa_still_off == false ]] && echo true || echo false)"

echo
echo "== compliance spine =="
sender=$(curl -s --max-time 10 -X POST "$API/v1/sender-ids" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"header":"E2EHDR","channel":"SMS","country":"IN"}')
sender_id=$(echo "$sender" | jq -r '.id // empty')
check "sender created, pending review" \
  "$([[ $(echo "$sender" | jq -r .status) == pending_review ]] && echo true || echo false)"

template=$(curl -s --max-time 10 -X POST "$API/v1/templates" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"name\":\"E2E template ${stamp}\",\"senderId\":\"$sender_id\",\"body\":\"Hi {{name}}, order {{order_id}}\"}")
template_vars=$(echo "$template" | jq -r '.variables | join(",")')
check "template parses its variables (got $template_vars)" \
  "$([[ $template_vars == "name,order_id" ]] && echo true || echo false)"

shortened=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/templates" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"name\":\"Shortened ${stamp}\",\"senderId\":\"$sender_id\",\"body\":\"Deal\",\"ctaUrl\":\"https://bit.ly/x\"}")
check "India rejects a shortened CTA URL (got $shortened)" \
  "$([[ $shortened == 422 ]] && echo true || echo false)"

registration=$(curl -s --max-time 10 -X POST "$API/v1/registrations" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"country":"IN","objectKey":"pe_rtm_entity","fields":{"legalName":"E2E Ltd","pan":"ABCDE1234F","entityType":"private_ltd","contactEmail":"c@e2e.test"}}')
check "DLT principal entity registered" \
  "$([[ $(echo "$registration" | jq -r .objectKey) == pe_rtm_entity ]] && echo true || echo false)"

campaign_early=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/registrations" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"country":"US","objectKey":"tcr_campaign","fields":{"useCase":"2fa","description":"d","sampleMessage":"s"}}')
check "US campaign blocked before its brand (got $campaign_early)" \
  "$([[ $campaign_early == 422 ]] && echo true || echo false)"

echo
echo "== compliance data reaches the UI =="
for page in /senders /templates /compliance; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -H "Cookie: $COOKIE=$token" "$UI$page")
  check "GET $page -> 200 (got $code)" "$([[ $code == 200 ]] && echo true || echo false)"
done
senders_page=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/senders")
check "the sender header we created renders on /senders" \
  "$(grep -qF "E2EHDR" <<<"$senders_page" && echo true || echo false)"

echo
echo "== money =="
card=$(curl -s --max-time 10 -X POST "$API/v1/wallet/payment-methods" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"brand":"visa","last4":"4242"}')
card_id=$(echo "$card" | jq -r '.id // empty')
check "first card becomes the default" \
  "$([[ $(echo "$card" | jq -r .isDefault) == true ]] && echo true || echo false)"

entry=$(curl -s --max-time 10 -X POST "$API/v1/wallet/topup" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"currency\":\"INR\",\"amountMinor\":250000,\"paymentMethodId\":\"$card_id\"}")
check "top-up credits the wallet" \
  "$([[ $(echo "$entry" | jq -r .balanceAfterMinor) == 250000 ]] && echo true || echo false)"
check "ledger amount stays positive (sign implied by type)" \
  "$([[ $(echo "$entry" | jq -r .amountMinor) == 250000 && $(echo "$entry" | jq -r .type) == topup ]] && echo true || echo false)"

balance=$(curl -s --max-time 10 "$API/v1/wallet/balances" -H "authorization: Bearer $token" | jq -r '.[0].balanceMinor')
check "balance reads back as $balance" \
  "$([[ $balance == 250000 ]] && echo true || echo false)"

overdraw=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API/v1/wallet/topup" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"currency\":\"INR\",\"amountMinor\":-5000,\"paymentMethodId\":\"$card_id\"}")
check "negative top-up rejected (got $overdraw)" \
  "$([[ $overdraw == 422 ]] && echo true || echo false)"

est=$(curl -s --max-time 10 -X POST "$API/v1/billing/estimate" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"country":"IN","channel":"SMS","recipientCount":1000,"primaryBody":"Your order has shipped."}')
check "estimate is 1000 x 1 segment x 12 paise = 12000" \
  "$([[ $(echo "$est" | jq -r .costMinorMin) == 12000 ]] && echo true || echo false)"

long_body=$(printf 'a%.0s' $(seq 1 161))
est2=$(curl -s --max-time 10 -X POST "$API/v1/billing/estimate" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"country\":\"IN\",\"channel\":\"SMS\",\"recipientCount\":1,\"primaryBody\":\"$long_body\"}")
check "161 characters bills as 2 segments" \
  "$([[ $(echo "$est2" | jq -r .segmentsPerMessage) == 2 ]] && echo true || echo false)"

echo
echo "== money reaches the UI =="
billing_page=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/billing")
check "the topped-up balance renders on /billing" \
  "$(grep -qE "2,500|2500" <<<"$billing_page" && echo true || echo false)"

echo
echo "== audience =="
list=$(curl -s --max-time 10 -X POST "$API/v1/contact-lists" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d "{\"name\":\"E2E list ${stamp}\"}")
list_id=$(echo "$list" | jq -r '.id // empty')
check "contact list created" "$([[ -n $list_id ]] && echo true || echo false)"

sup=$(curl -s --max-time 10 -X POST "$API/v1/suppressions" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -d '{"msisdns":["+919876543299"],"reason":"opted_out_keyword"}')
check "opt-out recorded" \
  "$([[ $(echo "$sup" | jq -r .created) == 1 ]] && echo true || echo false)"

imp=$(curl -s --max-time 20 -X POST "$API/v1/contacts/import" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -H "Idempotency-Key: e2e-${stamp}" \
  -d "{\"targetListId\":\"$list_id\",\"defaultCountry\":\"IN\",\"consentBasis\":{\"SMS\":\"opted_in\"},\"rows\":[{\"msisdn\":\"9876543210\",\"firstName\":\"Asha\",\"line\":2},{\"msisdn\":\"09876543211\",\"firstName\":\"Ravi\",\"line\":3},{\"msisdn\":\"9876543299\",\"line\":4},{\"msisdn\":\"junk\",\"line\":5}]}")
check "import created 2, skipped the suppressed one, flagged 1 invalid" \
  "$([[ $(echo "$imp" | jq -r '[.created,.skipped,.invalid]|join(",")') == "2,1,1" ]] && echo true || echo false)"
check "conflict reports the real CSV line" \
  "$([[ $(echo "$imp" | jq -r '.conflicts[]|select(.reason=="invalid_msisdn")|.line') == 5 ]] && echo true || echo false)"

replay=$(curl -s --max-time 20 -X POST "$API/v1/contacts/import" \
  -H "authorization: Bearer $token" -H 'content-type: application/json' \
  -H "Idempotency-Key: e2e-${stamp}" \
  -d "{\"targetListId\":\"$list_id\",\"defaultCountry\":\"IN\",\"consentBasis\":{\"SMS\":\"opted_in\"},\"rows\":[{\"msisdn\":\"9876543210\",\"line\":2}]}")
check "replayed import does not duplicate" \
  "$([[ $(echo "$replay" | jq -r .created) == 2 ]] && echo true || echo false)"

count=$(curl -s --max-time 10 "$API/v1/contact-lists/$list_id" -H "authorization: Bearer $token" | jq -r .contactCount)
check "list holds 2 contacts (got $count)" \
  "$([[ $count == 2 ]] && echo true || echo false)"

echo
echo "== audience reaches the UI =="
for page in /audience /audience/suppressions; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -H "Cookie: $COOKIE=$token" "$UI$page")
  check "GET $page -> 200 (got $code)" "$([[ $code == 200 ]] && echo true || echo false)"
done
aud=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI/audience")
check "the list we created renders on /audience" \
  "$(grep -qF "E2E list ${stamp}" <<<"$aud" && echo true || echo false)"

echo
echo "== every page renders, or fails for a known reason =="
# Status codes cannot answer this. Next.js renders its error boundary with
# HTTP 200, so a page whose data failed to load is indistinguishable from a
# working one by status alone. The boundary prints the underlying error, so
# that string is the only honest signal — and it is matched with a pattern
# that survives RSC splitting text across nodes.
#
# PENDING lists pages awaiting an endpoint from a later stage. They are
# expected to be broken; the check is that the list is EXACTLY right, so a
# page that breaks for a new reason fails the suite, and a page fixed by a
# new stage must be promoted out of this list.
PENDING="/analytics /automation /developer/verify /settings/data /settings/security /support"

pages=$(cd "$UI_DIR" && find "src/app/(dashboard)" -name page.tsx \
  | sed 's|src/app/(dashboard)||; s|/page.tsx||' | grep -v '\[' | sort)

for page in $pages; do
  [[ -z $page ]] && page=/
  html=$(curl -s --max-time 30 -H "Cookie: $COOKIE=$token" "$UI$page")
  failure=$(grep -o 'GET /v1/[^ "\\]* failed: [0-9]*' <<<"$html" | head -1)
  if [[ " $PENDING " == *" $page "* ]]; then
    check "$page is pending a later stage ($failure)" \
      "$([[ -n $failure ]] && echo true || echo false)"
  else
    check "$page renders with real data" \
      "$([[ -z $failure ]] && echo true || echo false)"
    [[ -n $failure ]] && echo "    $failure"
  fi
done

echo
echo "== authorisation is real =="
check "unauthenticated /v1/me is 401" \
  "$([[ $(curl -s -o /dev/null -w '%{http_code}' "$API/v1/me") == 401 ]] && echo true || echo false)"
logout_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/v1/auth/logout" \
  -H "authorization: Bearer $token")
check "logout returns 204" "$([[ $logout_code == 204 ]] && echo true || echo false)"
check "token is dead immediately after logout" \
  "$([[ $(curl -s -o /dev/null -w '%{http_code}' "$API/v1/me" \
       -H "authorization: Bearer $token") == 401 ]] && echo true || echo false)"

echo
if [[ $fail -eq 0 ]]; then
  printf '\033[32mRESULT: %d passed, 0 failed\033[0m\n' "$pass"
else
  printf '\033[31mRESULT: %d passed, %d failed\033[0m\n' "$pass" "$fail"
fi
exit $((fail > 0 ? 1 : 0))
