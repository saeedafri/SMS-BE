# Relay as-built (16 Oct 2026)

The definitive description of what Relay is **today**, taken from the code rather than the docs.
Every claim about Relay cites `file:line`. Paths without a prefix are in `SMS-BE`; frontend paths
are prefixed `SMS-UI/`. Where a doc disagrees with code, the code wins and the disagreement is
named. Things that could not be proven from the repositories (live carrier traffic, production
env values) are marked **UNVERIFIED**.

Snapshot: `SMS-BE` at `4417d70` (main, clean); `SMS-UI` at `4ba88297` (fast-forwarded from
`upstream/master` before writing). Stale docs to ignore: `STATUS.md` ("Last measured: 2026-08-17",
`STATUS.md:3`) and `docs/gaps.md` (last touched 2026-08-17).

Method: read `cmd/control-api/main.go` end to end, the send pipeline
(`internal/sending/service.go`), the gate (`internal/domain/messaging/gate.go`), the state
machine, every connector header, the compliance regimes, billing, auth; computed the data model
from the Up sections of all 78 migrations; computed the API surface by diffing
`openapi/control.json` against the generated Go interface and the hand-mounted chi routes;
computed UI coverage by diffing every `/v1/...` literal in `SMS-UI/src` (non-test, non-mock)
against the spec.

---

## 0. The ten things that matter most

1. **Only two channels can reach a real handset: SMS (SMPP) and RCS.** The connector registry is
   built as `Registry{Default: SMPPRouter{Fallback: sandbox}}` with only `RCS` given a dedicated
   gateway (`cmd/control-api/main.go:272-273`, `:276-286`). `SMPPRouter.Submit` sends anything
   whose channel is not `SMS` to the sandbox (`internal/connector/smpp.go:802`). WhatsApp, Email
   and Voice therefore go to an in-process fake carrier.
2. **Those sandbox sends are charged.** The sandbox marks every number not ending in
   000/001/002/003 as delivered (`internal/connector/sandbox.go:18-27`), and `delivered` keeps the
   hold as a charge (`internal/domain/messaging/state.go:84-89`). No gate refuses WHATSAPP, EMAIL
   or VOICE (no channel check anywhere in `gate.go:168-258`). A tenant who sends a WhatsApp
   campaign today pays for messages nobody received.
3. **Every promotional SMS to India is refused** `dnd_check_unavailable`: production wires
   `NoDNDRegister{}` (`cmd/control-api/main.go:347`), whose every lookup fails
   (`internal/sending/dnd.go:38-43`), and the gate refuses on lookup failure
   (`internal/domain/messaging/gate.go:251-252`). Transactional/service SMS are unaffected.
4. **Operator roles are not enforced on anything.** `requireOperatorAdmin` exists
   (`internal/api/operator.go:76-85`) but has **zero call sites** outside one unit test
   (`internal/api/send_cap_gate_internal_test.go:32-49`). Crediting a wallet
   (`internal/api/postpaid.go:210`), recording a payment (`:624`), setting a credit limit
   (`:779`), editing default rates (`internal/api/operator.go:1203`) and overrides (`:1297`) all
   call plain `requireOperator`.
5. **Pay-on-delivery billing.** Money is held at send (`internal/sending/service.go:415-427`),
   kept only on `delivered`, released on `undelivered`, `rejected`, `carrier_rejected`, `expired`
   (`state.go:84-89`). Unreported messages are expired and refunded after 48 h
   (`internal/sending/reconciler.go:17`). This is customer-friendly but the opposite of how Indian
   operators bill (on submit) — margin risk.
6. **No payment gateway.** `Server.Gateway` is never set in production (absent from the struct
   literal at `cmd/control-api/main.go:329-359`), so `POST /v1/wallet/topup` always answers 422
   "Card top-up is not available" (`internal/api/wallet.go:139-143`). Payment methods are a
   brand + last4 row (`db/migrations/00009_wallet.sql:53`). **Auto-recharge is config only**:
   `auto_recharge_configs` is read and written by `internal/api/wallet.go:319-360` and nothing
   ever executes a recharge (no other reference in `internal/`).
7. **Nine backend features have no screen at all.** Chatbots, short links, frequency caps, drip,
   test send, S3 report export, white-label branding, CRM webhook formats and error/latency
   analytics are mounted outside the contract (`internal/api/server.go:389-420`) and **zero**
   non-test, non-mock files in `SMS-UI/src` reference any of them (grep, §9.4).
8. **The live RCS vendor cannot do pre-send fallback.** Trustsignal has no capability API
   (`internal/connector/rcs_trustsignal.go:62-68`), and an unknown answer means "reachable"
   (`internal/sending/legs.go:211-215`), so RCS→SMS fallback on the live vendor only happens
   **after** a definite `undelivered` report (`internal/sending/fallback_after_failure.go:1-17`).
9. **Correction to the shared context:** RCS suggestions are **not** dropped at template
   registration on Trustsignal — they are sent as `suggestions` for both text and card templates
   (`internal/connector/rcs_trustsignal.go:239-258`, `:277`, `:286`, since commit `3128554`,
   1 Oct). They are dropped only for Airtel (text-only, card refused,
   `internal/connector/rcs_template.go:186-190`) and are moot for Jio/Vi (no template API,
   `rcs_jio.go:87-93`, `rcs_vi.go:279-288`). Carousels are not modelled anywhere
   (`rcs_template.go:37-39`).
10. **The CI deploy gate tests almost nothing that touches data.** GitHub Actions runs
    `go test ./...` with no datastores (`.github/workflows/deploy.yml:44-52`), so at least **369
    of 905** backend tests skip (§11). The real suite runs only when a developer runs
    `make test` (`Makefile:24-25`, `scripts/remote-test.sh`).

---

## 1. Architecture

### 1.1 Processes (`cmd/*`)

| Binary | Lines | What it is | Deployed? |
|---|---|---|---|
| `cmd/control-api` | 460 | The whole product: HTTP API, every background worker, SMPP binds, RCS gateways, send coalescer. One process. | Yes — built by `.github/workflows/deploy.yml:50-53`, run by systemd `deploy/relay-api.service:18` |
| `cmd/operator-admin` | 711 + 388 | Operator CLI: `list`, `create`, `set-password`, `enable`/`disable` operators; `bans`/`unban` (abuse guard); `rcs-launch`; `rcs-connection` (add/edit RCS vendor accounts with sealed secrets); `credit-wallet`; `send-cap`; `send-share`; `always-send`; `withheld` (`cmd/operator-admin/main.go:110-195`) | Built and shipped (`deploy.yml:52`) |
| `cmd/seed-demo` | 49 | Destructive demo seeder (wipes tenants/routes first, per memory note) | Shipped, never run by deploy |
| `cmd/smpp-uat-probe` | 75 | One-shot SMPP bind/submit probe for operator UAT | Not shipped |
| `cmd/gen-unimplemented` | 169 | Codegen: writes 501 stubs for every contract op (`cmd/gen-unimplemented/main.go:1-14`) | Dev tool |

There is **no separate worker process, no message broker, no queue service**. Everything below
runs as goroutines inside `control-api`, each wrapped in `resilience.Supervise` (panic-recovering
ticker loop, `internal/platform/resilience/resilience.go`).

Notable: **RCS vendor connections cannot be configured from the operator console.** They are
created with `operator-admin rcs-connection` (`cmd/operator-admin/main.go:165-170`,
`cmd/operator-admin/rcs_connections.go`), and RCS agent carrier launches are confirmed with
`operator-admin rcs-launch` (`main.go:160-164`). The console's Connections screen handles SMPP
only (no `rcs` reference in `SMS-UI/src/app/(admin)/admin/connections`).

### 1.2 Datastores

| Store | Role | Wiring |
|---|---|---|
| Postgres 16 (`deploy/aws/docker-compose.yml:12-13`) | Control plane: tenants, users, money ledger, compliance registry, campaigns, routes, connections. RLS on 55 tables (§2). | Tenant pool `store.Open` with startup retry (`cmd/control-api/main.go:76-85`); separate **operator pool** with cross-tenant visibility (`:122-127`); admin (migration-role) pool opened **only** when dev endpoints are on (`:109-117`) |
| ClickHouse 24.8 (`docker-compose.yml:40-41`, limited to 2 GB / 1.5 CPU at `:59`) | Data plane: every message row, every state transition, hourly rollups (§2.3) | Self-healing handle `store.NewClickHousePool` (`main.go:103`) — reconnects per tick rather than once at boot |
| Redis 7 (`docker-compose.yml:70-71`, 192 MB / 0.5 CPU at `:79`) | Rate limiting only: send limiter, login backoff, abuse guard bans | `store.OpenRedis`; boot **continues without it** and limits fail open (`main.go:87-96`, `internal/api/send_ratelimit.go:41-49`) |
| Local disk (`MEDIA_ROOT`) | RCS brand artwork and verification documents | `mediastore.New`, only when root + signing key + base URL are all set (`main.go:221-228`) |
| In-process `HotCache` (2 s TTL) | Sender, tenant status, pricing rate, entity-registration lookups | `main.go:327`; wallet balance and suppression deliberately uncached (`main.go:313-326`) |

