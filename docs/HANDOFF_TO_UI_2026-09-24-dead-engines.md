# Handoff — asks 9, 65, 66, 67, 68 (the dead-engines brief)

Answers `SMS-UI/docs/api-contract/BACKEND_BRIEF_2026-09-23_dead-engines.md`.
**Live since `56a2aa2`** (`/healthz` reports it; relay-api restarted 24 Sep 00:27 IST).
Nothing on the wire changed shape. Two field descriptions in `openapi.json` are now out of date;
see §6.

## 9 — `description` on Campaign and Journey: shipped

`campaigns.description` and `journeys.description` are nullable text. Both are set on create,
and a journey's can also be changed with PATCH. Empty and absent both store NULL, which reads
back as an absent field. A PATCH that leaves `description` out keeps it; `""` clears it.
Checked live: set, changed, kept across a rename, cleared.

## 65 — Journeys engine: shipped (read §65a before testing)

A `journeys` worker runs every 30s over **active** journeys only.

- **Enrolment.** `list_entry` enrols everyone on the list who isn't enrolled yet, on every
  cycle. That includes people already on the list at activation and anyone added later.
  `scheduled` enrols the list once, at `runAt`, and never again.
- **Send steps** use the campaign pipeline for a one-recipient page: same consent rule, gate,
  pricing, wallet hold and daily send ceiling. How each case is handled:
  - Suppressed on that step's channel: the contact **exits** and is counted in
    `exitedSuppressed`.
  - No opt-in or address on that channel, or a template slot the contact can't fill: the step
    is **skipped**. Nothing is sent or charged, and the contact moves on. This is the same
    non-event a campaign makes of them.
  - Daily ceiling spent: the contact waits on the step and retries an hour later.
- **Wait steps** hold the contact for `durationMinutes`. Polling more often doesn't shorten
  the wait.
- **Pause and archive** freeze the journey: no new enrolment and no advancement. A contact
  mid-wait stays mid-wait. On resume, everyone picks up where they were, so a wait that ran
  out during the pause ends on the first cycle after the resume.
- **Funnel.** The counts now come from real enrolments.
  - `totalEnrolled`: everyone who ever entered.
  - `completed` / `exitedSuppressed`: finished contacts.
  - `stepCounts[i]`: contacts on that step right now. That's normally 0 on a send step, which
    is passed through instantly, and non-zero on waits.
  - `Journey.completedCount` / `exitedSuppressedCount` mirror the funnel.

Verified live: a journey with a 2-contact list enrolled both within one cycle, and the funnel
read `totalEnrolled: 2` with both contacts on the wait step.

**65a — every journey that was `active` before this deploy is now `paused`.** Those journeys
were activated when activating did nothing. Left active, the first cycle would have enrolled
their whole lists and sent real messages that nobody decided to send today. `activatedAt` is
kept. Resuming now starts a journey for real. You may want copy on the journeys screen saying
so.

**Known gap:** journey messages are not yet tagged with `journey_id` in the message log, so
the by-journey usage breakdown stays empty. Tell us if that screen needs it soon.

## 66 — Per-tenant retention: shipped

- The ClickHouse TTL on `messages` is now 365 days, the largest setting.
- An hourly worker deletes each tenant's messages older than its `messageLogRetentionDays`
  (default 90).
- The setting is re-read every cycle and applies to what is already stored.

Existing data past the old 90-day TTL was already gone before this, so a tenant moving to 365
keeps from now on, not retroactively.

The hourly **rollups** that feed `/v1/analytics` summaries are counts, not messages. They are
not deleted. Deleting them would rewrite billing history. The message log and every
per-message view respect the setting. `message_events` stays at a flat 30 days, as you scoped
it.

## 67 — Durable webhook retries: shipped

A failed delivery writes a `webhook_retries` row, and a 15s worker makes the retry. The
schedule is unchanged: 1m, 5m, 30m. A restart inside the window still retries at about the
original time. A delivered retry ends `succeeded` and is never re-sent. An exhausted one ends
`abandoned` and is not left pending. Disabling the endpoint abandons its pending retries.

## 68 — Alerts and scheduled reports: shipped, pending 68a

**68a.1: is `RESEND_API_KEY` set on production?** Not confirmed yet. Reading the production
env was refused from our side, so Saeed has to check it by hand. Until it is confirmed, treat
both engines as not sending. Without the key they deliberately **do nothing at all**: no alert
is marked fired and no report is recorded as sent.

**68a.2: does this fit the mailer's "transactional only" scope?** Yes, and I agree with your
reading. It is operational mail to the tenant's own team: not metered, not priced, not held
against a wallet. Both engines call `s.Mail.Send`. No separate rate limit for now.

**Alerts** (worker every 5 min):
- Firing: each enabled rule with recipients emails **once when it enters breach**. It stays
  quiet while the breach lasts and re-arms when the metric recovers, so a second breach emails
  again. Switching a rule off re-arms it too.
- `deliveryFloor` uses the analytics delivery rate over its `range`. No traffic never
  breaches.
- `spendCeiling` / `volumeCeiling` use the trailing 30 days of delivered usage, the same
  numbers as the usage report.
- If no recipient could be emailed, the rule stays armed and tries again next cycle.
- **`lastFiredAt`:** it's stored (`alert_state.last_fired_at`) but not exposed. If you want
  it, declare `lastFiredAt: date-time, nullable` on each rule object in `AlertRules` (four
  places, one per low-balance currency) and we'll fill it.

**Scheduled reports** (worker every 5 min):
- `nextSendAt` and `recentSends` are now **stored**, no longer derived.
- A new report's first send is one period after creation. Existing reports were set to one
  period after this deploy, so nobody gets a backlog.
- The email carries sent, delivered and failed counts, the delivery rate and the delivered
  cost for the report's `range`.
- A bad recipient address doesn't stop the others. Only a send that reached at least one
  address is recorded.
- A paused report's `nextSendAt` is `null`, and its `recentSends` still shows its real
  history.
- A report resumed after being overdue sends on the next cycle, then once per period.

## 6 — Contract text to update (descriptions only, no shape change)

- `DataRetentionSettings.messageLogRetentionDays`: remove "A stored setting only — not
  enforced". It is enforced now.
- `ScheduledReport.recentSends`: remove "Derived at read time … not a stored history". It
  lists the real sends, newest first, up to 5.
