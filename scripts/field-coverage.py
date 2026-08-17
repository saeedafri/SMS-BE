#!/usr/bin/env python3
"""Find contract fields the backend never actually fills.

The enum checker catches a field whose VALUE is wrong. This catches the quieter
failure: a field the contract declares, that we return as null or omit on every
single row, forever. Nothing else notices. The response validates, the page
renders, and one column is simply always empty.

Three real bugs of exactly this shape shipped before this script existed:

  * `SenderId.qualityRating` and `messagingTier` — declared in the contract,
    rendered by the senders list, and with no column in the database to hold
    them. Every WhatsApp sender reported null and the "Quality / tier" column
    was blank for everyone.
  * `SenderId.dnsRecords` — declared, never implemented, so an email sender
    finished onboarding with nothing to publish and the screen dead-ended.
  * `Registration.registrationId` — never assigned on approval, so an approved
    registration showed the label with an empty value beside it.

A null here is not automatically a bug: `rejectionReason` SHOULD be null on
everything that was not rejected, and an SMS sender genuinely has no WhatsApp
quality rating. So this script reports, it does not fail. Read the output and
judge each line — the question to ask is "is there any row in this fixture that
should have had a value here?"

Usage:
    python3 scripts/field-coverage.py                  # all endpoints
    python3 scripts/field-coverage.py sender-ids       # one, by path fragment
"""

import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("RELAY_API", "http://localhost:8080")
EMAIL = os.environ.get("RELAY_EMAIL", "founder@acme.test")
PASSWORD = os.environ.get("RELAY_PASSWORD", "relay-dev")
OPERATOR_EMAIL = os.environ.get("RELAY_OPS_EMAIL", "ops@relay.internal")
OPERATOR_PASSWORD = os.environ.get("RELAY_OPS_PASSWORD", "relay-ops-dev")
CONTRACT = os.environ.get(
    "RELAY_CONTRACT", "../SMS-UI/openapi.json"
)

# Only GETs that need no path parameter. A field on a detail-only schema is not
# reachable from a list endpoint, so those are listed separately below with an
# id pulled from the corresponding list.
LIST_ENDPOINTS = [
    "/v1/sender-ids",
    "/v1/templates",
    "/v1/campaigns",
    "/v1/automation/journeys",
    "/v1/contacts",
    "/v1/contact-lists",
    "/v1/suppressions",
    "/v1/conversations",
    "/v1/verify/services",
    "/v1/billing/invoices",
    "/v1/developer/api-keys",
    "/v1/developer/webhooks",
    "/v1/support/tickets",
    "/v1/registrations",
]

# The operator console signs in separately and its endpoints were invisible to
# this script for exactly as long as it took one of them to ship a bug: the
# rate card dropped every row's `category`, so the screen showed two "IN EMAIL"
# rows at different prices with nothing to tell them apart. Neither this script
# nor the enum checker could see it, because both only spoke to tenant
# endpoints. A checker's blind spot is where the next bug lives.
OPERATOR_ENDPOINTS = [
    "/v1/operator/tenants",
    "/v1/operator/routes",
    "/v1/operator/rates",
    "/v1/operator/approvals",
    "/v1/operator/abuse-queue",
    "/v1/operator/audit-log",
    "/v1/operator/support/tickets",
]