All three datastores run as containers on the same EC2 box as the API, on loopback
(`Makefile:63-66` comment; `deploy/aws/docker-compose.yml`).

### 1.3 Background loops (all in `cmd/control-api/main.go`)

| Loop | Interval | Line | What it does |
|---|---|---|---|
| delivery-drainer | 2 s | `:146` | Applies the **sandbox** carrier's queued delivery reports (`internal/sending/drain.go:31`). Only exists because the sandbox cannot POST to us. |
| reconciler | 15 min | `:170`, `:179` | Expires messages accepted but unreported for 48 h (`internal/sending/reconciler.go:17`) through the normal settle path → refund; then lands campaigns stuck at `sending` > 15 min (`:192`, `internal/sending/campaign_reconcile.go:23`) |
| smpp-binds | 1 min | `:387` | Reloads operator SMPP binds from the `connections` table; first reload is synchronous before serving (`:382`) so no SMS leaks to the sandbox after a deploy |
| rcs-connections | 1 min | `:394` | Reloads RCS vendor accounts from `rcs_connections` |
| campaign-scheduler | 30 s | `:400` | `LaunchDueCampaigns` (`internal/api/campaign_scheduler.go:19`) — starts scheduled campaigns, honours `held_until` |
| journeys | 30 s | `:407` | `RunJourneys` (`internal/api/journey_engine.go`) — enrols trigger lists, advances send/wait steps |
| webhook-retries | 15 s | `:413` | `RetryDueWebhooks` (`internal/api/webhook_emit.go:85`); delays 1 m, 5 m, 30 m (`webhook_emit.go:66`) |
| message-retention | 1 h | `:418` | Deletes each tenant's ClickHouse rows beyond its 30/90/180/365-day setting |
| alerts | 5 min | `:424` | `EvaluateAlerts` — emails on rule breach; no-op without `RESEND_API_KEY` (`:421-423`) |
| scheduled-reports | 5 min | `:427` | `SendDueReports` — emails due reports |
| invoicing | 1 h | `:433` | `IssueMonthlyInvoices` — last month's usage invoices early in the new month |
| send coalescer | continuous | `:366` | Batches concurrent transactional sends: one suppression query, one wallet move per tenant, one carrier call, one ClickHouse write per batch (`internal/sending/coalesce.go`) |

There is no distributed lock on any of these. With one process this is fine; a second instance
would double-run the scheduler, journeys and invoicing (the retry worker alone uses a lease,
`webhook_emit.go:75-77`, `:99`). Horizontal scale is therefore **not** available without work.

### 1.4 Deploy topology

```
 GitHub push to main
        │  .github/workflows/deploy.yml (concurrency group deploy-aws, no cancel)
        ▼
 ubuntu runner: go vet ‖ go test ./... (datastore tests SKIP) ‖ go build control-api, seed-demo, operator-admin
        │ scp
        ▼
 EC2 relay-prod, ap-south-1, 2 vCPU  (memory: aws-server.md; UNVERIFIED instance type)
 ┌──────────────────────────────────────────────────────────────────────────┐
 │ nginx (deploy/aws/nginx, JSON access log → CloudWatch)  + fail2ban        │
 │   └─► relay-api.service  /opt/relay/control-api  MemoryMax=512M, Restart=always │
 │          ├─ Postgres 16 container (loopback)                              │
 │          ├─ ClickHouse 24.8 container (2 GB, 1.5 CPU)                     │
 │          └─ Redis 7 container (192 MB, 0.5 CPU)                           │
 │ relay-backup.timer 02:30 daily → pg_dump + ClickHouse native → S3         │
 └──────────────────────────────────────────────────────────────────────────┘
        ▲                                   ▲
   SMS-UI on Vercel (BFF)            Operators' SMPP (VIDEOCON), Trustsignal HTTPS
```

Deploy steps (`deploy.yml:101-185`): goose migrate with the admin URL, ClickHouse migrate
(`scripts/clickhouse_migrate.py`), `systemctl restart relay-api`, poll
`http://127.0.0.1:8080/healthz` for `"status":"ok"`, **roll back the binary** on failure, then
probe `https://sms-api.saqibsaeed.cloud/healthz`. Migrations are not rolled back with the binary.
Backups: `deploy/relay-backup.sh:30` (pg_dump), `:52-53` (ClickHouse), `:46` (S3 upload), timer
`deploy/relay-backup.timer` `OnCalendar=*-*-* 02:30:00` — RPO is up to 24 h.

The API process is capped at **512 MB** (`deploy/relay-api.service:39`).

### 1.5 Throughput

Measured on 4 Sep 2026 (`docs/PERFORMANCE_live-send-throughput-2026-09-04.md`), **on the
retired Hostinger VPS, not on AWS** — no AWS re-measurement exists in the repo:

| Concurrency | Before coalescer | After coalescer |
|---|---|---|
| 4 | 15.6/s, p50 208 ms | 15.6/s, p50 260 ms |
| 24 | 26.9/s | 50.6/s, p50 410 ms |
| 64 | 34.6/s | 104–132/s |
| 128–256 | — | 188–195/s (per-tenant limiter engaged) |

(`PERFORMANCE_…md:203-207`.) The doc tells sales to quote "190/second per tenant"
(`:268`). Limits in code: `liveBurst = 200`, `testBurst = 20` per tenant per second
(`internal/api/send_ratelimit.go:22-24`), fixed window, fails open without Redis (`:41-49`).
`GET /v1/developer/rate-limit` advertises `RequestsPerSecond: 100, Burst: 200`
(`internal/api/developer.go:207`). Per-SMPP-bind throughput is `max_tps` / `window_size` from the
`connections` row (`internal/connector/smpp.go:186-206`), default 1 TPS and window 1 when unset.

Practical ceiling: one 2-vCPU box shared by API + Postgres + ClickHouse. The doc names ClickHouse
inserts as the first bottleneck (`PERFORMANCE_…md:257`). A "2–3 crore/day" ambition is quoted
in `db/clickhouse/001_messages.sql:3-4`; ~190/s sustained is ~1.6 crore/day only if sustained
24 h, and that is one tenant at the limiter.

### 1.6 Observability

- Structured JSON logs via `slog` (`internal/platform/telemetry/logger.go`); nginx JSON access
  log for CloudWatch (`deploy/aws/nginx/conf.d-00-relay-logformat.conf:1`).
- In-process metrics at `GET /metrics` — counters, timings, recent incidents, slow requests
  > 250 ms — **deliberately no Prometheus** (`internal/api/observability.go:1-25`), and the
  endpoint is **unauthenticated** (`internal/api/server.go:370-373`).
- `/healthz` (dependency check, 503 when down) and `/readyz` (`server.go:366-369`).
- Worker panics recorded as incidents (`main.go:147` and every `Supervise` call).
- No tracing (OpenTelemetry appears only as an indirect dependency, `go.mod:43-44`), no
  alerting on the platform itself in code — CloudWatch configuration is outside the repo
  (UNVERIFIED).

### 1.7 Architecture gaps

- Single process, single box, no HA, no queue: a restart drops in-flight coalescer batches only
  after draining (`main.go:452-458`), but a box failure stops all sending.
- Background loops assume one instance (§1.3).
- SMPP binds are plain TCP, no TLS option (`internal/connector/smpp.go:303-306`).
- Sandbox is the default carrier for three of five channels (§0.1).
- Generated API code is stale vs the contract (§3.1).

---

## 2. Data model

Two stores. **Postgres** holds every control-plane row. **ClickHouse** holds every message and
every message state transition. There is **no `messages` table in Postgres** — none of the 78
migrations creates one; message rows live only in `db/clickhouse/001_messages.sql:18`. The
Postgres footprint of a message is its wallet hold in `wallet_ledger` and, for campaigns, the
campaign counters.

Migrations are goose files applied by `make migrate-up` (`Makefile:68-69`) and by the deploy
workflow. ClickHouse DDL is idempotent `IF NOT EXISTS` (`db/clickhouse/001_messages.sql:7-8`).

### 2.1 Totals (computed from the Up sections of all 78 files)

