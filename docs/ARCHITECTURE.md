# Architecture

The engine uses ports and adapters (hexagonal architecture). The reasons are
in [ADR 0001](adr/0001-hexagonal-architecture.md); this page describes the
current shape of the code. The online flow is in [PIPELINE.md](PIPELINE.md).

## Layers

```mermaid
flowchart TB
    subgraph HOST["host program (not in this repository)"]
        MAIN["main package: engine.Run, cli.Root, registrations"]
        EXT["host adapters: scrapers, extra fetchers, history client, remote ops"]
    end
    subgraph OUTER["engine, cli, adapters, infra"]
        ENG["engine: composition root of the service"]
        CLI["cli: operator command tree"]
        AD["adapters: scheduler, sqlite, feeds, telegram"]
        INF["infra: config, sqlite open and migrate, netguard, loaders, clients"]
    end
    subgraph APP["app and ports"]
        PORTS["ports/in and ports/out"]
        UC["app/usecase and offline workflows"]
    end
    subgraph DOM["domain (pure, no I/O)"]
        D["article, normalization, deduplication, classification, clustering, editorial, sourcepolicy, publishing"]
    end
    MAIN --> ENG
    MAIN --> CLI
    EXT --> PORTS
    ENG --> AD
    ENG --> INF
    CLI --> UC
    AD --> PORTS
    INF --> PORTS
    UC --> PORTS
    UC --> DOM
    AD --> DOM
```

| Layer | Owns | May import |
|---|---|---|
| `domain` | Every decision rule: validation, normalization, fingerprints, similarity, relevance, event signals, join decisions, editorial scoring, source policy, freshness and publication policy | the standard library only |
| `ports` | Interfaces between the use cases and the outside: `ports/in` (driving) and `ports/out` (driven) | `domain` |
| `app` | Orchestration of one cycle (`app/usecase`) and offline workflows (training, evaluation, dictionary and entity maintenance, replay) | `domain`, `ports` |
| `adapters` | Implementations of `ports/out` against SQLite, HTTP APIs and Telegram; the scheduler that drives `ports/in` | everything above, plus drivers |
| `infra` | Cross-cutting technical code: configuration, the SQLite connection and migrator, logging, clock, file loaders, HTTP hardening, the LLM, search and Wikidata clients, fetcher wiring | everything above |
| `engine`, `cli` | Composition: build adapters, inject them into use cases, run | everything |

`domain` imports nothing from this module, and the use cases never import an
adapter. A use case recognises adapter outcomes through types declared in
`ports/out` (for example `out.PermanentError`, `out.RateLimitedError` and
`out.AmbiguousError` from a `Publisher`). A multi-part payload (thread) goes through the optional `out.ThreadPublisher` ([ADR 0040](adr/0040-bluesky-threads.md)).

## Composition

`engine.Run` is the composition root of the long-running service. In order,
it:

1. loads and validates the configuration (`config.Load`);
2. opens SQLite and applies the embedded migrations;
3. loads the source catalog and builds the fetchers with
   `fetcherwiring.Build`, which evaluates the seven categories (`website`,
   `rss`, `reddit`, `bluesky`, `twitter`, `instagram`, `youtube`, in that
   order) and logs a capability report;
4. builds the Telegram publisher and, when reconciliation is enabled and the
   host supplied one, the channel-history client;
5. loads the relevance dictionary, the editorial model and veto rules, the
   clustering rules and entity catalogs, and the source policies, failing
   fast on any invalid file;
6. wires the use cases and starts `scheduler.Scheduler`, which runs until the
   context is cancelled.

