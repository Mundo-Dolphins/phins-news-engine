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
- **Seen items backfill.** `seen_items.backfill` mirrors `seenitemsbackfill`:
  write, long-running and exclusive, offered in both modes. It takes no
  params and returns the candidate, inserted and skipped counts; a second run
  inserts nothing. The logic lives in `app/seenitemsbackfill`, shared with
  the CLI.
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
- **Step 2, batch 3.** 18 more engine actions in six groups: `audit.*`
  (duplicates, quality), `config.validate`, `model.validate` and
  `clustering.rules_validate`, `dictionary.*` (validate, inspect, history,
  diff, regression_check, rollback, mine_candidates) and `events.*` (type_list,
  brief, gate, impact, type_approve, type_reject). Production offers the 13
  read-only ones; `dictionary.rollback` (destructive, so it needs `confirm`
  and never prompts), `dictionary.regression_check`,
  `dictionary.mine_candidates`, `events.type_approve` and `events.type_reject`
  are local-only. A both-mode action accepts a path param in production only
  when it equals the configured value. Reports whose CLI exits non-zero after
  printing (`dictionary.validate` with errors, `dictionary.regression_check`
  over its threshold, `events.gate` blocked) return a result whose summary
  starts with `FAILED:` or `BLOCKED:`. `events.impact` is long-running.
  A host adds its own actions with `admin.Add`; the engine hands them the
  host's fetcher categories through `Options.FetcherExtensions` (the bot
  uses both for `ocr.check` and `dictionary.fetch`). The logic lives in
  `infra/dictionarywiring`, `infra/eventrules`, `infra/configcheck` and
  `app/decisionaudit`, shared with the CLI.
- **Step 2, batch 4.** Four evaluation actions in three groups:
  `clustering.evaluate` and `dedup.evaluate` (production and local, both
  read-only and long-running) and the local-only `backtest.candidate` (writes
  its CSV) and `backtest.league` (read-only). In production the datasets and
  `generated_rules` accept only the configured value. The backtests have no
  `db` param: they read the panel's configured database, `backtest.league`
  through its own read-only connection and never `env.DB`, and pass the
  admin's settings file where the CLI passes `--config`. A violated league
  invariant returns the report with a `FAILED:` summary. The host adds
  `outbox.reconcile`. The logic lives in `infra/evalwiring`, shared with the
  CLI.
- **Phase 2, part 1: review screens.** The interactive reviews become web
  screens under `/review`, offered in local mode only (the nav entry shows
  only there). A shared `ReviewScreen` component holds the queue on the
  left, the selected item in the centre and a decision bar with keyboard
  shortcuts (`J`/`K` or arrows to move, `O` opens the original, `S` skips,
  plus each review's decision letters); shortcuts are ignored while a field
  has focus or with Ctrl, Meta or Alt held. The first review is
  `train review` (`/review/train`) over two local-only actions:
  `train.review_queue` (read) returns the queue joined with each article
  and its prior human labels, and `train.review_decide` (write) applies
  `publish` or `discard` with exactly the CLI's effect (human label,
  proposal approved or rejected, queue entry deleted). An item labeled
  elsewhere or whose article was purged is dropped with a notice
  (`dropped_already_labeled`, `dropped_purged`), and an entry already gone
  from the queue is shown as a notice too, never an error page. Labels from
  the panel have source `admin:train-review` and `labeled_by` the bare panel
  username, or `mcp` for MCP decisions. Each decision is saved at once;
  skip is session-only; the queue reloads when the tab becomes visible.
  Only `http:`/`https:` URLs become links (new tab, `noopener noreferrer`),
  and links use their own colour tokens with at least 4.5:1 contrast in
  both themes. The per-item logic lives in `app/trainreview`, shared with
  the CLI.
