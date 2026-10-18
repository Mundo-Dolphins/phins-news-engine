# Database

SQLite is the only datastore ([ADR 0003](adr/0003-sqlite-primary-storage.md)).
The service and the tools open the same file: `sqlite.path`, which the
`SQLITE_DB_PATH` environment variable overrides. There is no database
server. The driver is `modernc.org/sqlite`, which needs no cgo.

## Connection settings

Set when the database is opened (`infra/sqlite/db.go`):

| Setting | Value | Why |
|---|---|---|
| `journal_mode` | `WAL` | Readers do not block the writer |
| `synchronous` | `NORMAL` | Safe against a process crash; a power cut can lose the last commits |
| `foreign_keys` | `ON` | Referential integrity |
| `temp_store` | `MEMORY` | No temporary files on slow or flash storage |
| `busy_timeout` | `sqlite.busy_timeout_ms` | Wait for a lock instead of failing |
| Connections | one | Single-writer serialisation makes the claim and reserve operations atomic |

The WAL is checkpointed (`PRAGMA wal_checkpoint(PASSIVE)`) at the end of
each retention run. While the service runs, `-wal` and `-shm` files sit
next to the database; back up with `sqlite3 .backup` or the SQLite backup
API, never by copying the `.db` file alone. New database files are created
`0600`.

## Schema map

Migrations live in `migrations` as `NNNN_description.sql` and are embedded
in the module. The main groups of tables:

| Group | Tables | Used by |
|---|---|---|
| Articles and audit | `articles`, `article_processing_events`, `sources`, `source_id_migrations` | every pipeline step |
| Identity and dedup | `seen_items`, `exact_fingerprints`, `article_aliases`, `article_dedup_terms` | [DEDUPLICATION.md](DEDUPLICATION.md), layers 1 to 3 |
| Clustering | `event_clusters`, `event_cluster_members`, `event_keys`, `event_representations`, `event_clustering_decisions`, `event_cluster_representative_history`, `event_cluster_publications` | layers 4 and 5 |
| Source policy | `source_policy_decisions`, `source_policy_cooldowns` | gates A and B |
| Publication | `publication_outbox`, `publication_attempts` | [ADR 0030](adr/0030-outbox-delivery-guarantee.md) |
| Editorial and training | `model_registry`, `dictionary_registry`, `dictionary_history`, `human_labels`, `human_label_conflicts`, `editorial_examples`, `editorial_review_decisions`, `editorial_cycle_sessions`, `editorial_cycle_session_candidates`, `training_runs`, `editorial_shadow_comparisons`, `editorial_label_proposals`, `hard_example_candidates`, `review_queue` | offline tools, shadow and canary reports |
| Dictionary, entities and event rules | `dictionary_candidate_terms`, `classifier_dictionary_*`, `entity_*`, `event_rule_proposals`, `unknown_entity_occurrences`, `knowledge_*_cache`, `web_search_cache` | offline tools |
| Bookkeeping | `schema_migrations` | the migrator |

Counts and open work:

```bash
phins-news-engine-tools sqlite inspect --config configs/example/settings.yml
```

`sqlite inspect` applies pending migrations before it reports.

## Migrations

- Forward only. An applied file is never edited; a change is a new file.
  There are no down migrations.
- Files run in name order. Each one runs in its own transaction and is
  recorded by file name in `schema_migrations` (no checksum of the SQL).
  Foreign keys are checked around each migration so it cannot introduce
  violations. Two numbers appear twice (`0038_*`, `0039_*`); full file names
  keep the order deterministic.
- `engine.Run` applies pending migrations at start-up, as does
  `sqlite inspect`. These only report:

```bash
phins-news-engine-tools sqlite migrate status --config configs/example/settings.yml
phins-news-engine-tools sqlite integrity --config configs/example/settings.yml
```

- Rolling a binary back across a migration is safe only when the older code
  tolerates the newer schema. The migrations so far are additive. Take a
  backup before upgrading.

## Retention

`RunRetention` runs every `scheduler.cleanup_interval_hours`, right after
`CloseInactiveClusters`. Windows come from `retention`:

| Data | Setting |
|---|---|
| Rejected articles (not duplicates) | `rejected_articles_days` |
| Duplicate rejections | `duplicates_days` |
| Audit events | `audit_events_days` |
| Publication attempts | `publication_attempts_days` |
| Closed clusters, counted from `closed_at` | `clusters_days` |
| Published articles, human labels, model and dictionary registries | never deleted |

Deleting an article cascades to its aliases, fingerprints and `seen_items`
row (see the residual risk in [DEDUPLICATION.md](DEDUPLICATION.md)).
`cluster_lifecycle.close_after_hours` closes inactive clusters so retention
can purge them. Keep `close_after_hours + clusters_days * 24` at least as
large as the widest temporal window in the clustering rules, so a cluster
that could still be joined is never purged. This is an operator invariant;
configuration validation does not check it.

## Backups and recovery

The library ships no backup scheduler. Whoever operates a deployment should
take regular `sqlite3 .backup` copies (or an equivalent online backup),
store them off the host, and test restores on a copy.

| Situation | What to do |
|---|---|
| Corruption suspected | Run `sqlite integrity` on a copy first; restore the newest backup that passes |
| Restoring a backup | Stop the service, move the database and its `-wal` and `-shm` files aside, put the verified copy in place with mode `0600`, start the service. Items fetched since the backup are fetched again while they are inside their windows |
| Outbox after a restore | Rows delivered after the backup are unknown to it. Enable reconciliation (`publication.reconciliation`) with a host history client so recent channel posts are matched, or accept a few repeated posts |
| Labels and review sessions in an older local database | `phins-news-engine-tools sqlite merge-local` migrates the snapshot, then carries them forward |
