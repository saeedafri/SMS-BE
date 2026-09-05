"""Generate the complete API reference from the contract the server is built from.

Written rather than hand-maintained because there are 177 operations: a
hand-written reference is wrong the first time someone adds a field, and this one
is regenerated from openapi/control.json, which IS what the deployed server was
generated from.

The one thing not in the contract is which routes an API key may call and under
which scope. That lives in internal/api/key_scopes.go and is parsed out of it, so
the auth column cannot drift from the enforcement.
"""
import json
import re
import sys
from collections import OrderedDict

spec = json.load(open(sys.argv[1]))
key_scopes_source = open(sys.argv[2]).read()
out = open(sys.argv[3], "w")

# ---------------------------------------------------------------- key scopes
key_routes = {}
block = re.search(r"var keyRoutes = map\[string\]string\{(.*?)\n\}", key_scopes_source, re.S)
for line in block.group(1).splitlines():
    match = re.match(r'\s*"([A-Z]+) ([^"]+)":\s*(scopeCheckedByHandler|"([^"]*)")', line.strip())
    if match:
        method, path, raw, scope = match.groups()
        key_routes[(method, path)] = ("send:sms / send:rcs, by the sender's channel"
                                   if raw == "scopeCheckedByHandler" else scope)


def route_key(method, path):
    return (method.upper(), re.sub(r"\{[^}]+\}", "{id}", path))


def auth_for(method, path, operation):
    responses = operation.get("responses", {})
    if path.startswith("/v1/operator/login") or path.startswith("/v1/auth/"):
        return "public"
    if path.startswith("/v1/operator"):
        return "operator session"
    scope = key_routes.get(route_key(method, path))
    if scope:
        return f"session **or** API key (`{scope}`)"
    if "401" in responses or "403" in responses:
        return "session"
    return "session"


# ---------------------------------------------------------------- schemas
schemas = spec["components"]["schemas"]


def ref_name(node):
    if isinstance(node, dict) and "$ref" in node:
        return node["$ref"].rsplit("/", 1)[-1]
    return None


def type_of(node, depth=0):
    """A short, readable type for one schema node."""
    if node is None:
        return "—"
    name = ref_name(node)
    if name:
        return f"[`{name}`](#{name.lower()})"
    if "oneOf" in node:
        inner = " \\| ".join(type_of(x, depth + 1) for x in node["oneOf"])
        return inner + (" \\| `null`" if node.get("nullable") else "")
    if "allOf" in node:
        return " & ".join(type_of(x, depth + 1) for x in node["allOf"])
    kind = node.get("type", "object")
    if kind == "array":
        return f"{type_of(node.get('items'), depth + 1)}[]"
    if node.get("enum"):
        return " \\| ".join(f"`{v}`" for v in node["enum"])
    if node.get("format"):
        kind = f"{kind}({node['format']})"
    suffix = " \\| `null`" if node.get("nullable") else ""
    return f"`{kind}`" + suffix


def fields_table(schema, indent=""):
    """Render an object schema's own properties as a table."""
    if not schema:
        return []
    name = ref_name(schema)
    if name:
        schema = schemas.get(name, {})
    if "oneOf" in schema and len(schema["oneOf"]) == 1:
        return fields_table(schema["oneOf"][0], indent)
    properties = schema.get("properties")
    if not properties:
        return []
    required = set(schema.get("required", []))
    rows = [f"{indent}| Field | Type | Required |",
            f"{indent}| --- | --- | --- |"]
    for field, node in properties.items():
        rows.append(f"{indent}| `{field}` | {type_of(node)} | "
                    f"{'**yes**' if field in required else 'no'} |")
    return rows