| Measure | Count | Note |
|---|---|---|
| Postgres migrations | 78 (`00001`–`00078`) | |
| Tables created | **70** | distinct `CREATE TABLE` names |
| Tables with `ENABLE ROW LEVEL SECURITY` | **55** | matches the shared context |
| …also `FORCE ROW LEVEL SECURITY` | **49** | |
| RLS enabled but not forced | 6: `link_clicks`, `media_assets`, `rcs_agent_carrier_launches`, `rcs_agents`, `sender_dns_records`, `short_links` | Only matters if the API role owns them; production API does not hold the migration role (`main.go:106-108`), so low risk |
| Tables with no RLS | 15: `users`, `email_verifications`, `password_resets`, `mfa_challenges`, `mfa_recovery_codes`, `operator_users`, `operator_sessions`, `operator_mfa_challenges`, `operator_recovery_codes`, `operator_audit_log`, `routes`, `rate_overrides`, `pricing_rates`, `connections`, `rcs_connections` | Global/operator tables, except `rate_overrides`, which **is** tenant-keyed (`00018_operator.sql:56`) and relies on a `tenant_id` predicate in code (`internal/store/wallet.go:580-596`) |
| ClickHouse DDL files / tables | 6 / 3 | `messages`, `message_events`, `message_rollup_hourly` |

The tenant boundary is one function, `current_tenant_id()` (`00002_current_tenant_id.sql:18`).
Operator cross-tenant reads go through `acting_as_operator()` (`00019_operator_visibility.sql:23`),
opened table by table in `00032`, `00033`, `00035`, `00046`, `00048`, `00061`, `00076`.

### 2.2 Postgres migrations by domain

Line numbers are the `CREATE TABLE` / `ALTER TABLE` line.

#### Tenancy, identity, sessions, roles

| Migration | Introduces |
|---|---|
| `00001_tenants.sql` | `tenants` (L4: id, name, status); RLS (L20-21) |
| `00002_current_tenant_id.sql` | `current_tenant_id()` (L18) |
| `00003_identity.sql` | `users` (L13: email, password_hash, email_verified, mfa_secret, mfa_enabled); `tenant_users` (L25: role, status, invited_at); `sessions` (L43: token_hash, device, browser, location, ip_address, last_active_at, expires_at, revoked_at); `email_verifications` (L60); `password_resets` (L67) |
| `00004_session_resolution.sql` | SECURITY DEFINER `resolve_session` (L17), `resolve_user_tenant` (L33), `create_tenant_membership` (L49), `create_session` (L59) |
| `00005_mfa.sql` | `mfa_challenges` (L5), `mfa_recovery_codes` (L13) |
| `00006_signup.sql` | `signup_tenant_owner(...)` (L14) |
| `00007_revoke_user_sessions.sql` | `revoke_user_sessions(uuid)` (L20) |
| `00020_tenant_pending_status.sql` | `tenants.status` gains `pending` (L11-12) |
| `00028_user_activity.sql` | `user_activity` (L15) — tenant audit log |
| `00032_user_activity_operator_read.sql` | operator read on `user_activity` |
| `00042_campaign_halt_activity.sql` | activity types for pause/resume/cancel (L11-12) |
| `00071_viewer_role.sql` | role CHECK gains `viewer` (L10-11) → owner/admin/member/viewer |

#### Compliance registry

| Migration | Introduces |
|---|---|
| `00008_compliance.sql` | `registrations` (L2: country, object_key, status, external_id, fields jsonb); `sender_ids` (L18: header, channel, country, status, external_id, waba_id, display_name, phone_number, email_domain, from_address, from_name, caller_id_number, voice_code, voice_verified); `templates` (L46: sender_id, name, channel, country, body, category, variables, cta_url, status) |
| `00017_sender_dns_records.sql` | `sender_dns_records` (L9) — email-domain DNS records |
| `00022_sender_wa_quality.sql` | `sender_ids.quality_rating`, `messaging_tier` (L14) |
| `00023_registration_ids.sql` | external-id triggers (L20, L38) |
| `00024_template_rich_content.sql` | `templates.rcs_content`, `wa_content`, `email_content` (L19) |
| `00025_template_category_check.sql` | category CHECK (L24) |
| `00031_sender_external_id_format.sql` | id format (L17, L30) |
| `00033`, `00035` | operator read on DNS records, registrations |
| `00037_rcs_carrier_templates.sql` | `templates.carrier_vendor`, `carrier_template_id`, `carrier_status`, `carrier_rejection_reason`, `carrier_submitted_at`, `carrier_updated_at` (L17); unique `templates_carrier_identity` (L56) |
| `00038_dlt_ids_are_customer_supplied.sql` | Relay records DLT ids, never issues them; `templates.external_id`, `dlt_category` (L40) |

#### Billing and money

| Migration | Introduces |
|---|---|
| `00009_wallet.sql` | `wallet_balances` (L6); `wallet_ledger` (L17: entry_type, amount_minor, balance_after_minor, campaign/journey ids+names) with append-only trigger `reject_ledger_mutation()` (L40); `payment_methods` (L53: brand, last4, is_default); `auto_recharge_configs` (L67); `invoices` (L79: subtotal, tax_rate_percent, tax, total); `invoice_line_items` (L94); `pricing_rates` (L106) |
| `00026_pricing_category_check.sql` | category CHECK (L31) |
| `00034_pricing_rates_operator_write.sql` | operator write grant |
| `00049_invoice_line_items_detail.sql` | `channel`, `country` (L9-10) |
| `00055_wallet_transfer_reference_unique.sql` | a bank UTR credits once (L8) |
| `00056_postpaid_credit_on_account.sql` | `account_invoices` (L21), `tenant_payments` (L66), `tenants.credit_limit_minor/currency` (L99-101) |

No column in any migration is named `gstin`, `hsn`, `sac`, `cgst`, `sgst` or `igst` (grep of
`db/migrations` and `internal/`). Tax is one `tax_rate_percent` per invoice.

#### Audience and consent

| Migration | Introduces |
|---|---|
| `00010_audience.sql` | `contact_lists` (L2); `contacts` (L10: msisdn, email, country, fields jsonb, consent jsonb, consented_at); `contact_list_members` (L30); `suppressions` (L40); `idempotency_keys` (L57) |
| `00057_contact_columns_mapping_and_consent.sql` | `contact_lists.variable_mapping` (L12); `contact_consent_records` (L24) immutable (L47); GIN on `fields` (L73) |
| `00064_contact_always_send_and_cap_percent.sql` | `tenants.send_cap_percent` (L15); `contacts.always_send` (L30), `last_capped_send_at` (L47) |
| `00065_withheld_contacts.sql` | `withheld_contacts` (L14) |

#### Campaigns

| Migration | Introduces |
|---|---|
| `00011_campaigns.sql` | `campaigns` (L8: channel, country, list_id, sender_id, template_id, fallback_channel/sender/template, status, scheduled_at, send_started_at, recipients, segments_per_message, cost_minor_min/max, retry_of) |
| `00041_campaign_halt.sql` | `paused_at`, `cancelled_at`, `dispatch_cursor` (L13) |
| `00043_campaign_segment_range.sql` | segment min/max (L12-14) |
| `00048_campaigns_operator_read.sql` | scheduler finds due campaigns across tenants |
| `00050_campaigns_held_until.sql` | `held_until` (L7) |
| `00059`, `00066`, `00067` | description; `created_by_*` (L13); newest index |
| `00072_campaign_drip.sql` | `drip_batch_size`, `drip_interval_minutes` (L9) |
| `00074_fallback_after_failure.sql` | `fallback_sends` (L14) — after-failure fallback, once per message |

#### Developer platform

| Migration | Introduces |
|---|---|
| `00012_developer.sql` | `api_keys` (L11: environment, scopes, key_prefix, key_hash); `webhook_endpoints` (L30); `webhook_events` (L50); `ip_allowlist` (L66); `resolve_api_key(bytea)` (L100) |
| `00052_webhook_signing_secret_sealed.sql` | `signing_secret_sealed` (L7) |
| `00060_webhook_retries.sql` | `webhook_retries` (L11) |
| `00073_webhook_integrations.sql` | `webhook_integrations` (L13: integration_type, sealed_headers) |

#### Settings, alerts, reports, branding

| Migration | Introduces |
|---|---|
| `00013_settings_reports.sql` | `tenant_settings` (L6: sso_enabled, sso_provider, sso_metadata_url, sso_entity_id, message_log_retention_days); `scheduled_reports` (L27) |
| `00027_alert_rules.sql` | `alert_rules` (L14) |
| `00061_tenant_settings_operator.sql` | operator read for the retention worker |
| `00062_alerts_and_report_sends.sql` | `alert_state` (L10); `scheduled_reports.next_send_at` (L29); `report_sends` (L38) |
| `00077_report_s3_destination.sql` | `report_s3_destinations` (L9: bucket, region, prefix, access_key_id, sealed_secret_key) |
| `00078_tenant_branding.sql` | `tenant_branding` (L9: display_name, logo_url, colours, support_email, custom_domain, domain_token, domain_verified_at) |

SSO in the schema is those four columns. No login path reads them: the only code touching SSO is
the settings GET/PUT (`internal/api/analytics.go:237-308`).

