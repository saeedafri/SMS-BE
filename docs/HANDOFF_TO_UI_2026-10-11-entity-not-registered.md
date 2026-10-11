# New send refusal: `entity_not_registered` (11 Oct 2026)

The API now applies the same rule the campaign wizard's "Channel & country" grid already shows
(`src/lib/compliance/readiness.ts`, link 1): a tenant can only send to a country once **every
entity-tier registration** for that country is approved. In India that is the principal entity
(`pe_rtm_entity`) **and** the telemarketer mapping (`tm_mapping`); in the US, `tcr_brand` and
`tcr_campaign`. Countries with no regime, or a stub regime, are unaffected.

**Why:** the wizard showed India "In review — Approve your entity registration first" for a tenant
whose TM mapping was pending, while `POST /v1/messages` and `POST /v1/campaigns` from the same tenant
sent anyway. One rule, both doors now.

## What you will see

- A refused message: `status` `rejected`, `costMinor` 0, `errorCode` **`entity_not_registered`**,
  no `errorClass`. Same shape as every other submit-time refusal. On a campaign, each recipient is
  refused individually with this code.
- Order: after `tenant_suspended` and `invalid_recipient`, **before** `sender_not_approved`, the same
  order the readiness grid reports.

## Asked of you

1. Add `entity_not_registered` to `MessageRefusalCode` in `openapi/control.json`. Until it is there,
   the backend keeps it in `awaitingContract` (`internal/domain/messaging/gate.go`); tell us when it
   lands and we move it into the checked list.
2. A plain-English line for it, e.g. "Your registration for this country is not approved yet. Finish it
   under Compliance." with a link to `/compliance/<country>`.

## Who it affects on live today

Two active India tenants have the principal entity approved and **no** TM mapping:
**Concentrix services** and **Saqib222**. The wizard already blocked them; now their API sends are
refused too, until they submit the TM mapping and an operator approves it.
