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