#### Inbox, support, chatbots, verify, journeys

| Migration | Introduces |
|---|---|
| `00014_support_inbox.sql` | `support_tickets` (L3), `support_messages` (L15), `conversations` (L28), `conversation_messages` (L46) |
| `00075_chatbots.sql` | `chatbot_flows` (L12: channel, sender_id, template_id, reply_body, active), `chatbot_keywords` (L27) |
| `00015_verify.sql` | `verify_services` (L5: channels, fallback_order, code_length, ttl, max_attempts, max_per_phone, window, cooldown, region_allowlist); `verifications` (L32: code_hash, attempts, cost, fraud_flag) |
| `00016_journeys.sql` | `journeys` (L10: trigger_type, trigger_list_id, trigger_run_at, steps jsonb) |
| `00063_journey_enrollments.sql` | `trigger_swept_at` (L10); `journey_enrollments` (L21: step_index, waiting, next_run_at, state) |

#### Operator plane, routing, carriers, RCS, media, controls, links

| Migration | Introduces |
|---|---|
| `00018_operator.sql` | `operator_users` (L13, role default `operator` L18); `operator_sessions` (L25); `routes` (L37: country, channel, carrier, label, priority, compliance_standing, cost_per_segment_minor, status); `rate_overrides` (L56); `operator_audit_log` (L72) immutable (L85); `tenants.flagged_at/flag_reason/throttled_at` (L100-102) |
| `00019_operator_visibility.sql` | `acting_as_operator()` (L23) |
| `00021`, `00029`, `00030`, `00054` | route standing registered/grey; status active/disabled; priority per carrier; unique label per corridor |
| `00036_operator_mfa.sql` | operator TOTP (L14), challenges (L18), recovery codes (L27) |
| `00039_operator_connections.sql` | `connections` (L9: host, port, system_id, system_type, bind_type, password_encrypted, max_tps, window_size, enquire_link_seconds, reconnect_backoff_seconds, health) ; `routes.connection_id` (L69) |
| `00040_tenant_throttle_rate.sql` | `tenants.throttled_rate_per_second` (L14) |
| `00051_connection_protocol.sql` | `connections.protocol` jsonb (L8) |
| `00044_media_assets.sql` | `media_assets` (L13) |
| `00045_rcs_agents.sql` | `rcs_agents` (L14, brand + verification fields); `rcs_agent_carrier_launches` (L86); `sender_ids.rcs_agent_id` (L134) |
| `00046`, `00047` | operator read; carrier vocabulary + name-length CHECKs |
| `00053_rcs_connections.sql` | `rcs_connections` (L10: vendor, settings, secrets_sealed, health) |
| `00068_rcs_trustsignal.sql` | vendor `trustsignal` (L5-6) |
| `00058_tenant_send_cap.sql` | `tenants.send_cap_per_day` (L16); `tenant_send_usage` (L34) |
| `00069_frequency_caps.sql` | `frequency_caps` (L16: daily/weekly/monthly, excluded_numbers) |
| `00070_short_links.sql` | `short_links` (L15), `link_clicks` (L29: is_bot, device, os, ip_hash) |
| `00076_links_operator_pool.sql` | public redirect works on the operator pool |

### 2.3 ClickHouse

| File | Object | Notes |
|---|---|---|
| `001_messages.sql:18` | `messages` | `ReplacingMergeTree(version)`, daily partitions, `ORDER BY (tenant_id, created_at, id)`; reads need `FINAL` |
| `001_messages.sql:68` | `message_events` | append-only transition log, 30-day TTL |
| `001_messages.sql:84` | `message_rollup_hourly` | `SummingMergeTree`, no TTL — analytics read here; written in the ingest path, not a materialised view |
| `002` | `messages.carrier` | carrying operator |
| `003` | bloom index on `carrier_ref` | DLR lookup |
| `004` | TTL ceiling 365 d | per-tenant retention enforced by worker |
| `005` | `sent_by_kind`, `sent_by_id`, `read_at` | |
| `006` | `rendered_text`, `otp`, `dlr_stat`, `dlr_err`, `dlr_*_at`, `dlr_raw` | OTP text never stored |

Design hazard named in the DDL itself: a later version that omits a column **erases** it
(`005_message_sender_and_read.sql`, `006_message_text_and_dlr.sql` headers). Every settle path
must carry every column forward.

---

## 3. API surface

### 3.1 Counts

| Surface | Count | Proof |
|---|---|---|
| Contract file | `openapi/control.json` is a **symlink** to `../../SMS-UI/openapi.json` — the frontend repo owns the contract | `ls -la openapi/` |
| Paths in the contract | **170** | `len(spec.paths)` |
| Operations | **211** | operationId count |
| Tags | effectively none: 203 untagged, 6 `operator`, 2 `billing` | spec |
| Ops in the generated `StrictServerInterface` | **204** | `internal/gen/api/control.gen.go` |
| Generated ops with a real handler (no 501 stub reachable) | **204 / 204** | every interface method has a `func (s *Server) X(ctx…` in `internal/api` |
| Contract ops **absent from generated code**, served by hand-mounted chi routes | **7**: `listOperatorCampaigns`, `getOperatorCampaign`, `suggestOperatorTenants`, `listOperatorTenantSenders`, `listOperatorMessages`, `exportOperatorMessages`, `getOperatorMessage` | `internal/api/operator_sends.go:31-39`. `control.gen.go` was last regenerated 2026-09-24 (`git log`); those ops entered the contract 2026-09-30 → 10-01 (`git log SMS-UI/openapi.json`) |
| Non-contract production routes | **39** | §3.3 |
| Dev-only routes (`ENABLE_DEV_ENDPOINTS=true`) | **9** | `internal/api/dev.go:33-43`, gated at `internal/api/server.go:380-383` |

As-built HTTP surface: **211 contract ops + 39 non-contract routes (+9 dev hooks)**. Comments in
code still say "151 operations" (`internal/api/server.go:441`, `internal/api/dev.go:23`,
`cmd/gen-unimplemented/main.go:5`) and the Makefile says 177 (`Makefile:43`) — all stale.

Drift risk: `make generate` (`Makefile:59-62`) has not been run since the operator-console ops
landed, so contract request validation does not apply to those 7 ops; they parse their own query
strings in `internal/api/operator_sends.go`. All 211 are reachable.

Auth model per route: session bearer (dashboard), API key (`resolve_api_key`,
`00012_developer.sql:100`, scopes in `internal/api/key_scopes.go`), or operator session; enforced
by the `authenticate` middleware (`internal/api/server.go:292`) — see §10.

### 3.2 Contract operations by domain

Grouped by path prefix (the spec has no useful tags). "Purpose" is the spec `summary` when there
is one, else the operationId read as words.

| Group | Ops |
|---|---|
| Auth & sessions | 19 |
| Team | 4 |
| Operator console | 70 |
| Wallet & billing | 16 |
| Senders, templates, registrations | 16 |
| RCS agents, capability, media | 8 |
| Audience | 13 |
| Campaigns | 9 |
| Messages | 3 |
| Analytics & reports | 5 |
| Developer | 17 |
| Verify / OTP | 8 |
| Inbox | 6 |
| Journeys | 9 |
| Settings | 4 |
| Support | 4 |
| **Total** | **211** |

#### Auth & sessions (19)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| POST | `/v1/auth/login` | `login` | login |
| POST | `/v1/auth/mfa/enroll` | `enrollMfa` | enroll mfa |
| POST | `/v1/auth/mfa/enroll/confirm` | `confirmMfaEnrollment` | confirm mfa enrollment |
| POST | `/v1/auth/mfa/disable` | `disableMfa` | disable mfa |
| POST | `/v1/auth/mfa/challenge` | `verifyMfaChallenge` | verify mfa challenge |
| POST | `/v1/auth/logout` | `logout` | logout |
| POST | `/v1/auth/signup` | `signup` | signup |
| POST | `/v1/auth/verify-email/resend` | `resendVerificationEmail` | resend verification email |
| POST | `/v1/auth/verify-email/confirm` | `confirmVerificationEmail` | confirm verification email |
| POST | `/v1/auth/password/forgot` | `requestPasswordReset` | request password reset |
| POST | `/v1/auth/password/reset` | `resetPassword` | reset password |
| GET | `/v1/me` | `getMe` | get me |
| PATCH | `/v1/me` | `updateMe` | update me |
| PATCH | `/v1/tenant` | `updateTenant` | update tenant |
| PATCH | `/v1/auth/password` | `changePassword` | change password |
| GET | `/v1/sso` | `getSso` | get sso |
| PUT | `/v1/sso` | `updateSso` | update sso |
| GET | `/v1/sessions` | `listSessions` | list sessions |
| DELETE | `/v1/sessions/{id}` | `revokeSession` | revoke session |

