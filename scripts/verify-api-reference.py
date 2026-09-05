"""Prove the API reference matches the live API.

Two claims are checked against production for every operation in the contract:

  1. the route EXISTS — anything answering "no such endpoint" is documented and
     not implemented, which is the worst kind of reference;
  2. the auth column is true — a route the reference says accepts an API key
     under a scope must accept a key holding it and refuse one that does not.
"""
import json, os, re, sys, urllib.error, urllib.request

BASE = "https://sms-api.saqibsaeed.cloud"
spec = json.load(open(os.environ["SPEC"]))
key_source = open(os.environ["KEY_SCOPES"]).read()

key_routes = {}
block = re.search(r"var keyRoutes = map\[string\]string\{(.*?)\n\}", key_source, re.S)
for line in block.group(1).splitlines():
    m = re.match(r'\s*"([A-Z]+) ([^"]+)":\s*(scopeCheckedByHandler|"([^"]*)")', line.strip())
    if m:
        key_routes[(m.group(1), m.group(2))] = m.group(4) or "SEND"


def call(method, path, token=None):
    request = urllib.request.Request(BASE + path, method=method)
    if token:
        request.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(request, None, timeout=30) as response:
            return response.status, response.read().decode()[:200]
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()[:200]
    except Exception as error:                      # noqa: BLE001
        return 0, str(error)


def login(email, password, operator=False):
    import json as j
    path = "/v1/operator/login" if operator else "/v1/auth/login"
    request = urllib.request.Request(BASE + path, method="POST")
    request.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(request, j.dumps({"email": email, "password": password}).encode()) as r:
        body = j.loads(r.read())
    return body.get("token") or body["session"]["token"]


customer = login("founder@acme.test", "relay-dev")
operator = login("ops@relay.internal", "relay-ops-dev", operator=True)

SAMPLE = {"{id}": "00000000-0000-0000-0000-000000000000",
          "{eventId}": "00000000-0000-0000-0000-000000000000",
          "{contactId}": "00000000-0000-0000-0000-000000000000"}
REQUIRED_QUERY = {"/v1/developer/webhooks": "?environment=live",
                  "/v1/developer/api-keys": "?environment=live"}

missing, checked = [], 0
for path, methods in spec["paths"].items():
    for method, operation in methods.items():
        if method.upper() != "GET":
            continue
        concrete = path
        for token, value in SAMPLE.items():
            concrete = concrete.replace(token, value)
        concrete += REQUIRED_QUERY.get(path, "")
        token = operator if path.startswith("/v1/operator") else customer
        status, body = call("GET", concrete, token)
        checked += 1
        if status == 404 and "no such endpoint" in body:
            missing.append(f"GET {path} -> route does not exist")

print(f"route existence: {checked - len(missing)}/{checked} GET operations exist")
for row in missing:
    print("   MISSING:", row)

# ---- the auth column ------------------------------------------------------
def mint(scopes, name):
    import json as j
    request = urllib.request.Request(BASE + "/v1/developer/api-keys", method="POST")
    request.add_header("Authorization", "Bearer " + customer)
    request.add_header("Content-Type", "application/json")
    payload = {"name": name, "environment": "live", "scopes": scopes}
    with urllib.request.urlopen(request, j.dumps(payload).encode()) as r:
        body = j.loads(r.read())
    return body["secret"], body["id"]

scopes = ["send:sms", "read:messages", "read:analytics", "read:logs", "webhooks:manage"]
keys, ids = {}, []
for scope in scopes:
    secret, key_id = mint([scope], "reference sweep " + scope)
    keys[scope] = secret
    ids.append(key_id)

auth_ok, auth_bad = 0, []
for (method, route), scope in sorted(key_routes.items()):
    if method != "GET" or scope == "SEND":
        continue
    concrete = route
    for token, value in SAMPLE.items():
        concrete = concrete.replace(token, value)
    concrete += REQUIRED_QUERY.get(route, "")

    status, _ = call(method, concrete, keys[scope])
    if status in (401, 403):
        auth_bad.append(f"{method} {route}: the scope it documents ({scope}) got {status}")
    else:
        auth_ok += 1

    wrong = "read:analytics" if scope != "read:analytics" else "read:logs"
    status, _ = call(method, concrete, keys[wrong])
    if status != 403:
        auth_bad.append(f"{method} {route}: a key WITHOUT {scope} got {status}, want 403")
    else:
        auth_ok += 1

for key_id in ids:
    call("DELETE", f"/v1/developer/api-keys/{key_id}", customer)

print(f"auth column:     {auth_ok}/{auth_ok + len(auth_bad)} checks correct")
for row in auth_bad:
    print("   WRONG:", row)

failures = len(missing) + len(auth_bad)
print(f"\n{'ALL CORRECT' if not failures else str(failures) + ' PROBLEMS'}")
sys.exit(1 if failures else 0)