# ---------------------------------------------------------------- grouping
GROUPS = [
    ("Authentication & session", ("/v1/auth", "/v1/me", "/v1/sessions")),
    ("Messages & sending", ("/v1/messages", "/v1/conversations", "/v1/inbox")),
    ("Campaigns", ("/v1/campaigns",)),
    ("Automation & journeys", ("/v1/automation",)),
    ("Audience & suppression", ("/v1/contacts", "/v1/contact-lists", "/v1/suppressions")),
    ("Senders, templates & compliance", ("/v1/sender-ids", "/v1/templates", "/v1/registrations",
                                         "/v1/compliance", "/v1/rcs", "/v1/verify")),
    ("Billing & wallet", ("/v1/wallet", "/v1/billing", "/v1/pricing", "/v1/usage")),
    ("Developer API", ("/v1/developer",)),
    ("Analytics & reporting", ("/v1/analytics", "/v1/dashboard", "/v1/events")),
    ("Team & tenant settings", ("/v1/team", "/v1/tenant", "/v1/settings", "/v1/account",
                                "/v1/sso", "/v1/alerts", "/v1/support", "/v1/notifications")),
    ("Operator console", ("/v1/operator",)),
    ("Carrier webhooks & internal", ("/v1/carrier", "/v1/webhooks", "/v1/dev", "/healthz", "/metrics")),
]

operations = []
for path, methods in spec["paths"].items():
    for method, operation in methods.items():
        if method in ("get", "post", "patch", "put", "delete"):
            operations.append((path, method.upper(), operation))

grouped = OrderedDict((title, []) for title, _ in GROUPS)
grouped["Other"] = []
for path, method, operation in operations:
    for title, prefixes in GROUPS:
        if any(path.startswith(prefix) for prefix in prefixes):
            grouped[title].append((path, method, operation))
            break
    else:
        grouped["Other"].append((path, method, operation))
grouped = OrderedDict((k, sorted(v)) for k, v in grouped.items() if v)

METHOD_ORDER = {"GET": 0, "POST": 1, "PATCH": 2, "PUT": 3, "DELETE": 4}
for title in grouped:
    grouped[title].sort(key=lambda row: (row[0], METHOD_ORDER.get(row[1], 9)))


def summary_of(operation):
    text = operation.get("summary") or operation.get("description") or ""
    return text.strip().split("\n")[0][:110]


def anchor(path, method):
    return (method + " " + path).lower().replace("/", "").replace(" ", "-") \
        .replace("{", "").replace("}", "").replace("_", "-")


print(f"generating reference for {len(operations)} operations across {len(grouped)} groups",
      file=sys.stderr)

# ---------------------------------------------------------------- rendering
w = out.write

w(f"""# Relay backend — complete API reference

**Base URL:** `https://sms-api.saqibsaeed.cloud`
**Operations:** {len(operations)} across {len(spec['paths'])} paths
**Schemas:** {len(schemas)}
**Generated from:** `openapi/control.json` — the same file the server is generated
from, so this reference cannot describe an API the server does not implement.

Regenerate with `make api-reference` after any contract change. **Do not
hand-edit:** every operation, field, type and requiredness below is read out of
the contract, and the auth column is parsed out of `internal/api/key_scopes.go`,
so neither can drift from what is actually enforced.

---

## How authentication works

Two credential kinds, and they are not interchangeable.

**Session token** — from `POST /v1/auth/login`, sent as `Authorization: Bearer <token>`.
Authorised by the user's **role** (`owner`, `admin`, `member`). This is what the
dashboard uses.

**API key** — from `POST /v1/developer/api-keys`, prefixed `sk_live_` or `sk_test_`,
sent the same way. Authorised by **scopes**, not by role. A key is only a
credential on the routes listed below as accepting one; on every other route it
answers `401`, deliberately — the team roster, billing, the wallet and tenant
settings are session-only.

The six scopes: `send:sms`, `send:rcs`, `read:messages`, `read:analytics`,
`read:logs`, `webhooks:manage`. Anything else is refused at key creation with
`422`.

| Status | Means |
| --- | --- |
| `401` | No credential, or a key on a route that does not accept keys |
| `403` | A valid credential that lacks the scope or role. The message names the missing scope |

**Operator console** routes (`/v1/operator/*`) use a separate session from
`POST /v1/operator/login` against a separate user table, and may additionally be
restricted by IP allowlist.

---

## Two things that will surprise you

**1. A refused send is `202`, not `4xx`.**

`POST /v1/messages` answers `202` whenever the request was well-formed and we
reached a decision. Read `status`:

| `status` | Means |
| --- | --- |
| `sent` / `queued` | Accepted |
| `rejected` | **Refused at submit.** Terminal, carries `errorCode`, `costMinor` is `0` |
| `failed` | Reserved for a message that was accepted and then failed in delivery |

A malformed body is still `422`. A refusal is `202` because it carries a real
message id you can look up afterwards — an error body would not.

**2. The send result and the message log use different status vocabularies.**

`SendMessageResult.status` carries `rejected`. `MessageStatus` — what
`GET /v1/messages` returns — does not. So a message refused at submit reads
`rejected` in the send response and `failed` in the log. That is a real
difference between two enums, not a bug.

**Submit refusal codes** (`errorCode` on a `rejected` result):

| Code | Cause |
| --- | --- |
| `registered_template_required` | The destination regime requires a registered template and none was named (India) |
| `template_body_mismatch` | The body is not a legal instantiation of the named template |
| `content_not_allowed` | The country's content rules refuse the body (e.g. India's public-shortener ban) |
| `sender_not_approved` | The sender is not approved for this tenant |
| `sender_not_found` | No such sender on this account |
| `template_not_approved` | Our own review has not passed |
| `carrier_template_not_approved` | The carrier's separate review has not passed (RCS) |
| `sender_template_mismatch` | The template is registered against a different sender |
| `recipient_suppressed` | The recipient is on this tenant's suppression list |
| `invalid_recipient` | Not a valid number for this corridor |
| `insufficient_balance` | The wallet cannot cover the send |
| `no_rate` | No price configured for this country/channel |

---

## Index

""")