#### Team (4)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/team` | `getTeamMembers` | get team members |
| POST | `/v1/team/invite` | `inviteTeamMember` | invite team member |
| PATCH | `/v1/team/{id}` | `updateTeamMemberRole` | update team member role |
| DELETE | `/v1/team/{id}` | `removeTeamMember` | remove team member |

#### Operator console (70)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| POST | `/v1/operator/login` | `operatorLogin` | operator login |
| GET | `/v1/operator/me` | `getOperatorMe` | get operator me |
| GET | `/v1/operator/tenants` | `getTenants` | get tenants |
| GET | `/v1/operator/tenants/suggest` | `suggestOperatorTenants (hand-mounted)` | Tenants for a search-as-you-type box |
| GET | `/v1/operator/tenants/{id}/senders` | `listOperatorTenantSenders (hand-mounted)` | list operator tenant senders |
| GET | `/v1/operator/tenants/{id}` | `getTenantDetail` | get tenant detail |
| POST | `/v1/operator/tenants/{id}/suspend` | `suspendTenant` | suspend tenant |
| POST | `/v1/operator/tenants/{id}/reinstate` | `reinstateTenant` | reinstate tenant |
| POST | `/v1/operator/tenants/{id}/flag-abuse` | `flagTenantForAbuse` | flag tenant for abuse |
| POST | `/v1/operator/tenants/{id}/wallet/credit` | `creditTenantWallet` | Credit a tenant's wallet |
| GET | `/v1/operator/tenants/{id}/wallet` | `getTenantWallet` | Get a tenant's wallet balances |
| POST | `/v1/operator/tenants/{id}/throttle` | `throttleTenant` | Throttle a tenant to a fixed send rate |
| POST | `/v1/operator/tenants/{id}/dismiss-flag` | `dismissFlag` | dismiss flag |
| GET | `/v1/operator/abuse-queue` | `getAbuseQueue` | get abuse queue |
| GET | `/v1/operator/routes` | `getRoutes` | get routes |
| POST | `/v1/operator/routes` | `createRoute` | Add a route to a corridor |
| POST | `/v1/operator/routes/{id}/move-up` | `moveRouteUp` | move route up |
| POST | `/v1/operator/routes/{id}/move-down` | `moveRouteDown` | move route down |
| POST | `/v1/operator/routes/{id}/enable` | `enableRoute` | enable route |
| POST | `/v1/operator/routes/{id}/disable` | `disableRoute` | disable route |
| GET | `/v1/operator/rates` | `getRateCard` | get rate card |
| PATCH | `/v1/operator/rates/default` | `updateDefaultRate` | update default rate |
| POST | `/v1/operator/rates/overrides` | `createRateOverride` | create rate override |
| PATCH | `/v1/operator/rates/overrides/{id}` | `editRateOverride` | edit rate override |
| DELETE | `/v1/operator/rates/overrides/{id}` | `removeRateOverride` | remove rate override |
| GET | `/v1/operator/usage` | `getOperatorUsage` | get operator usage |
| GET | `/v1/operator/margin` | `getOperatorMargin` | get operator margin |
| GET | `/v1/operator/audit-log` | `getAuditLog` | get audit log |
| GET | `/v1/operator/campaigns` | `listOperatorCampaigns (hand-mounted)` | list operator campaigns |
| GET | `/v1/operator/campaigns/{id}` | `getOperatorCampaign (hand-mounted)` | One campaign live overview |
| GET | `/v1/operator/messages` | `listOperatorMessages (hand-mounted)` | list operator messages |
| GET | `/v1/operator/messages/export` | `exportOperatorMessages (hand-mounted)` | The whole filtered message log as CSV |
| GET | `/v1/operator/messages/{id}` | `getOperatorMessage (hand-mounted)` | get operator message |
| GET | `/v1/operator/user-activity` | `getUserActivity` | get user activity |
| GET | `/v1/operator/support/tickets` | `getOperatorSupportTickets` | get operator support tickets |
| GET | `/v1/operator/support/tickets/{id}` | `getOperatorSupportTicket` | get operator support ticket |
| POST | `/v1/operator/support/tickets/{id}/messages` | `addOperatorSupportMessage` | add operator support message |
| POST | `/v1/operator/support/tickets/{id}/resolve` | `resolveSupportTicket` | resolve support ticket |
| POST | `/v1/operator/support/tickets/{id}/reopen` | `reopenSupportTicket` | reopen support ticket |
| GET | `/v1/operator/approvals` | `getApprovalQueue` | get approval queue |
| POST | `/v1/operator/senders/{id}/approve` | `approveSenderItem` | approve sender item |
| POST | `/v1/operator/senders/{id}/reject` | `rejectSenderItem` | reject sender item |
| POST | `/v1/operator/templates/{id}/approve` | `approveTemplateItem` | approve template item |
| POST | `/v1/operator/templates/{id}/reject` | `rejectTemplateItem` | reject template item |
| POST | `/v1/operator/registrations/{id}/approve` | `approveRegistrationItem` | approve registration item |
| POST | `/v1/operator/registrations/{id}/reject` | `rejectRegistrationItem` | reject registration item |
| PATCH | `/v1/operator/routes/{id}` | `updateRoute` | Change a route's cost |
| DELETE | `/v1/operator/routes/{id}` | `deleteRoute` | Remove a route |
| POST | `/v1/operator/login/mfa` | `operatorVerifyMfa` | Complete an operator sign-in with a second factor |
| POST | `/v1/operator/mfa/enroll` | `operatorEnrollMfa` | Start second-factor enrolment for the signed-in operator |
| POST | `/v1/operator/mfa/confirm` | `operatorConfirmMfa` | Confirm enrolment with a code from the authenticator |
| POST | `/v1/operator/mfa/disable` | `operatorDisableMfa` | Turn off the second factor |
| GET | `/v1/operator/connections` | `getConnections` | List operator SMPP connections |
| POST | `/v1/operator/connections` | `createConnection` | Add an operator SMPP connection |
| GET | `/v1/operator/connections/{id}` | `getConnection` | get connection |
| PATCH | `/v1/operator/connections/{id}` | `updateConnection` | Change a connection's settings |
| DELETE | `/v1/operator/connections/{id}` | `deleteConnection` | Remove a connection |
| POST | `/v1/operator/connections/{id}/enable` | `enableConnection` | enable connection |
| POST | `/v1/operator/connections/{id}/disable` | `disableConnection` | disable connection |
| POST | `/v1/operator/connections/{id}/test` | `testConnection` | Attempt a bind without enabling the connection |
| GET | `/v1/operator/audit-log/export` | `exportAuditLog` | The whole filtered audit log as CSV |
| GET | `/v1/operator/user-activity/export` | `exportUserActivity` | The whole filtered user-activity log as CSV |
| POST | `/v1/operator/rcs-agents/{id}/approve` | `approveRcsAgent` | approve rcs agent |
| POST | `/v1/operator/rcs-agents/{id}/reject` | `rejectRcsAgent` | reject rcs agent |
| GET | `/v1/operator/tenants/{id}/invoices` | `listTenantInvoices` | List a tenant's account invoices |
| GET | `/v1/operator/tenants/{id}/invoices/export` | `exportTenantInvoices` | Export a tenant's account invoices as CSV |
| GET | `/v1/operator/tenants/{id}/payments` | `listTenantPayments` | List payments received from a tenant |
| POST | `/v1/operator/tenants/{id}/payments` | `recordTenantPayment` | Record a payment received from a tenant |
| POST | `/v1/operator/tenants/{id}/payments/{paymentId}/void` | `voidTenantPayment` | Void a recorded payment |
| PUT | `/v1/operator/tenants/{id}/credit-limit` | `setTenantCreditLimit` | Set a tenant's credit limit |

#### Wallet & billing (16)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/wallet/balances` | `listWalletBalances` | list wallet balances |
| GET | `/v1/wallet/ledger` | `listLedger` | list ledger |
| POST | `/v1/wallet/topup` | `topUpWallet` | top up wallet |
| GET | `/v1/wallet/payment-methods` | `listPaymentMethods` | list payment methods |
| POST | `/v1/wallet/payment-methods` | `addPaymentMethod` | add payment method |
| DELETE | `/v1/wallet/payment-methods/{id}` | `removePaymentMethod` | remove payment method |
| POST | `/v1/wallet/payment-methods/{id}/default` | `setDefaultPaymentMethod` | set default payment method |
| GET | `/v1/wallet/auto-recharge` | `listAutoRecharge` | list auto recharge |
| PUT | `/v1/wallet/auto-recharge` | `updateAutoRecharge` | update auto recharge |
| GET | `/v1/billing/invoices` | `listInvoices` | list invoices |
| GET | `/v1/billing/invoices/{id}` | `getInvoice` | get invoice |
| GET | `/v1/billing/usage` | `getUsage` | get usage |
| POST | `/v1/billing/estimate` | `estimateCost` | estimate cost |
| GET | `/v1/pricing` | `listPricing` | list pricing |
| GET | `/v1/billing/account-invoices` | `listAccountInvoices` | The invoices this tenant owes |
| GET | `/v1/billing/account-invoices/export` | `exportAccountInvoices` | Export this tenant's invoices as CSV |