def request(path, token=None, body=None):
    """One HTTP call. Returns (status, parsed-json-or-None)."""
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method="POST" if data else "GET")
    if data:
        req.add_header("content-type", "application/json")
    if token:
        req.add_header("authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            return response.status, json.loads(response.read() or b"null")
    except urllib.error.HTTPError as error:
        # A 404 on an endpoint we have not built yet is information, not a
        # crash — report it alongside the field results.
        return error.code, None
    except urllib.error.URLError as error:
        print(f"cannot reach {BASE}: {error.reason}", file=sys.stderr)
        sys.exit(2)


def login():
    status, body = request(
        "/v1/auth/login", body={"email": EMAIL, "password": PASSWORD}
    )
    if status != 200 or not body:
        print(f"login failed ({status}) — is the API up and seeded?", file=sys.stderr)
        sys.exit(2)
    return body["session"]["token"]


def operator_login():
    """The operator console's own session. Its response shape differs from the
    tenant one — a bare token rather than a session object — because operators
    and tenants are deliberately separate identity systems."""
    status, body = request(
        "/v1/operator/login",
        body={"email": OPERATOR_EMAIL, "password": OPERATOR_PASSWORD},
    )
    if status != 200 or not body:
        print(f"operator login failed ({status})", file=sys.stderr)
        return None
    return body.get("token")


# The envelope keys this API uses to wrap a list. Shared by rows_of and
# declared_fields deliberately: if the two disagree about which key holds the
# rows, the script compares an envelope's field names against an item's fields
# and reports every one of them as missing — noise that buries the real finding.
ENVELOPE_KEYS = ("data", "items", "results", "tenants", "routes", "defaults",
                 "entries", "tickets", "conversations")


def rows_of(payload):
    """Unwrap whichever envelope this endpoint uses into a list of objects."""
    if isinstance(payload, list):
        return [r for r in payload if isinstance(r, dict)]
    if isinstance(payload, dict):
        for key in ENVELOPE_KEYS:
            if isinstance(payload.get(key), list):
                return [r for r in payload[key] if isinstance(r, dict)]
        return [payload]
    return []


def declared_fields(contract, path):
    """The property names the contract promises on this path's 200 response.

    Resolved one $ref deep, which is as far as this contract ever nests its
    response envelopes. A deeper chain returns nothing rather than guessing —
    silently reporting "no fields declared" is safer than reporting a wrong set.
    """

    def resolve(node):
        if isinstance(node, dict) and "$ref" in node:
            name = node["$ref"].rsplit("/", 1)[-1]
            return contract["components"]["schemas"].get(name, {})
        return node or {}

    operation = contract.get("paths", {}).get(path, {}).get("get")
    if not operation:
        return set()
    content = (
        operation.get("responses", {})
        .get("200", {})
        .get("content", {})
        .get("application/json", {})
    )
    schema = resolve(content.get("schema"))
    # Unwrap an array, then an envelope with a data/items array inside it.
    if schema.get("type") == "array":
        schema = resolve(schema.get("items"))
    else:
        for key in ENVELOPE_KEYS:
            prop = schema.get("properties", {}).get(key)
            if prop and prop.get("type") == "array":
                schema = resolve(prop.get("items"))
                break
    return set(schema.get("properties", {}))


def main():
    wanted = sys.argv[1] if len(sys.argv) > 1 else None
    with open(CONTRACT) as handle:
        contract = json.load(handle)

    token = login()
    operator_token = operator_login()
    always_empty = []
    unreachable = []
    checked = 0

    endpoints = [(p, token) for p in LIST_ENDPOINTS]
    if operator_token:
        endpoints += [(p, operator_token) for p in OPERATOR_ENDPOINTS]
    else:
        unreachable.append(("/v1/operator/*", "could not sign in as an operator"))

    for path, path_token in endpoints:
        if wanted and wanted not in path:
            continue
        status, payload = request(path, path_token)
        if status != 200:
            unreachable.append((path, status))
            continue
        rows = rows_of(payload)
        if not rows:
            unreachable.append((path, "200 but no rows — nothing to judge"))
            continue

        declared = declared_fields(contract, path)
        if not declared:
            unreachable.append((path, "no schema resolved from the contract"))
            continue

        checked += 1
        for field in sorted(declared):
            # Present with a real value on at least one row is all we ask. One
            # row proving the field can be filled is enough; requiring every row
            # would flag rejectionReason on a fixture with nothing rejected.
            filled = any(
                row.get(field) not in (None, "", [], {}) for row in rows
            )
            if not filled:
                always_empty.append((path, field, len(rows)))

    print(f"checked {checked} endpoints against {CONTRACT}\n")

    if always_empty:
        print(f"{len(always_empty)} field(s) empty on EVERY row — judge each:\n")
        width = max(len(p) for p, _, _ in always_empty)
        for path, field, count in always_empty:
            print(f"  {path.ljust(width)}  {field}   (all {count} rows)")
    else:
        print("every declared field is filled on at least one row.")

    if unreachable:
        print(f"\n{len(unreachable)} endpoint(s) not judged:\n")
        for path, why in unreachable:
            print(f"  {path}  — {why}")

    # Always exits 0. This script informs a human; a null is not proof of a bug
    # and a gate that cries wolf gets switched off.
    return 0


if __name__ == "__main__":
    sys.exit(main())