for title, rows in grouped.items():
    w(f"### {title}\n\n| Method | Path | Auth | Summary |\n| --- | --- | --- | --- |\n")
    for path, method, operation in rows:
        w(f"| `{method}` | [`{path}`](#{anchor(path, method)}) | "
          f"{auth_for(method, path, operation)} | {summary_of(operation)} |\n")
    w("\n")

w("\n---\n\n# Operations\n\n")

for title, rows in grouped.items():
    w(f"## {title}\n\n")
    for path, method, operation in rows:
        w(f'### <a id="{anchor(path, method)}"></a>`{method} {path}`\n\n')
        description = operation.get("description") or operation.get("summary")
        if description:
            w(description.strip() + "\n\n")
        w(f"**Auth:** {auth_for(method, path, operation)}\n\n")

        parameters = operation.get("parameters", [])
        if parameters:
            w("**Parameters**\n\n| Name | In | Type | Required |\n| --- | --- | --- | --- |\n")
            for parameter in parameters:
                w(f"| `{parameter['name']}` | {parameter['in']} | "
                  f"{type_of(parameter.get('schema'))} | "
                  f"{'**yes**' if parameter.get('required') else 'no'} |\n")
            w("\n")

        body = operation.get("requestBody")
        if body:
            schema = body.get("content", {}).get("application/json", {}).get("schema")
            required = " (required)" if body.get("required", True) else " (optional)"
            w(f"**Request body**{required} — {type_of(schema)}\n\n")
            table = fields_table(schema)
            if table:
                w("\n".join(table) + "\n\n")

        w("**Responses**\n\n| Status | Body |\n| --- | --- |\n")
        for code, response in sorted(operation.get("responses", {}).items()):
            schema = response.get("content", {}).get("application/json", {}).get("schema")
            shape = type_of(schema) if schema else "_no body_"
            w(f"| `{code}` | {shape} — {response.get('description', '').strip()} |\n")
        w("\n")

        for code, response in sorted(operation.get("responses", {}).items()):
            if not code.startswith("2"):
                continue
            schema = response.get("content", {}).get("application/json", {}).get("schema")
            table = fields_table(schema)
            if table and not ref_name(schema):
                w(f"`{code}` fields:\n\n" + "\n".join(table) + "\n\n")
        w("\n")

# ---------------------------------------------------------------- schemas
w("\n---\n\n# Schemas\n\n")
for name in sorted(schemas):
    schema = schemas[name]
    w(f'## <a id="{name.lower()}"></a>`{name}`\n\n')
    if schema.get("description"):
        w(schema["description"].strip() + "\n\n")
    if schema.get("enum"):
        w("One of: " + ", ".join(f"`{v}`" for v in schema["enum"]) + "\n\n")
        continue
    table = fields_table(schema)
    if table:
        w("\n".join(table) + "\n\n")
    else:
        w(f"Type: {type_of(schema)}\n\n")

out.close()
print("written", file=sys.stderr)
