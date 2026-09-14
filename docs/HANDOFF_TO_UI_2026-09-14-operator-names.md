# Handoff to UI — operator names are free text (14 Sep 2026)

## Why

Our first direct SMS operator is **Videocon**. The contract's `CarrierId` is a
closed enum (`JIO, AIRTEL, VI, BSNL, VERIZON, ATT, TMOBILE, EE, O2, VODAFONE_UK,
THREE, ETISALAT, DU`), so the console cannot create a Videocon connection or
route. A new operator contract must never need a release.

## Backend (done, not yet deployed)

`carrier` on connections (create, update) and routes (create) is no longer
checked against a list. It must match:

```
^[A-Z][A-Z0-9_]{1,19}$
```

2–20 characters, uppercase letters, digits and `_`, starting with a letter.
Anything else is `422 validation_failed` with the message:

> carrier must be 2-20 characters of A-Z, 0-9 and _, starting with a letter (for example VIDEOCON).

The name is matched exactly against routes and delivery receipts, which is why
case is fixed rather than normalised.

Tests: `internal/api/operator_names_test.go` —
`TestAnyWellFormedOperatorNameCanBeConfigured` (red on main: VIDEOCON → 422),
`TestAMalformedOperatorNameIsRefused` (mutation: loosening the pattern to
`^.{1,40}$` turns it red).

## What to change in the contract

`components.schemas.CarrierId`:

```json
"CarrierId": {
  "type": "string",
  "pattern": "^[A-Z][A-Z0-9_]{1,19}$",
  "description": "The operator's name as configured on its connection, e.g. AIRTEL, JIO, VIDEOCON. Free text: any operator the platform has a contract with."
}
```

Every schema that references `CarrierId` keeps the reference:
`Connection`, `ConnectionCreate`, `ConnectionUpdate`, `Route`, `RouteCreate`,
`RcsCarrierLaunch`, `RcsVendor`, `AnalyticsDeliverabilityRow`.

**Not** part of this change: RCS carriers. RCS launch and template registration
only work for gateways the backend has code for (Airtel IQ, Vi, Google), and
still answer 503 for any other name.

## What to change in the console

1. Connection create/edit and route create: the operator field becomes a text
   input (uppercase as the user types, validate the pattern client-side, show
   the message above on 422). Suggest existing names from `GET
   /v1/operator/connections` so AIRTEL is not typed as AIRTEL_.
2. Filters and labels that switch on the enum (`CarrierId` unions in
   `api-types.ts`, mock states) must render an unknown name as itself rather
   than falling through to a blank.
3. Regenerate types after the contract change.

## Order

Contract change → UI → tell us the commit; we run `make generate` and deploy.