#### Senders, templates, registrations (16)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/sender-ids` | `listSenderIds` | list sender ids |
| POST | `/v1/sender-ids` | `createSenderId` | create sender id |
| GET | `/v1/sender-ids/{id}` | `getSenderId` | get sender id |
| PATCH | `/v1/sender-ids/{id}` | `updateSenderId` | Correct a sender that has not been verified yet |
| DELETE | `/v1/sender-ids/{id}` | `deleteSenderId` | Remove a sender that nothing is using |
| POST | `/v1/sender-ids/{id}/voice-call` | `requestVoiceCall` | request voice call |
| POST | `/v1/sender-ids/{id}/voice-code` | `confirmVoiceCode` | confirm voice code |
| GET | `/v1/templates` | `listTemplates` | list templates |
| POST | `/v1/templates` | `createTemplate` | create template |
| GET | `/v1/templates/{id}` | `getTemplate` | get template |
| PATCH | `/v1/templates/{id}` | `updateTemplate` | Correct a template |
| DELETE | `/v1/templates/{id}` | `deleteTemplate` | Remove a template that nothing is using |
| POST | `/v1/templates/{id}/carrier-registration` | `registerTemplateWithCarrier` | Submit an RCS template to the carrier for approval, or attach a code from their portal |
| GET | `/v1/registrations` | `listRegistrations` | list registrations |
| POST | `/v1/registrations` | `createRegistration` | create registration |
| GET | `/v1/registrations/{id}` | `getRegistration` | get registration |

#### RCS agents, capability, media (8)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| POST | `/v1/rcs/capabilities` | `checkRcsCapabilities` | Ask the RCS carrier which of these handsets can receive RCS |
| GET | `/v1/rcs/agents` | `listRcsAgents` | list rcs agents |
| POST | `/v1/rcs/agents` | `createRcsAgent` | create rcs agent |
| GET | `/v1/rcs/agents/{id}` | `getRcsAgent` | get rcs agent |
| PATCH | `/v1/rcs/agents/{id}` | `updateRcsAgent` | update rcs agent |
| POST | `/v1/rcs/agents/{id}/verification` | `submitRcsAgentVerification` | submit rcs agent verification |
| POST | `/v1/rcs/agents/{id}/launch` | `launchRcsAgentOnCarrier` | launch rcs agent on carrier |
| POST | `/v1/media` | `uploadMedia` | upload media |

#### Audience (13)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/contact-lists` | `listContactLists` | list contact lists |
| POST | `/v1/contact-lists` | `createContactList` | create contact list |
| GET | `/v1/contact-lists/{id}` | `getContactList` | get contact list |
| PATCH | `/v1/contact-lists/{id}` | `updateContactList` | update contact list |
| DELETE | `/v1/contact-lists/{id}` | `deleteContactList` | delete contact list |
| GET | `/v1/contacts` | `listContacts` | list contacts |
| POST | `/v1/contacts/import` | `importContacts` | import contacts |
| DELETE | `/v1/contact-lists/{id}/members/{contactId}` | `removeContactListMember` | remove contact list member |
| GET | `/v1/suppressions` | `listSuppressions` | list suppressions |
| POST | `/v1/suppressions` | `addSuppressions` | add suppressions |
| DELETE | `/v1/suppressions/{identity}` | `removeSuppression` | remove suppression |
| PATCH | `/v1/contacts/{id}` | `updateContact` | update contact |
| POST | `/v1/contacts/consent` | `recordContactConsent` | record contact consent |

#### Campaigns (9)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/campaigns` | `listCampaigns` | list campaigns |
| POST | `/v1/campaigns` | `createCampaign` | create campaign |
| GET | `/v1/campaigns/{id}` | `getCampaign` | get campaign |
| POST | `/v1/campaigns/{id}/pause` | `pauseCampaign` | Hold a sending campaign. No further recipients are dispatched until it is resumed. |
| POST | `/v1/campaigns/{id}/resume` | `resumeCampaign` | Resume a paused campaign from exactly where it stopped. |
| POST | `/v1/campaigns/{id}/cancel` | `cancelCampaign` | Stop a campaign for good. Recipients not yet dispatched are cancelled and never charged. |
| GET | `/v1/campaigns/{id}/messages` | `listCampaignMessages` | list campaign messages |
| POST | `/v1/campaigns/estimate` | `estimateCampaign` | estimate campaign |
| GET | `/v1/campaigns/{id}/recipients` | `listCampaignRecipients` | The recipients of one campaign, and what became of each |

#### Messages (3)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/messages` | `listMessages` | list messages |
| POST | `/v1/messages` | `sendMessage` | Send a single message |
| GET | `/v1/messages/{id}` | `getMessage` | Read one message |

#### Analytics & reports (5)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/analytics` | `getAnalytics` | get analytics |
| GET | `/v1/analytics/reports` | `listScheduledReports` | list scheduled reports |
| POST | `/v1/analytics/reports` | `createScheduledReport` | create scheduled report |
| PATCH | `/v1/analytics/reports/{id}` | `updateScheduledReport` | update scheduled report |
| DELETE | `/v1/analytics/reports/{id}` | `deleteScheduledReport` | delete scheduled report |

#### Developer (17)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/developer/scopes` | `listApiScopes` | list api scopes |
| GET | `/v1/developer/api-keys` | `listApiKeys` | list api keys |
| POST | `/v1/developer/api-keys` | `createApiKey` | create api key |
| POST | `/v1/developer/api-keys/{id}/rotate` | `rotateApiKey` | rotate api key |
| DELETE | `/v1/developer/api-keys/{id}` | `revokeApiKey` | revoke api key |
| GET | `/v1/developer/webhooks` | `listWebhookEndpoints` | list webhook endpoints |
| POST | `/v1/developer/webhooks` | `createWebhookEndpoint` | create webhook endpoint |
| GET | `/v1/developer/webhooks/{id}` | `getWebhookEndpoint` | get webhook endpoint |
| PATCH | `/v1/developer/webhooks/{id}` | `updateWebhookEndpoint` | update webhook endpoint |
| DELETE | `/v1/developer/webhooks/{id}` | `deleteWebhookEndpoint` | delete webhook endpoint |
| GET | `/v1/developer/webhooks/{id}/events` | `listWebhookEvents` | list webhook events |
| POST | `/v1/developer/webhooks/{id}/test-event` | `sendWebhookTestEvent` | send webhook test event |
| POST | `/v1/developer/webhooks/{id}/events/{eventId}/resend` | `resendWebhookEvent` | resend webhook event |
| GET | `/v1/developer/ip-allowlist` | `listIpAllowlist` | list ip allowlist |
| POST | `/v1/developer/ip-allowlist` | `addIpAllowlistEntry` | add ip allowlist entry |
| DELETE | `/v1/developer/ip-allowlist/{id}` | `removeIpAllowlistEntry` | remove ip allowlist entry |
| GET | `/v1/developer/rate-limit` | `getRateLimit` | get rate limit |

#### Verify / OTP (8)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/verify/services` | `listVerifyServices` | list verify services |
| POST | `/v1/verify/services` | `createVerifyService` | create verify service |
| GET | `/v1/verify/services/{id}` | `getVerifyService` | get verify service |
| PATCH | `/v1/verify/services/{id}` | `updateVerifyService` | update verify service |
| POST | `/v1/verify/services/{id}/verifications` | `createVerification` | create verification |
| POST | `/v1/verify/services/{id}/verifications/{vid}/check` | `checkVerification` | check verification |
| GET | `/v1/verify/services/{id}/analytics` | `getVerifyAnalytics` | get verify analytics |
| GET | `/v1/verify/services/{id}/attempts` | `listVerificationAttempts` | list verification attempts |

#### Inbox (6)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/conversations` | `listConversations` | list conversations |
| GET | `/v1/conversations/{id}` | `getConversation` | get conversation |
| POST | `/v1/conversations/{id}/reply` | `replyToConversation` | reply to conversation |
| POST | `/v1/conversations/{id}/read` | `markConversationRead` | mark conversation read |
| POST | `/v1/conversations/{id}/close` | `closeConversation` | close conversation |
| POST | `/v1/conversations/{id}/reopen` | `reopenConversation` | reopen conversation |

