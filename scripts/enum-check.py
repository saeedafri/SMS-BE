#!/usr/bin/env python3
"""Checks every enum-typed value the API returns against the contract's enum.

This exists because the same bug shipped three times in one day: the backend
returned a value that was perfectly reasonable on its own — "Reliance Jio",
"jio", "good" — where the contract declares an enum ("JIO", "REGISTERED"…).
The API answered 200 every time. The frontend resolves these against fixed
registries, so an out-of-enum value does not degrade one field: it throws, and
the entire page renders blank.

Nothing caught it. Go's type system cannot, because the generated types are
string aliases. The contract test checks response SHAPE, not enum membership.
Only a browser noticed, and only by rendering nothing.

Usage: python3 scripts/enum-check.py [base_url]
"""
import json
import subprocess
import sys
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080"
SPEC = "../SMS-UI/openapi.json"

# Endpoints worth walking, with the auth each needs. Read-only: this runs
# against a live server and must never change what it is inspecting.
TENANT_PATHS = [
    "/v1/analytics?range=30d", "/v1/campaigns", "/v1/sender-ids", "/v1/templates",
    "/v1/messages?limit=20", "/v1/billing/usage", "/v1/billing/invoices",
    "/v1/automation/journeys", "/v1/conversations", "/v1/suppressions",
    "/v1/contact-lists", "/v1/support/tickets", "/v1/verify/services",
]
OPERATOR_PATHS = [
    "/v1/operator/tenants", "/v1/operator/routes", "/v1/operator/rates",
    "/v1/operator/approvals", "/v1/operator/abuse-queue", "/v1/operator/audit-log",
    "/v1/operator/usage", "/v1/operator/margin", "/v1/operator/user-activity",
    "/v1/operator/support/tickets",
]


def post(path, body):
    request = urllib.request.Request(
        BASE + path, data=json.dumps(body).encode(),
        headers={"content-type": "application/json"})
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def get(path, token):
    request = urllib.request.Request(
        BASE + path, headers={"authorization": "Bearer " + token})
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def enums_by_field(spec):
    """Maps a property name to the set of values the contract allows.

    Keyed by NAME rather than by schema path because the response bodies are
    walked without their schemas — a field called "carrier" means the same thing
    wherever it appears, which is exactly why a registry lookup on it is safe
    for the frontend and dangerous for us to get wrong.
    """
    allowed = {}

    def refs_of(schema):
        """Every schema name this property can resolve to."""
        if not isinstance(schema, dict):
            return []
        names = []
        if isinstance(schema.get("$ref"), str):
            names.append(schema["$ref"].rsplit("/", 1)[-1])
        for key in ("oneOf", "anyOf", "allOf"):
            for branch in schema.get(key, []) or []:
                names.extend(refs_of(branch))
        if isinstance(schema.get("items"), dict):
            names.extend(refs_of(schema["items"]))
        return names

    def walk(node):
        if isinstance(node, dict):
            for key, value in node.items():
                if key == "properties" and isinstance(value, dict):
                    for prop, schema in value.items():
                        if isinstance(schema, dict) and isinstance(schema.get("enum"), list):
                            allowed.setdefault(prop, set()).update(schema["enum"])
                        # Follow $ref, and the oneOf/anyOf branches a nullable
                        # enum compiles to. Missing those made a template's
                        # category look out-of-contract because only the ticket
                        # category enum had been collected under that name.
                        for branch in refs_of(schema):
                            target = spec["components"]["schemas"].get(branch, {})
                            if isinstance(target.get("enum"), list):
                                allowed.setdefault(prop, set()).update(target["enum"])
                walk(value)
        elif isinstance(node, list):
            for item in node:
                walk(item)
    walk(spec)
    return allowed


def check(body, allowed, path, problems):
    if isinstance(body, dict):
        for key, value in body.items():
            if isinstance(value, str) and key in allowed and value not in allowed[key]:
                problems.append(f"{path}: {key}={value!r} not in {sorted(allowed[key])}")
            check(value, allowed, path, problems)
    elif isinstance(body, list):
        for item in body:
            check(item, allowed, path, problems)


def main():
    spec = json.load(open(SPEC))
    allowed = enums_by_field(spec)

    tenant = post("/v1/auth/login",
                  {"email": "founder@acme.test", "password": "relay-dev"})["session"]["token"]
    operator = post("/v1/operator/login",
                    {"email": "ops@relay.internal", "password": "relay-ops-dev"})["token"]

    problems = []
    for path in TENANT_PATHS:
        try:
            check(get(path, tenant), allowed, path, problems)
        except Exception as error:  # a path that errors is a different problem
            problems.append(f"{path}: request failed: {error}")
    for path in OPERATOR_PATHS:
        try:
            check(get(path, operator), allowed, path, problems)
        except Exception as error:
            problems.append(f"{path}: request failed: {error}")

    if problems:
        print(f"\033[31mENUM CHECK: {len(problems)} out-of-contract values\033[0m")
        for problem in problems:
            print("  " + problem)
        return 1
    print(f"\033[32mENUM CHECK: every enum value across "
          f"{len(TENANT_PATHS) + len(OPERATOR_PATHS)} endpoints is in the contract\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