- **Phase 2, part 2: dataset review.** `train review-dataset` becomes
  `/review/dataset` on the same screen, over two local-only actions:
  `train.review_dataset_queue` (read) returns the audit CSV rows not yet in
  the decision log, each with its dataset fields and the original URL from
  the operational database when the article is still stored (otherwise the
  card shows only the domain), and `train.review_dataset_decide` (write)
  applies `publish` or `reject` (relabel) or `confirm` (keep the label),
  shortcuts `P`, `R`, `C` (the CLI's `k` is taken by "previous"). Files and
  defaults are the CLI's: dataset `data/editorial_dataset.yml` (never
  modified), audit `data/editorial_dataset-review.csv`, reviewed copy
  `data/editorial_dataset-reviewed.yml` and log `<out>.review.csv`; the
  screen shows the three paths as editable fields. Each decision rewrites
  the copy and its manifest and appends one log line with the CLI's field
  changes; the reviewer is the bare panel username, or `mcp`. Reopening the
  screen shows only pending rows. A copy that no longer matches the dataset
  is a `conflict` with the CLI's message, `out` equal to the dataset is
  invalid, and a row already in the log is a `conflict` the screen shows as
  a notice. Panel writes are serialised; the CLI is not, so reviewing the
  same copy from both at once can lose decisions (the screen says so). The
  logic lives in `app/datasetreview`, shared with the CLI.
- **Phase 2, part 3: entity review.** `entities review` becomes
  `/review/entities` on the same screen, over two local-only reads:
  `entities.review_queue` returns the pending proposals best score first
  (`entityproposal.Score`, as the CLI) with the verification lines, the
  CLI's affiliation warning (`FREE AGENT` / `UNKNOWN`, from
  `entitydiscovery.AffiliationLabel`), the suggested match or candidates,
  the aliases, the metadata without `verify_attempted_at`, every evidence
  article with its original URL from the operational database (none when
  purged: the title stays, unlinked) and the verification web sources;
  `entities.catalog_search` looks a name up in the merged entity catalog
  (manual and generated), ignoring case, accents and punctuation. Decisions
  use the existing `entities.approve` and `entities.reject`: `A` approve,
  `R` reject, `S` skip. A possible duplicate offers no `A`: `N` approves it
  as a new entity (`as_new`) and `M` opens a modal search over the catalog,
  pre-filled with the suggested match or the name, whose pick approves it
  as an alias (`merge_into`) — merging from the review is new, the CLI
  sends you to `entities approve --merge-into`. While the search is open no
  key reaches the review's shortcuts; arrow keys move within the results,
  `Enter` picks and `Escape` closes. An approval the dictionary or catalog
  refuses (`entitydiscovery.ErrApprovalRefused`: already present, no
  affiliation, unknown merge target, invalid result) is a `conflict` with
  the CLI's reason; the screen keeps the proposal and shows it. An unknown
  or already decided proposal is dropped with a notice. The approval writes `configs/` under the existing
  `ConfigFilesMu`.