#### Journeys (9)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/automation/journeys` | `listJourneys` | list journeys |
| POST | `/v1/automation/journeys` | `createJourney` | create journey |
| GET | `/v1/automation/journeys/{id}` | `getJourney` | get journey |
| PATCH | `/v1/automation/journeys/{id}` | `updateJourney` | update journey |
| POST | `/v1/automation/journeys/{id}/activate` | `activateJourney` | activate journey |
| POST | `/v1/automation/journeys/{id}/pause` | `pauseJourney` | pause journey |
| POST | `/v1/automation/journeys/{id}/resume` | `resumeJourney` | resume journey |
| POST | `/v1/automation/journeys/{id}/archive` | `archiveJourney` | archive journey |
| POST | `/v1/automation/journeys/{id}/unarchive` | `unarchiveJourney` | unarchive journey |

#### Settings (4)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/alerts` | `getAlerts` | get alerts |
| PATCH | `/v1/alerts` | `updateAlerts` | update alerts |
| GET | `/v1/data-retention` | `getDataRetention` | get data retention |
| PATCH | `/v1/data-retention` | `updateDataRetention` | update data retention |

#### Support (4)

| Method | Path | operationId | Purpose |
|---|---|---|---|
| GET | `/v1/support/tickets` | `getSupportTickets` | get support tickets |
| POST | `/v1/support/tickets` | `createSupportTicket` | create support ticket |
| GET | `/v1/support/tickets/{id}` | `getSupportTicket` | get support ticket |
| POST | `/v1/support/tickets/{id}/messages` | `addSupportMessage` | add support message |

### 3.3 Routes mounted outside the contract

Registered directly on chi in `internal/api/server.go:366-436`, after the middleware chain
(`server.go:275-301`: request id, trusted-proxy real IP, client info, body limit, enum
rejection, drip peek, request log, abuse guard, operator-network restriction, `authenticate`,
read-only viewers, unknown-field rejection).

| # | Method | Path | Handler | Auth / gating | Purpose | UI? |
|---|---|---|---|---|---|---|
| 1 | GET | `/healthz` | `server.go:366` | public | liveness + dependency check | n/a |
| 2 | GET | `/readyz` | `server.go:369` | public | readiness | n/a |
| 3 | GET | `/metrics` | `server.go:373` → `observability.go:214` | **public** | counters, timings, incidents | no |
| 4 | GET | `/v1/events` | `events.go:177` → `:186` | session | SSE live updates | yes (`SMS-UI/src/app/api/events/route.ts`) |
| 5 | GET | `/v1/operator/events` | `events.go:178` | operator | SSE for console | yes (`SMS-UI/src/app/api/operator/events/route.ts`) |
| 6 | GET | `/v1/operator/journeys` | `operator_sends.go:35` → `:319` | operator | cross-tenant journeys | **no** |
| 7 | GET | `/v1/operator/messages/summary` | `operator_sends.go:37` → `:754` | operator | message-log totals | **no** |
| 8-9 | GET, PUT | `/v1/frequency-caps`, `/v1/frequency-caps/{channel}` | `frequency_caps.go:23-24` | session | per-recipient caps | **no** |
| 10-14 | POST, POST, GET, GET, GET | `/v1/links`, `/v1/links/bulk`, `/v1/links`, `/v1/links/clicks`, `/v1/links/stats` | `links.go:27-31` | session/key | short links + click analytics | **no** |
| 15 | GET | `/l/{code}` | `links.go:32` → `:348` | **public** | redirect + click capture | n/a |
| 16-17 | GET, PUT | `/v1/campaigns/{id}/drip` | `campaign_drip.go:90-91` | session | drip batch/interval | **no** |
| 18 | POST | `/v1/messages/test` | `test_send.go:24` → `:44` | session | test send to own handsets | **no** |
| 19-23 | GET, POST, GET, PATCH, DELETE | `/v1/chatbots[/{id}]` | `chatbots.go:25-29` | session | keyword auto-replies | **no** |
| 24-27 | GET, PUT, DELETE, POST | `/v1/report-export/s3[/test]` | `report_s3.go:22-25` | session | customer S3 report export | **no** |
| 28-31 | GET, PUT, DELETE, POST | `/v1/branding`, `/v1/branding/verify-domain` | `branding.go:24-27` | session | white-label branding, TXT domain check (`branding.go:49`) | **no** |
| 32 | GET | `/v1/branding/public` | `branding.go:28` → `:213` | **public** | host → brand lookup | **no** |
| 33-34 | GET, PUT | `/v1/developer/webhooks/{id}/integration` | `webhook_integrations.go:25-26` | session | WebEngage/MoEngage/CleverTap formats | **no** |
| 35-36 | GET | `/v1/analytics/errors`, `/v1/analytics/latency` | `analytics_detail.go:17-18` | session | error breakdown, latency | **no** |
| 37 | GET | `/brand/{file}` | `media.go:194` → `:260` | public; only with media store (`server.go:426-428`) | RCS artwork for carriers | n/a |
| 38 | GET | `/v1/media/{id}/{filename}` | `media.go:196` | signed URL | asset read-back | yes |
| 39 | POST | `/v1/carrier-webhooks/rcs/{vendor}/{token}` | `carrier_webhooks.go:39` | **shared secret in URL path** + optional IP allowlist; mounted only if `CarrierWebhookToken != ""` (`server.go:434-436`) | RCS DLRs, template decisions, inbound | n/a |

Dev hooks (`dev.go:34-42`, only when `ENABLE_DEV_ENDPOINTS` is exactly `"true"`, `dev.go:24-26`):
`advance-campaign`, `advance-sender`, `advance-template`, `advance-registration`,
`advance-email-dns`, `drain-wallet`, `set-my-role`, `receive-inbound-message`,
`reset-mock-state`; each acts only on the caller's own tenant (`dev.go:28-31`). The template
`deploy/env.production.example:52` sets it `false`; the live value is UNVERIFIED (a comment at
`internal/api/wallet.go:136-138` says the flag was `true` on the deployment an earlier bug was
reported against).

The "UI?" column is from a grep of every non-test, non-mock file under `SMS-UI/src` for each
path fragment: chatbots, `/v1/links`, frequency-caps, `/drip`, report-export, branding,
`/integration` (webhook), analytics/errors, analytics/latency, messages/test, operator/journeys,
messages/summary → **0 files each**.

---

## 4. Message lifecycle end to end

### 4.1 Entry points

| Entry | Path into the pipeline |
|---|---|
| `POST /v1/messages` (session or API key) | `internal/api/messages.go` → `sending.Service.Send` (`internal/sending/service.go:239`), coalesced when the coalescer runs (`:245-247`) |
| `POST /v1/messages/test` | `internal/api/test_send.go:44` |
| Campaign launch (now or by scheduler) | `sending.Service.LaunchCampaign` (`internal/sending/campaign.go:221`) → pages through the list → `SendBatch` (`internal/sending/batch.go`) with per-recipient leg choice (`internal/sending/legs.go`) |
| Journey send step | `RunJourneys` (`internal/api/journey_engine.go:106-114`) → `internal/sending/journey.go` |
| Verify / OTP | `POST /v1/verify/services/{id}/verifications` → send with `OneTimeCode` (text never stored, `db/clickhouse/006_message_text_and_dlr.sql`) |
| Inbox reply / chatbot reply | `internal/api/support.go:366` prices it; `internal/api/inbound_sms.go:48` `FileReply` |
| Fallback after failure | `internal/sending/fallback_after_failure.go` on an `undelivered` report |

Throttles applied before the pipeline: per-tenant send limiter 200/s live, 20/s test
(`internal/api/send_ratelimit.go:22-24`), operator throttle (`tenants.throttled_rate_per_second`,
`00040`), daily send cap and send-share cap (`internal/api/send_cap.go`,
`internal/sending/cap_cut.go`), abuse guard 1200 req/IP/min default
(`internal/platform/config/config.go:246`).

### 4.2 The single-send pipeline (`internal/sending/service.go:252-545`)

Order matters: everything that can refuse happens before money moves or a carrier sees anything
(`service.go:235-238`).

1. Resolve sender (cached) — not found → `sender_not_found` (`:253-257`).
2. Country content rules: URLs in the body checked against the regime (India bans public
   shorteners) → `content_not_allowed` (`:268-276`).
3. Normalise the MSISDN to the sender's country, else any E.164 (`:283-289`).
4. Price: `billing.SegmentCount(body)` × `CachedPricingRate(..., category "")` (`:292-298`) →
   `no_rate` if none. **Every channel, including Email/WhatsApp/RCS, is priced per GSM/UCS-2
   SMS segment**, and single sends always pass an empty category, so a corridor priced only per
   category bills its **cheapest** category (`internal/store/wallet.go:612-631`).
5. Template lookup → `template_not_found` (`:304-310`); suppression lookup (`:315-320`); wallet
   balance (`:322-332`); tenant status (cached, `:334`); RCS agent/carrier (`:342`); DND (`:344`);
   render with variables (`:350-353`).