The library implements `rss`, `reddit`, `bluesky` and `youtube`. The other
categories exist in configuration so that a host can supply them through
`fetcherwiring.Extensions`. An enabled category without a builder shows up
in the capability report with an explicit reason; whether that is fatal
depends on `fetch_sources.<category>.on_missing_dependency` and the profile
(see [CONFIG-REFERENCE.md](CONFIG-REFERENCE.md#profiles)). Zero active
sources is always fatal.

The `reddit` fetcher sets `article.MetadataMediaImages` (`media_images`, a
JSON array of `{"url","type"}`) on image posts whose link is a `.jpg`,
`.jpeg` or `.png` on `i.redd.it`, so event handlers can download the image.

`cli.Root()` is the composition root of the operator tools. Commands that
need something only a host can provide (a scraper, remote operations, a
channel-history client) look it up in `cli/cmd/shared` and fail with a
clear error when nothing is registered.

## Packages by concern

| Concern | Packages |
|---|---|
| Intake | `adapters/outbound/rss`, `reddit`, `bluesky`, `youtube`, `compositefetcher`, `sourcefilter`; `infra/rssclient`, `infra/sourcesfile`, `infra/fetcherwiring` |
| Identity and dedup | `domain/ingestidentity`, `domain/normalization`, `domain/deduplication`, `domain/similarity` |
| Relevance | `domain/classification`, `domain/dictionary`, `infra/dictionaryfile` |
| Events | `domain/eventsignals`, `domain/clustering`, `domain/clusterlifecycle`, `domain/clustergate`, `infra/clusteringrules`, `infra/entitycatalog`, `infra/leaguecatalog` |
| Source policy | `domain/sourcepolicy`, `infra/sourcepolicyfile` |
| Editorial | `domain/editorial`, `domain/editorialveto`, `infra/editorialmodel`, `infra/editorialveto`, `app/editorialtrain`, `app/editorialcycle` |
| Publication | `domain/publishing`, `app/usecase` (`PublishOutbox`, `RepairOutbox`, `ExpireOutbox`), `app/outboxreconcile`, `adapters/outbound/telegram`, `adapters/outbound/bluesky` (publisher and threads, [ADR 0035](adr/0035-bluesky-publisher.md), [ADR 0040](adr/0040-bluesky-threads.md)), `adapters/outbound/mediafs` (image download and retention, [ADR 0036](adr/0036-media-store.md)), operator alerts (`out.Alerter`, `telegram.Alerter`, [ADR 0037](adr/0037-operator-alerts.md)), event claim operator commands ([runbook](EVENT-CLAIMS-RUNBOOK.md)) |
| Match state | `domain/matchstate`, `adapters/outbound/espnscoreboard` |
| Storage | `adapters/outbound/sqlite` and the `sqlite*` proposal and cache adapters, `infra/sqlite`, `migrations` |
| Knowledge (tools only) | `infra/wikidata`, `app/knowledgecache`, `domain/entityidentity`, `app/entitydiscovery` |

## Admin panel

Package `admin` is a second inbound surface next to the scheduler: a web
panel and an MCP endpoint over operator actions, run by the host's
`phins-news-admin` binary through `admin.Run` ([ADR 0039](adr/0039-admin-panel.md)).
A host adds its own actions with `admin.Add` before `Run`.

| Piece | Package |
|---|---|
| Typed actions, one definition for REST, MCP and the form | `admin/action`, `admin/actions/{claims,train,outbox,stats,sqlite,label,sourcepolicy,entities,classifierdictionary,editorial,audit,validation,dictionary,events,evaluation,cycle}`; shared logic in `app/pipelinestats`, `app/trainreview`, `app/datasetreview`, `app/dbinspect`, `app/humanlabels`, `app/datasetaudit`, `app/editorialmetrics`, `app/lookback`, `app/decisionaudit`, `infra/entitywiring`, `infra/evalwiring`, `infra/dictionarywiring`, `infra/eventrules`, `infra/configcheck` |
| Jobs, logs, audit, sessions and admin state in `admin.db` | `admin/store`, `admin/jobs` |
| Login, session cookie, LAN allow-list, bearer token | `admin/auth` |
| REST API, SSE job events, embedded SPA | `admin/httpapi`, `admin/web` |
| Review screens (`/review`, local only): shared queue, item and decision layout with shortcuts; `train review`, `train review-dataset`, `entities review` (with merging a possible duplicate from a catalog search) and `events review` (discover, review with an inline phrase editor, gate and apply; failing proposals flagged, never auto-rejected); the dictionary update review (`/review/dictionary`) only when the host registers its `dictionary.update_*` actions | `admin/web` (`ReviewScreen`, `MergePicker`, `PhraseEditor`, `ProposalEditor`), actions `train.review_queue` and `train.review_decide` (logic in `app/trainreview`), `train.review_dataset_queue` and `train.review_dataset_decide` (logic in `app/datasetreview`), `entities.review_queue`, `entities.catalog_search`, `entities.approve` and `entities.reject` (logic in `app/entitydiscovery`), `events.rules_discover`, `events.rules_queue`, `events.rules_decide`, `events.rules_reject_failing` and `events.rules_apply` (logic in `app/eventrulecycle`); the dictionary update actions are the host's |
| Editorial cycle wizard (`/cycle`, local only): `editorial cycle` step by step in the CLI's order, each waiting for Next; state in the admin state, so it survives a sync and a restart; the sync swaps the operational DB through the gate; deploy stays in the terminal | `admin/web` (`Cycle`, `ReviewArticles`, `ReviewTerms`, plus the entity and event rule screens), actions `cycle.*` in `admin/actions/cycle` (logic in `app/editorialcycle`, `app/editorialsync`, `app/telegramimport`, `app/entitycycle`, `app/eventrulecycle`, `app/dictionarymining`, `infra/trainwiring`); remote and Telegram hooks from the host's `Options.Remote` |
| MCP tools | `admin/mcpserver` |

The mode follows the profile: `production` offers reads plus claims retry and
release, `outbox.expire`/`outbox.repair` and label writes; any other
profile is `local` and offers every action. In production
the admin never migrates the operational DB: if its schema is ahead of or
behind the binary, write actions answer `503 schema_mismatch` and reads keep
working. The admin opens the operational DB with `BEGIN IMMEDIATE`
transactions so it waits for the bot's writes rather than failing.

## Guard rails in tests

- `engine` has a dependency test that fails if the service depends on the
  knowledge-source packages (Wikidata and its cache): an outage of an
  external knowledge service can never affect ingestion.
- `testsupport/repofs` fails any test that reads fixtures outside the
  module root.
- `docscheck` validates this documentation against the command tree.
- CI runs a public boundary check and a consumer smoke test that builds a
  host module against the library.

## Single writer

SQLite allows one writer, and the connection pool has one connection. The
pipeline processes articles one at a time inside a cycle, and the atomic
claim and reservation operations (seen items, outbox leases, cluster
publication slots, cooldowns) rely on that serialisation. See
[DATABASE.md](DATABASE.md) and [ADR 0003](adr/0003-sqlite-primary-storage.md).
