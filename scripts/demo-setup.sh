#!/usr/bin/env bash
# Builds a complete, realistic tenant for a live demonstration.
#
# Everything below goes through the real API — no direct database writes except
# the two approvals an operator would perform, which are the operator console
# (not yet built). What you see in the UI afterwards is genuinely what the
# backend produced.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

API=${API:-http://localhost:8080}
say() { printf '\033[36m▸ %s\033[0m\n' "$1"; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }

stamp=$(date +%s)
EMAIL="demo-${stamp}@relay.test"
PASSWORD="demo-password-123"

say "Creating the demo account"
token=$(curl -s -X POST "$API/v1/auth/signup" -H 'content-type: application/json' \
  -d "{\"fullName\":\"Priya Sharma\",\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\",\"orgName\":\"Acme Retail\",\"country\":\"IN\"}" \
  | jq -r .token)
A="authorization: Bearer $token"
tenant=$(curl -s "$API/v1/me" -H "$A" | jq -r .tenantId)
ok "Acme Retail created"

say "Funding the wallet"
psql "$DATABASE_ADMIN_URL" -q -c \
  "INSERT INTO wallet_balances (tenant_id,currency,balance_minor) VALUES ('$tenant','INR',5000000)
   ON CONFLICT (tenant_id,currency) DO UPDATE SET balance_minor=5000000"
psql "$DATABASE_ADMIN_URL" -q -c \
  "INSERT INTO wallet_ledger (tenant_id,currency,entry_type,amount_minor,balance_after_minor,description)
   VALUES ('$tenant','INR','topup',5000000,5000000,'Opening balance')"
ok "₹50,000 available"

say "Registering the sender ID"
sender=$(curl -s -X POST "$API/v1/sender-ids" -H "$A" -H 'content-type: application/json' \
  -d '{"header":"ACMERT","channel":"SMS","country":"IN"}' | jq -r .id)
psql "$DATABASE_ADMIN_URL" -q -c "UPDATE sender_ids SET status='approved' WHERE id='$sender'"
ok "ACMERT approved (operator action)"

say "Creating templates"
shipped=$(curl -s -X POST "$API/v1/templates" -H "$A" -H 'content-type: application/json' \
  -d "{\"name\":\"Order shipped\",\"senderId\":\"$sender\",\"channel\":\"SMS\",\"country\":\"IN\",\"body\":\"Hi {{firstName}}, your Acme order has shipped and arrives tomorrow.\",\"category\":\"TRANSACTIONAL\"}" | jq -r .id)
welcome=$(curl -s -X POST "$API/v1/templates" -H "$A" -H 'content-type: application/json' \
  -d "{\"name\":\"Welcome\",\"senderId\":\"$sender\",\"channel\":\"SMS\",\"country\":\"IN\",\"body\":\"Welcome to Acme, {{firstName}}. Here is 10% off your first order.\",\"category\":\"PROMOTIONAL\"}" | jq -r .id)
psql "$DATABASE_ADMIN_URL" -q -c "UPDATE templates SET status='approved' WHERE tenant_id='$tenant'"
ok "2 templates approved"

say "Importing contacts"
list=$(curl -s -X POST "$API/v1/contact-lists" -H "$A" -H 'content-type: application/json' \
  -d '{"name":"October shoppers"}' | jq -r .id)
python3 - "$list" > /tmp/demo-contacts.json <<'PY'
import json,sys
names=["Asha","Ravi","Priya","Arjun","Meera","Vikram","Sneha","Rahul","Divya","Karan"]
rows=[]
for i in range(240):
    rows.append({"msisdn":"98765%05d"%i,"firstName":names[i%len(names)]})
# Deliberate outcomes for the demo: these suffixes drive the sandbox carrier.
rows += [
  {"msisdn":"9876500001","firstName":"Unreachable"},   # ABSENT_SUBSCRIBER
  {"msisdn":"9876500002","firstName":"DoNotDisturb"},  # DND_BLOCKED
  {"msisdn":"9876500000","firstName":"BadSender"},     # rejected at submit
]
print(json.dumps({"targetListId":sys.argv[1],"defaultCountry":"IN","consentBasis":{},"rows":rows}))
PY
imported=$(curl -s -X POST "$API/v1/contacts/import" -H "$A" -H 'content-type: application/json' \
  -d @/tmp/demo-contacts.json | jq -r .created)
ok "$imported contacts imported"

say "Recording an opt-out"
curl -s -X POST "$API/v1/suppressions" -H "$A" -H 'content-type: application/json' \
  -d '{"msisdns":["+919876500050"],"reason":"opted_out_keyword","note":"Replied STOP"}' -o /dev/null
ok "1 contact opted out — the gate will refuse them"

say "Launching a campaign"
campaign=$(curl -s -X POST "$API/v1/campaigns" -H "$A" -H 'content-type: application/json' \
  -d "{\"name\":\"October shipping updates\",\"channel\":\"SMS\",\"country\":\"IN\",\"listId\":\"$list\",\"senderId\":\"$sender\",\"templateId\":\"$shipped\"}" | jq -r .id)
ok "Campaign launched"

say "Creating an API key and a webhook"
curl -s -X POST "$API/v1/developer/api-keys" -H "$A" -H 'content-type: application/json' \
  -d '{"name":"Production","environment":"live","scopes":["messages.send","messages.read"]}' -o /dev/null
curl -s -X POST "$API/v1/developer/webhooks" -H "$A" -H 'content-type: application/json' \
  -d '{"url":"https://acme.example.com/relay-hooks","environment":"live","subscribedEvents":["message.delivered","message.failed"]}' -o /dev/null
ok "Developer surface ready"

say "Setting up an OTP service"
verify=$(curl -s -X POST "$API/v1/verify/services" -H "$A" -H 'content-type: application/json' \
  -d "{\"name\":\"Login OTP\",\"channels\":[{\"channel\":\"SMS\",\"senderId\":\"$sender\",\"body\":\"Your Acme code is {{code}}\"}],\"fallbackOrder\":[\"SMS\"],\"codeLength\":6,\"codeTtlSeconds\":300,\"maxAttempts\":3,\"rateLimit\":{\"maxPerPhone\":5,\"windowSeconds\":3600,\"cooldownSeconds\":60},\"regionAllowlist\":[]}" | jq -r .id)
ok "Verify service live"

say "Creating a welcome journey"
curl -s -X POST "$API/v1/automation/journeys" -H "$A" -H 'content-type: application/json' \
  -d "{\"name\":\"New customer welcome\",\"trigger\":{\"type\":\"list_entry\",\"listId\":\"$list\"},\"steps\":[{\"type\":\"send\",\"id\":\"s1\",\"channel\":\"SMS\",\"senderId\":\"$sender\",\"templateId\":\"$welcome\"},{\"type\":\"wait\",\"id\":\"w1\",\"durationMinutes\":2880},{\"type\":\"send\",\"id\":\"s2\",\"channel\":\"SMS\",\"senderId\":\"$sender\",\"templateId\":\"$shipped\"}]}" -o /dev/null
ok "Journey created"

say "Opening a support ticket"
curl -s -X POST "$API/v1/support/tickets" -H "$A" -H 'content-type: application/json' \
  -d '{"subject":"Delivery rates in Karnataka","category":"technical","body":"We are seeing lower delivery in one circle. Can you check the route?"}' -o /dev/null
ok "Ticket open"

echo
say "Waiting for delivery reports to settle"
sleep 8

counts=$(curl -s "$API/v1/campaigns/$campaign" -H "$A" | jq -c '.counts')
balance=$(curl -s "$API/v1/wallet/balances" -H "$A" | jq -r '.[0].balanceMinor')
analytics=$(curl -s "$API/v1/analytics?range=30d" -H "$A" | jq -c '{sent:.summary.sent,delivered:.summary.delivered,failed:.summary.failed,rate:.summary.deliveryRate,cost:.summary.costMinor}')

echo
printf '\033[1m═══ DEMO READY ═══\033[0m\n\n'
printf '  Sign in at  \033[36mhttp://localhost:3000/login\033[0m\n'
printf '  Email       \033[1m%s\033[0m\n' "$EMAIL"
printf '  Password    \033[1m%s\033[0m\n\n' "$PASSWORD"
printf '  Campaign    %s\n' "$counts"
printf '  Analytics   %s\n' "$analytics"
printf '  Balance     %s paise (started 5000000)\n\n' "$balance"
printf '  Live health   %s/healthz\n' "$API"
printf '  Live metrics  %s/metrics\n' "$API"