- **Phase 2, part 4: event rule review.** `events review` becomes
  `/review/events`, in three zones over five local-only actions, with the
  CLI's sample (`eventrulecycle.Draw`, 60 generic articles from the last 30
  days plus the typed control sample) and check options. **Discover** runs
  `events.rules_discover`, a long-running exclusive job that asks the
  offline-training LLM for phrases and stores them as pending proposals
  (failed calls give a `WARNING:` summary; a disabled or misconfigured LLM
  makes the job fail with the reason, since the job starts before the
  check); the screen follows the job (polling it if its event stream
  drops) and reloads the queue when it ends. With no generic article in
  the sample the queue is empty with that notice and nothing is rejected.
  **Review** shows `events.rules_queue` on the shared screen: target kind
  and target, the phrase, its impact on the sample (generic articles moved
  per type, examples, retyped articles), the check verdict and the cited
  articles linked to their originals. `A` accept, `R` reject, `S` skip,
  `E` edit (`events.rules_decide`; `R` rejects only a proposal still
  pending, the apply list's reject only one still approved). The panel never auto-rejects, unlike
  the CLI: a proposal that fails the checks is flagged "does not pass" with
  its reasons and offers no `A` (an accept of it, from MCP or a stale
  screen, is a `conflict` with the reasons); the owner rejects it, one by
  one or with "reject all failing" (`events.rules_reject_failing`, after a
  confirmation; the ids it listed are sent), which rejects only those
  proposals still pending and still failing. Proposals with no evidence in the sample stay pending, outside
  the queue, listed apart. `E` opens an inline editor: Enter previews the
  phrase (impact and verdict, nothing written), Save or `Ctrl+Enter` saves
  it once the last preview of that text passed — the edited rule is approved
  and the original rejected as superseded, as in the CLI; while it is open
  no key reaches the review's shortcuts. **Apply** lists the approved rules
  not yet applied (each rejectable) and runs `events.rules_apply`, a
  long-running exclusive job: the gate scores the generated rules plus all
  of them (no keep step) and shows the before/after metrics; on PASS it
  writes the generated rules file under `ConfigFilesMu` and marks them
  applied, on BLOCKED it shows the reason and leaves them approved. Every
  decision is saved only while the proposal is still in the status it was
  read in, and a rule rejected while the gate ran makes apply a `conflict`
  ("run apply again") with nothing written; the screen also disables the
  decisions while the apply job runs. A failure marking the rules applied
  after the file was written says so; running apply again finishes it. The
  logic lives in `app/eventrulecycle`, shared with the CLI.
- **Phase 2, part 5: host-provided review.** The dictionary update review
  (official-source roster and staff changes proposed against the
  dictionary) has no action in the engine: the host registers seven
  local-only `dictionary.update_*` actions (`update_start`, a long-running
  exclusive job; `update_session`, `update_decide`, `update_decide_type`,
  `update_preview`, `update_apply` and `update_discard`, the last two
  destructive) over the session files of its own command. The engine ships
  only the screen, `/review/dictionary`, which stays generic: its card in the
  Review index appears, and the route works, only when `listActions()`
  contains `dictionary.update_session`; otherwise the route says "Not
  available on this host". Three zones. **Session** shows the open session
  (id, status, start, dictionary version, proposals and undecided count),
  "Fetch and start" (the start job, followed as in part 4) when there is none
  and "Discard session" after the typed confirmation; a session whose
  dictionary changed since it started is shown with the host's conflict
  message and can be discarded from there. **Review** puts every proposal on
  the shared screen — decided ones stay listed with their decision and can be
  decided again (the index is posted, the earlier decision replaced) — with
  the kind, the detail, conflicts and the dictionary entry beside the source
  observation (links only through `safeHref`). `A` accept, `R` reject, `S`
  skip (a recorded decision, not just moving on), `E` edit (an inline editor
  for the category and a note appended to the detail; while it is open no key
  reaches the shortcuts); after a decision the selection moves to the next
  undecided proposal (`ReviewScreen`'s `goTo`). "Accept/Reject all of this
  type" decides the proposal and every later one of its kind, after a
  confirmation. **Apply** shows the undecided count, the preview (changes,
  diff, regression check) and "Apply to the dictionary", disabled until every
  proposal has a decision (skip counts), as in the command line; it runs after
  the typed confirmation and shows the versions, changes and diff written.
- **Phase 2, part 6: editorial cycle wizard.** `editorial cycle` becomes
  `/cycle` (local only; linked from the nav and the Review index), a wizard
  over the `cycle.*` actions that runs the CLI's steps in the CLI's order —
  sync with the remote host, Telegram import, roster sync, article review,
  entities, event rules, mined terms, training, summary — and waits for
  "Next" after each one; the deploy stays in the terminal and the summary
  says so. The wizard state (`cycle.state`: the current step, each step's
  status, result and error, the review session id and the start options)
  lives in the admin state under one key, so it survives a sync and a panel
  restart: reopening continues at the saved step without running earlier
  steps again; a step job still running is followed, and a step left
  `running` by an earlier process can be run again (or, for event rules,
  discovered or applied again) or skipped, so the wizard never stays
  blocked. One cycle at a time; "Start cycle" takes the sample size and the
  entity verification depth (local, full, none); "Abandon cycle", after the
  typed confirmation, cancels the cycle and its review session. `cycle.next`
  refuses a step that is not finished, and with `step` (the panel always
  sends the step it shows) refuses when the cycle is at another step, so a
  repeated click never moves past the next step; `skip` works on every step but the
  article review and the summary, and records why. The screen shows a rail
  of the nine steps with their status, the current step's panel, and Next,
  Skip (skippable steps only; not while a training winner waits for its
  decision) and Retry (failed job steps).
  **Job steps** (`cycle.sync`, `cycle.telegram_import`, `cycle.roster_sync`,
  the entity discovery, the event rule discover and apply, `cycle.train`)
  run as long-running jobs whose log the panel follows; a failure marks the
  step failed with its error shown as is, and it can be retried. The sync is
  the `SwapsDB` action of the gate described under "Replacing the
  operational DB" below: it is refused while anything else runs and every
  other action fails fast while it runs — except `cycle.state`, which reads
  only the admin store (`NoOperationalDB`), so the wizard follows the
  running sync. `cycle.telegram_import`, the entity discovery, `cycle.train`,
  `cycle.promote` and the event rule discover and apply are `Exclusive`, as
  their CLI-side actions are. The wizard state stays in the
  admin store; after the sync the wizard reads it again, and the later
  steps read their articles, proposals and labels from the new operational
  DB. The host's
  `Options.Remote` provides the remote host, its key, the sync script and
  the Telegram client; never a panel field. With no remote or Telegram
  hooks the step says "Not available on this host" and only Skip moves on.
  The Telegram client is built non-interactive: an unauthorised or expired
  session fails the step with "log in to Telegram from the terminal first"
  and never waits for a code. **Review steps** embed the review screens:
  `/review/articles` (`cycle.review_open`, `review_queue`, `review_decide`,
  `review_finish`, `review_pause`; the frozen review set of the session,
  `P` publish, `R` reject, `A` ambiguous, `D` duplicate, `S` same news,
  `U` update, `E` edit category, `K` skip, decided articles can be decided
  again, and leaving the screen pauses the session; the session's frozen
  set is one of the local tables a sync carries over, and a session whose
  frozen set is gone anyway is cancelled and replaced, which
  `cycle.review_open` reports as `replaced_session_id` and the screen
  shows), the entity review
  limited to the session's articles (`entities.review_queue` with
  `session_id`) after `cycle.entities_discover`, finished with
  `cycle.entities_finish`, the event rule review with the cycle's own
  discover and apply actions (`cycle.event_rules_discover`,
  `cycle.event_rules_apply`, so the step counts them) finished with
  `cycle.event_rules_finish`, and `/review/terms` after
  `cycle.terms_prepare` (`A` approve, asking for a category when the guess
  is not one, `R` reject into the mining policy's ignored terms, `C`
  categorize; the dictionary is written once, by `cycle.terms_finish`, after
  every term has a decision). **Training** shows the winner with its
  metrics and asks before "Promote" (typed confirmation) or "Don't
  promote"; a second decision is refused. **Summary** shows the CLI's
  report; finishing the cycle writes its audit report as the CLI does.
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
`running` by a previous process are marked `interrupted` at start. A small
key/value table (`admin_state`, `Env.State`) keeps admin progress that must
survive a replacement of the operational DB.

**Not hurting the bot.** The admin opens the operational DB with
`_txlock=immediate`: every transaction starts with `BEGIN IMMEDIATE` and
waits out `busy_timeout` instead of failing with `SQLITE_BUSY` on a lock
upgrade. The bot's own transaction mode is not changed. At most one
`Exclusive` job runs at a time (`409` otherwise).

**Replacing the operational DB.** An action marked `SwapsDB` may install a
new operational DB handle (`Env.SwapDB`). The job runner gates every run: a
`SwapsDB` action starts only when nothing else runs (`409 conflict` "other
actions are running; try again when they finish"), and any action requested
while it runs fails fast (`409 conflict` "database sync in progress"), over
REST and MCP alike. Each run reads `Env.DB` from the gate, so later actions
use the new handle; the old one is closed once the swapping action returns.
An action marked `NoOperationalDB` never touches the operational DB (its
`Env.DB` is nil): it bypasses the gate, running during a swap and never
blocking one.
Host hooks for the remote host and Telegram arrive as `Options.Remote`
(host, key, sync script and the Telegram client factory); the panel never
takes them from a field, and a zero value means "not available on this
host".

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