6. **The gate** `messaging.Check` (`:358-383`), then frequency cap (`:388-391`).
7. Refused → recorded in ClickHouse as `rejected` with cost 0 and the code (`:396-412`).
8. **Hold**: ledger `charge` entry for the full cost (`:415-427`); `ErrInsufficientFunds` at the
   DB → `insufficient_balance`.
9. Resolve route/carrier from `routes` (`:434`, `resolvePath` `:913`); record `queued` (`:436-447`).
10. DLT ids for India (`dltIDs`, `:883`); `carrierFor(channel).Submit(...)` (`:455-466`).
11. Receipt → `outcomeOf` (`:547`): pending (bind window), accepted → `submitted/sent`, refused
    (`NO_OPERATOR_BIND`, `DLT_IDS_MISSING`, `OUTSIDE_PROMOTIONAL_WINDOW`, `:540-544`) or carrier
    rejection → release hold now (`:484-488`).
12. Later: DLR → `ApplyDeliveryReport` (`:587`) → `settle` (`:602`): legal transition check,
    `EffectCharge` keeps money, `EffectRelease` refunds (`:642-650`), `read_at` without state
    change for RCS reads (`:744`), customer webhook via `Settled` callback (`:700-701`),
    after-failure fallback on `undelivered` (`:704-706`).
13. No DLR within 48 h → reconciler settles `expired` → refund (`internal/sending/reconciler.go:17`,
    `:44-49`).

### 4.3 Refusal codes (the gate and around it)

Gate order is `internal/domain/messaging/gate.go:168-258`; codes are mapped at `:300-340`.

| Code | Condition | Raised at | Outcome |
|---|---|---|---|
| `sender_not_found` | sender id unknown for tenant | `service.go:255` | rejected, not recorded |
| `content_not_allowed` | URL fails regime rule (IN shorteners) | `service.go:271-274`; mapped `gate.go:304` | rejected |
| `no_rate` | no pricing row for country/channel | `service.go:294-297` | rejected |
| `template_not_found` | template id unknown | `service.go:306` | rejected |
| `tenant_suspended` | tenant status `suspended` | `gate.go:169-171` | rejected, recorded |
| `invalid_recipient` | MSISDN not normalisable | `gate.go:172-174` | rejected, recorded |
| `entity_not_registered` | tenant's registration for the sender's country not approved (added in `4417d70`) | `gate.go:178-180`; input `service.go:360` | rejected; listed as "awaiting contract" (`gate.go:296`) |
| `sender_not_approved` | sender status ≠ approved | `gate.go:181-183` | rejected |
| `template_not_approved` | template status ≠ approved | `gate.go:185-187` | rejected |
| `sender_template_mismatch` | template belongs to another sender | `gate.go:188-190` | rejected |
| `registered_template_required` | regime requires a registered template (IN) and none given | `gate.go:196-198`; `RegisteredTemplateRequired` `service.go:962` | rejected |
| `template_body_mismatch` | body is not an instantiation of the template | `gate.go:212-215` | rejected |
| `variable_unresolved` | unfilled `{{slot}}` | `gate.go:221-223` | rejected |
| `carrier_template_not_approved` | RCS carrier hasn't approved the template | `gate.go:227-229` | rejected |
| `rcs_agent_not_resolved` | RCS with a real gateway but no agent for the sender | `gate.go:235-237` | rejected |
| `outside_promotional_window` | IN promotional outside 10:00–21:00 IST (`compliance/india.go:149-156`) | `gate.go:238-240`; also re-checked at the bind (`service.go:543`) | rejected |
| `recipient_suppressed` | number on suppression list | `gate.go:243-245` | rejected |
| `dnd_blocked` | NCPR says blocked | `gate.go:248-250` | rejected (never happens today: no register) |
| `dnd_check_unavailable` | IN + SMS + PROMOTIONAL template and register lookup failed | `gate.go:251-253`; `dnd.go:96-115` | **every Indian promotional SMS today** |
| `insufficient_balance` | balance < cost | `gate.go:254-256`; DB re-check `service.go:421-424` | rejected |
| `frequency_cap` | over per-channel daily/weekly/monthly cap | `frequency_cap.go`; mapped `gate.go:302` | rejected; "awaiting contract" |
| `no_operator_bind` | SMS routed to a carrier with no live bind | `smpp.go:808-809`; map `service.go:541` | rejected at submit, refunded |
| `dlt_ids_missing` | IN SMS lacking PE or template DLT id | `smpp.go:810-813`; map `service.go:542` | rejected at submit, refunded |
| campaign-only: withheld by send cap/share | daily cap or share reached | `cap_cut.go`, `withheld_contacts` (`00065`) | withheld, never charged |
| campaign-only: no consent on channel | contact consent ≠ `opted_in` for the channel | `internal/store/audience.go:780`, `:834` | excluded from audience |

**Gap:** the single-send path does not check consent — only suppression (`service.go:315-320`).
Consent is enforced for campaigns and journeys through the audience queries
(`store/audience.go:780`) and for fallback legs (`legs.go:13-19`).

### 4.4 State machine (`internal/domain/messaging/state.go`)

```
queued ──► submitting ──► submitted ──► accepted ──► delivered      (charge kept)
   │            │             │            ├──────► undelivered    (refund, triggers fallback)
   │            │             │            └──────► expired        (refund)
   │            │             ├──► carrier_rejected                (refund)
   │            │             └──► expired                         (refund)
   └──► rejected ◄┘                                                (refund / never charged)
```

Transitions at `state.go:43-52`; billing effect at `state.go:84-93`; `accepted` shows to
customers as `sent`, never `delivered` (`state.go:95-100`).

### 4.5 End-to-end diagram

```
 Dashboard / API key / campaign scheduler / journey engine / verify / inbox reply
                │
       authenticate + abuse guard + send limiter (200/s) + send cap
                │
   ┌────────────▼─────────────┐   Postgres (tenant pool, RLS)
   │ sending.Service.sendOne  │◄── sender, template, suppression, wallet, tenant status,
   │  (or coalesced batch)    │    pricing (override → default), entity registration
   └────────────┬─────────────┘
     content rule │ normalise │ price │ GATE (16 checks) │ frequency cap
                │ refused ──────────────► ClickHouse `rejected`, cost 0 ──► (no webhook charge)
                ▼
     wallet_ledger "charge" hold (append-only)  ── insufficient ─► rejected
                ▼
     routes table → carrier, route_id ; ClickHouse `queued`
                ▼
   connector.Registry.For(channel)
     ├─ "RCS" → RCSRouter / single env carrier ─► Trustsignal | Airtel | Vi | Jio | Google (HTTPS)
     └─ default → SMPPRouter
            ├─ channel == SMS and binds configured → operator bind (gosmpp, TLV 0x1400/1401/1402)
            └─ anything else (WHATSAPP, EMAIL, VOICE, or no binds) → Sandbox (in-process)
                ▼
     receipt → submitted | refused (refund) | pending (bind window)
                ▼
   DLR in:  SMPP deliver_sm (smpp.go:680) │ POST /v1/carrier-webhooks/rcs/{vendor}/{token}
            │ sandbox drainer every 2 s
                ▼
     settle(): legal transition → delivered (keep) | undelivered/expired/rejected (refund)
                ├─► ClickHouse new version + message_events + hourly rollup
                ├─► customer webhook (HMAC-SHA256, retries 1m/5m/30m, CRM formats)
                ├─► SSE /v1/events
                └─► undelivered on a campaign's primary leg → fallback leg send (once)
   reconciler every 15 min: no report in 48 h → expired → refund
```

### 4.6 Customer webhooks

- Signature: `X-Relay-Signature: v1=<hex HMAC-SHA256(secret, "<unix ts>." + body)>`, plus
  `X-Relay-Timestamp`, `X-Relay-Event` (`internal/webhook/webhook.go:45-50`, `:68-71`). Replay
  protection is left to the receiver (timestamp is signed; Relay documents no tolerance).
- Delivery: 10 s timeout, redirects refused (`webhook.go:21-29`); https only; literal private IPs
  refused (`webhook.go:129-152`) — **hostnames that resolve to private addresses are not
  checked at dial time** (no `DialContext` control), so DNS-based SSRF remains possible.
- Retries: durable in `webhook_retries`, 1 m → 5 m → 30 m (`internal/api/webhook_emit.go:66`),
  lease 2 min (`:77`).
- Formats: native, or transformed for WebEngage / MoEngage / CleverTap with sealed custom headers
  (`internal/webhook/integrations.go:31`, `00073`).
- Event types emitted include `message.queued`, `message.sent`, `message.delivered`,
  `message.failed`, `message.read`, `message.inbound`, `campaign.progress`,
  `campaign.status_changed`, `campaign.completed`, `campaign.failed` (string literals in
  `internal/api/*.go`).

---
