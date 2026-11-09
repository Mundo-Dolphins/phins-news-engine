# ADR 0039: Admin panel

## Status

Accepted

## Context

Operating the bot meant SSH and the `tools` CLI: claims to retry or release,
a model to train and promote, each with its own flags and terminal text. The
owner wanted the same operations in a web panel, and an agent (MCP) able to
run them, without the panel ever endangering the running bot.

## Decision taken with the owner

- **Whole catalog.** Every operator action ends up in the panel, not a
  subset. Step 1 ships the foundation, `claims.*` and `train.analyze`,
  `train.run`, `train.promote`, `train.rollback`; the rest follows by group.
  `train.run` can promote, so like `train.promote` and `train.rollback` it is
  destructive and needs `confirm`.
- **Separate process.** The panel is its own binary, `phins-news-admin`, with
  its own service unit. The engine provides the core in package `admin`; the
  host binary calls `admin.Run` and adds private actions with `admin.Add`,
  like `cli.Add` for commands.
- **Phased.** Phase 1 covers the non-interactive actions. Interactive flows
  (editorial cycle, reviews, telegram login) come as web screens in phase 2.
- **One binary, two modes.** The mode comes from the profile: `production`
  gives production mode, anything else gives local mode. Each action declares
  the modes it is offered in.
- **Step 2, batch 1.** 27 more actions: `outbox.*` (status, backlog, inspect,
  expire_preview, expire, repair), `stats.report`, `sqlite.*` (inspect,
  integrity, migrate_status), `label.*` (set, history, conflicts,
  resolve_conflict), `source_policy.*` (validate, inspect, audit, simulate)
  and `train.*` (build, evaluate, mine, shadow_compare, audit_dataset,
  audit_gates, recheck_corpus, backfill_rejects, import_labels). Only
  `train.recheck_corpus` of the new train actions is offered in production;
  `outbox.expire` and `outbox.repair` are destructive and need `confirm`.
  Their logic lives in `app/pipelinestats`, `app/dbinspect`,
  `app/humanlabels` and `app/datasetaudit`, shared with the CLI.
- **Step 2, batch 2.** 26 more actions in three groups: `entities.*` (list,
  show, extract, league_evaluate, approve, reject, generate, roster_sync,
  discover, proposals_verify, fallback_test, wikidata_evaluate),
  `classifier_dictionary.*` (list, show, metrics, approve, reject, analyze)
  and `editorial.*` (metrics, review, canary_status, canary_promote,
  canary_abort, veto_report, classify, classify_eval). Production offers only
  the 12 read-only ones; approvals, rejects, generation, the LLM and web
  actions and the dataset reports are local-only. `editorial.canary_promote`
  and `editorial.canary_abort` are `read`: like the CLI they only print the
  config change to make. LLM runs with failed batches finish with a
  `WARNING` summary and keep the saved work. Their logic lives in
  `infra/entitywiring`, `app/editorialmetrics` and `app/lookback`, shared with
  the CLI.
- **Production writes only operational state.** Reads everywhere, plus
  `claims.retry`, `claims.release`, `outbox.expire`, `outbox.repair` and the
  `label.*` writes. Training, promote and rollback are local-only and reach the Pi through the normal deploy; no CPU-heavy job runs
  on the Pi.
- **MCP exposes everything.** Destructive actions need `confirm: true`. The
  agent has its own bearer token, not the owner's password.

## Decisions

**One definition, three surfaces.** An `action.Action` (name, kind, modes,
typed params) feeds the REST API, the MCP tools and the web form. Tool name =
action name with `.` replaced by `_`. The action calls application services
directly and never the Cobra tree, so the CLI keeps its output.

**Schema guard.** The admin never migrates the operational DB in production.
At start it runs `CheckSchemaCompatible` (DB ahead of the binary) and
`PendingMigrations` (DB behind). If either reports a problem, write and
destructive actions answer `503 schema_mismatch`, reads keep working,
`/healthz` reports `schema: mismatch`, and a warning is logged. In local mode
the admin migrates its DB copy, as the CLI does.

**Own state in `admin.db`.** Jobs, their logs, the audit trail and sessions
live in a separate SQLite file (`ADMIN_DB_PATH`, default `data/admin.db`),
not in the operational DB. Sessions persist across restarts. Jobs left
`running` by a previous process are marked `interrupted` at start.

**Not hurting the bot.** The admin opens the operational DB with
`_txlock=immediate`: every transaction starts with `BEGIN IMMEDIATE` and
waits out `busy_timeout` instead of failing with `SQLITE_BUSY` on a lock
upgrade. The bot's own transaction mode is not changed. At most one
`Exclusive` job runs at a time (`409` otherwise).

**Access.** Plain HTTP on the LAN. Clients must come from
`ADMIN_ALLOWED_CIDRS`; the client address is `RemoteAddr`, and
`X-Forwarded-For` / `X-Real-IP` are never trusted. The session cookie
(`phins_admin_session`) is HttpOnly, SameSite=Strict, with a 12 h sliding TTL
and stored as a SHA-256 hash. Five failed logins per IP per minute give
`429`. `/mcp` needs `ADMIN_MCP_TOKEN`. Settings are env-only (`ADMIN_*`) and
validated by `AdminConfig.Validate`, so the bot loads the same config without
them.

**Listening.** `ADMIN_LISTEN_ADDRS` is a list (default `127.0.0.1:8480`), one
`http.Server` per address, so a healthcheck can use loopback while the panel
is reachable on the LAN address. A bad address fails `Run` before serving.
Servers have `ReadHeaderTimeout` 10 s and no `WriteTimeout`, because job
events stream over SSE.

**Shutdown.** On context cancellation `Run` stops the HTTP servers first (10 s
grace) so no new job can start, letting in-flight requests finish while open SSE job streams are ended; cancels the running jobs; waits for them;
then closes the admin and operational databases.

**Frontend.** TypeScript, Vite and Preact with a small router. The built
`dist` is committed and embedded with `go:embed`, so `go build` needs no
Node; a Makefile target rebuilds it.

## Consequences

The panel is a second writer on the operational DB, relying on WAL and
`busy_timeout`; heavy jobs are kept off the Pi by mode. A schema change needs
the admin binary deployed with the bot, or the panel degrades to read-only in
production until it is. `admin.Add` must run before `Run`; the registry is
not safe for later changes. Plain HTTP is acceptable only on a trusted LAN;
TLS and multiple users are out of scope.
