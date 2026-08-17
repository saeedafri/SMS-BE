# Analytics

Two databases answer two different kinds of question.

## Aggregates come from a rollup

"How many delivered, by day, for 30 days" reads `message_rollup_hourly` — a
SummingMergeTree pre-adding counts per hour, channel, country and status.

!!! danger "The counting bug this caused"
    The rollup stores one row per state **transition**. A message going
    queued → accepted → delivered contributes three rows. Summing them reported **8
    sent where 4 were sent**. The fix counts only attempt states, each of which happens
    at most once per message:

    ```sql
    sumIf(message_count, status IN ('accepted','submitted','rejected')) AS total
    ```

## Per-carrier breakdown comes from the message rows

The rollup has no carrier dimension. Rather than reshape the ingest path the send tests
already cover, deliverability reads message rows and groups by carrier.

Messages with no carrier recorded are **excluded**, not bucketed under a placeholder — a
table that exists to compare carriers must not attribute traffic to a carrier that may
not have carried it.

## Live figures, Acme Retail, 30 days

| Metric | Value |
|---|---:|
| Attempted | 2,070 |
| Delivered | 1,782 |
| Failed | 205 |
| Delivery rate | 86.1% |
| Cost | ₹294.03 |
| Latency p50 / p90 | 3,624 / 5,859 ms |
| Velocity-flagged | 41 |
| Carriers | JIO, AIRTEL |

Latency is **handset-confirmed** — `created_at` to `delivered_at` — not
time-to-carrier-accept. Accept latency is nearly always fast and nearly always
meaningless; what a customer feels is when the phone buzzes.

!!! danger "The bug class that cost the most time"
    The API returned `"Reliance Jio"` for the carrier. Perfectly readable, and wrong:
    `CarrierId` is a contract **enum** whose value is `JIO`. The frontend resolves it
    against a fixed registry, so it threw and the **entire analytics page rendered
    blank** — while the API returned 200 throughout.

    This shipped three times in one day in three different fields. There is now a
    guard: `scripts/enum-check.py` walks 23 endpoints and validates every enum-typed
    value against the contract. Go cannot catch this — its generated enums are string
    aliases.

## The quieter bug: a field that is never filled

The enum checker catches a value that is *wrong*. It cannot catch a field that is
simply always **empty** — declared in the contract, rendered by a screen, and null on
every row forever. Nothing fails. The response validates, the page renders, and one
column is blank for everybody.

`scripts/field-coverage.py` walks the list endpoints and reports any contract field
that is empty on **every** row. It reports rather than fails, because a null is often
correct — `rejectionReason` *should* be null on everything that was not rejected.
The question it makes you ask is: *is there any row here that should have had a value?*

Run against the seeded tenant it found four real gaps in one pass:

| Field | What was wrong |
|---|---|
| `SenderId.qualityRating` / `messagingTier` | Declared, rendered by the senders list, **no column existed** |
| `Contact.consentedAt` | Store loaded it, contract declared it, the API never copied it across |
| `ContactList.waSessionActive` | Never implemented — the WhatsApp 24h window stat had nothing to count |
| `Template.rcsContent` / `waContent` / `emailContent` | **Entirely unimplemented.** Every channel was stored as if it were SMS |

```bash
python3 scripts/field-coverage.py            # all endpoints
python3 scripts/field-coverage.py templates  # one, by path fragment
```

## Verify analytics reads real attempts

The one-time-code dashboards are computed from the `verifications` table, not
simulated: the funnel narrows by counting rows that reached each state, and the
success rate is verified ÷ requested.

!!! danger "Three zeros that looked like an all-clear"
    The fraud panel returned `velocity: 0, geoAnomaly: 0, blocked: 0` — hardcoded —
    while every verification row carried a real `fraud_flag` column that was simply
    never read. This is the worst possible failure for a fraud panel: three zeros are
    indistinguishable from *"we checked and found nothing"*, so a customer under
    attack sees an all-clear. They are now counted from the column.

!!! warning "A chart that redrew itself differently every refresh"
    The daily buckets were built in a Go `map` and returned in iteration order. Go
    **randomises** map iteration deliberately, so the same unchanged data produced a
    different zigzag on every page load. Now sorted by date.

## Two fields report honestly rather than plausibly

- **Cost per conversion** returns 0 — conversion tracking does not exist.
- **Journey funnel counts** are derived from real suppression data, not simulated.

Inventing either would silently redefine a metric someone makes budget decisions from.
