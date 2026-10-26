# Configuration reference

The engine reads one YAML settings file (the `--config` flag of the tools,
the `configPath` argument of `engine.Run`) plus a few environment variables.
`infra/config` loads and validates it. A complete fictional example is
`configs/example/settings.yml`; `make validate-config` loads every file in
`configs/example` through the same loaders the engine uses.

The file names several other files (source catalog, source policies,
dictionary, clustering rules, entity catalogs, veto rules). Relative paths
resolve against the working directory of the process.

## Loading rules

1. The YAML file is decoded strictly: an unknown key is a load error in
   every profile.
2. Environment variables then override specific fields (see
   [Secrets and environment variables](#secrets-and-environment-variables)).
   Nothing else can be overridden from the environment.
3. The configuration is validated section by section. Most sections are
   required; a section with an `enabled` switch validates its other keys only
   when it is enabled.

## Profiles

The profile is chosen by the tools' `--profile` flag, otherwise by the
environment variable named in `config.ProfileEnvVar` (default
`NEWS_ENGINE_PROFILE`), otherwise `local` (`config.ResolveProfile`). A host
may rename the variable at start-up.

| Check | `local` | `test` | `production` |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` must be set | yes | yes | yes |
| `SCRAPING_SERVICE_API_KEY` must be set when `scraping_service.enabled` | yes | yes | yes |
| `telegram.channel_id` must not be the placeholder `-1000000000000` | no | no | yes |
| `dictionary_update.sources` URLs must not be loopback | no | no | yes |
| An enabled fetch category with a missing dependency (or no registered builder) | degrades: the category is inactive | fatal | fatal |
| Zero active sources overall | fatal | fatal | fatal |

`fetch_sources.<category>.on_missing_dependency` (`fatal` or `degrade`)
overrides the profile default for one category.

## Secrets and environment variables

Secret fields are tagged `yaml:"-"`: they cannot appear in the YAML file
(the strict decoder rejects them) and come only from these variables.

| Variable | Field | Secret | Needed when |
|---|---|---|---|
| `TELEGRAM_BOT_TOKEN` | `telegram.bot_token` | yes | always |
| `TELEGRAM_API_ID` | `telegram.api_id` | yes | a host history client needs it (ignored when not numeric) |
| `TELEGRAM_API_HASH` | `telegram.api_hash` | yes | same |
| `TELEGRAM_PHONE` | `telegram.phone` | yes | same, to create a new session |
| `TELEGRAM_SESSION_PATH` | `telegram.session_path` | no | to move the session file |
| `TELEGRAM_TIMEOUT_SECONDS` | `telegram.timeout_seconds` | no | optional (ignored when not numeric) |
| `SCRAPING_SERVICE_API_KEY` | `scraping_service.api_key` | yes | `scraping_service.enabled` |
| `WEB_SEARCH_API_KEY` | `catalog_maintenance.web_lookup.api_key` | yes | `catalog_maintenance.web_lookup.enabled` (validated at load, so the service needs it too) |
| `TAVILY_API_KEY` | `catalog_maintenance.web_lookup.fallback.api_key` | yes | a Tavily fallback provider |
| `LLM_API_KEY` | `entity_discovery.llm.api_key` | yes | `entities discover` against a hosted LLM |
| `OFFLINE_TRAINING_LLM_API_KEY` | `offline_training.llm.api_key` | yes | `editorial classify`, `train mine` against a hosted LLM |
| `LLM_FALLBACK_API_KEY` | `entity_extraction.fallback.api_key` | yes | `entity_extraction.fallback.enabled` with a provider that needs a key |
| `SQLITE_DB_PATH` | `sqlite.path` | no | to point at another database |

`Config.Redacted()` returns a copy with every secret masked; a reflection
test fails if a new secret field is not covered.

## Sections

"Service" means `engine.Run` reads the key. "Tools" means only commands in
`cli.Root()` read it, though the engine still validates it at load.

### Storage and scheduling

| Key | Meaning | Example value |
|---|---|---|
| `sqlite.path` | Database file | `data/operational.db` |
| `sqlite.busy_timeout_ms` | Lock wait, positive | 5000 |
| `scheduler.check_interval_minutes` | `ProcessCycle` interval | 5 |
| `scheduler.outbox_interval_minutes` | `PublishOutbox` interval | 1 |
| `scheduler.cleanup_interval_hours` | `CloseInactiveClusters` then `RunRetention` | 24 |
| `retention.*` | `rejected_articles_days`, `duplicates_days`, `audit_events_days`, `publication_attempts_days`, `clusters_days` (see [DATABASE.md](DATABASE.md#retention)) | 14, 7, 30, 30, 14 |
| `cluster_lifecycle.*` | `close_after_hours`, `batch_size`, `max_batches_per_run`, `time_budget_seconds`, `close_reason` | 96, 200, 50, 30, `inactivity_ttl` |
| `seen_items.reclaim_after_minutes` | When an unfinished seen-items claim may be taken over | 10 |

### Fetching

| Key | Meaning |
|---|---|
| `http.timeout_seconds` | Timeout for feed fetches (RSS, Reddit, YouTube, Bluesky) |
| `http.max_response_bytes` | Body cap for feed fetches |
| `http.allow_private_hosts` | Let catalog feed URLs reach loopback and private networks; default false ([SECURITY.md](SECURITY.md#url-and-destination-validation)) |
| `fetch.global_concurrency` | Concurrent source fetches |
| `fetch.fan_in_buffer` | Buffer between fetchers and the cycle |
| `fetch.sources_path` | Source catalog (example: `configs/example/sources.yml`) |
| `fetch_sources.*` | Per category (`website`, `rss`, `reddit`, `bluesky`, `twitter`, `instagram`, `youtube`): `enabled` and `on_missing_dependency`. The library builds `rss`, `reddit`, `bluesky` and `youtube`; the others need a host builder ([ARCHITECTURE.md](ARCHITECTURE.md#composition)) |
| `scraping_service.*` | Settings for a host-provided scraper (`enabled`, `base_url`, `timeout_seconds`, `max_retries`, `backoff_base_ms`, `backoff_max_ms`, `response_limit_bytes`, `default_mode`, `default_cache_ttl_seconds`, `concurrency`, `circuit_breaker`, `domains`). The library validates them and passes the configuration to the host's `Scraper` factory; it ships no client |
| `source_policy.path` | Per-source dedup and publication policies (example: `configs/example/source-policies.yml`) |

### Normalization, dedup and relevance

| Key | Meaning |
|---|---|
| `normalization.url` | Tracking parameters to drop, per-domain ignored parameters, default ports, trailing slash policy, known link resolvers |
| `normalization.text` | Emoji, hashtag, mention and inline URL stripping, repeated prefixes, boilerplate phrases, shingle size, minimum token length, stop words |
| `deduplication.near_duplicate` | `enabled`, `method` (`jaccard` or `dice`), `default_threshold`, `threshold_by_source_type`, `short_text_max_tokens`, `short_text_threshold`, `window_hours`, `max_candidates`, `min_shared_terms`, `fields_used`, `content_type_exceptions`, `attribute_combo_window_minutes`, `rules_version` ([DEDUPLICATION.md](DEDUPLICATION.md)) |
| `classification.enabled` | Turns the relevance gate on |
| `classification.trusted_source_ids` | Sources that skip the relevance gate |
| `classification.anchor` | `enabled`, `categories`, `multi_word_categories`, `min_confidence`: an untrusted article must have positive evidence from an anchor category (team names, staff, players by full name, hashtags) to count as related; `multi_word_categories` ignore bare single-word entries such as surnames |
| `deduplication.story_guard` | `enabled`, `window_hours`, `max_per_story`, `min_shared_tokens`, `overlap_threshold`, `verbatim_overlap_threshold`: withholds (`retained_no_publish`, reason `story_repeat_limit`) an article once its story has `max_per_story` published or queued messages, or at once when another source already carries a title overlapping it by `verbatim_overlap_threshold` (0 disables); official and model-bypass sources are exempt |
| `classification.context_source_ids` | Sources whose scope disambiguates mentions; still scored |
| `dictionary.path` | Relevance dictionary; required when classification is on |
| `dictionary.version` | Optional pin; start-up fails on a mismatch |

### Clustering

| Key | Meaning |
|---|---|
| `clustering.enabled` | Turns event clustering on |
| `clustering.event_threshold` | Join threshold, between 0 and 1 |
| `clustering.rules_path` | Clustering rules (example: `configs/example/event_clustering_rules.yml`) |
| `clustering.rules_version` | Optional pin |
| `clustering.generated_rules_path` | Optional file of approved generated phrases merged into the rules; empty means hand-written rules only |
| `clustering.candidate_limit` | Maximum clusters compared per article |
| `clustering.lexical_join` | `enabled`, `window_hours`, `min_shared_tokens`, `overlap_threshold`, `generic_token_share`: an article event signals could not place joins the open cluster of a recent article with overlapping distinctive title words, as role `related_coverage` (subject to the one-initial-publication gate: only an official source's first voice on the cluster is re-admitted, a rewording from any other source is `cluster_gate_denied_duplicate`) |
| `clustering.publish_ambiguous` | When true, an article whose join-vs-create decision is ambiguous is published as a new cluster instead of `retained_no_publish` (`cluster_ambiguous`) |
| `clustering.entity_catalog` | `manual_path`, `generated_path`, and `league` (`mode` `off` or `mentions_only`, `roster_path`, `rules_path`, `max_age_days`) |

### Editorial

| Key | Meaning |
|---|---|
| `editorial.mode` | `disabled`, `shadow`, `canary` or `enforce`; required ([CLASSIFIER.md](CLASSIFIER.md#modes)) |
| `editorial.ambiguous_policy` | `reject` or `accept`; required in every mode |
| `editorial.veto_path` | Veto rules; empty means no vetoes |
| `editorial.model_bypass_source_ids` | Sources the model and vetoes never reject |
| `editorial.unconditional_publish_source_ids` | Also exempt from freshness rejection |
| `model.path` | Model directory; required unless the mode is `disabled` |
| `model.version` | Optional pin |
| `editorial_gates.*` | Promotion floor (tools): `min_precision`, `max_recall_regression`, `min_regression_baseline_examples`, `min_prauc`, `max_false_positive_rate`, `min_per_source_precision`, `min_source_examples`, `excluded_sources`, `max_artifact_size_bytes`, `max_inference_nanos` |
| `editorial_costs.*` | `false_publish_cost`, `missed_news_cost` for threshold selection (tools) |
| `editorial_rollout.*` | Staged rollout criteria read by `editorial canary status` (tools) |
| `editorial_cycle.*` | `sample_size`, `pool_limit`, `ambiguous_cooldown_days` for the interactive review cycle (tools) |

### Publication

| Key | Meaning |
|---|---|
| `freshness.*` | Maximum age in hours: `hard_news_hours`, `social_hours`, `analysis_hours`, `default_hours` |
| `freshness.live_updates` | `max_age_minutes`, case-insensitive `patterns` and `exclude_patterns`: a social post that reads as news from a game in progress (running score, kickoff, halftime, turnover) expires after `max_age_minutes` instead of `social_hours`; a post matching an exclude pattern (final score, recap) is finished-game news and keeps the normal window; 0 disables |
| `telegram.channel_id` | Destination channel |
| `telegram.timeout_seconds` | Bot API request timeout |
| `telegram.session_path` | Session file for a host history client |
| `publication.max_attempts` | Send attempts before discarding |
| `publication.base_backoff_seconds`, `publication.max_backoff_seconds` | Exponential retry backoff |
| `publication.claim_batch_size` | Outbox rows leased per pass |
| `publication.stale_claim_after_minutes` | When an abandoned lease is reclaimed |
| `publication.reconciliation` | `enabled`, `channel_history_limit`, `clock_skew_seconds` (at least 60), `batch_size` ([ADR 0030](adr/0030-outbox-delivery-guarantee.md)) |
| `publication.backlog` | Warning and critical levels for pending count, age and p95 lag |
| `publication_policy.burst` | `max_per_window` posts per `window_minutes` |
| `publication_policy.same_topic` | Same limit per topic, with `exempt_entities` |
| `publication_policy.quiet_hours` | `enabled`, `start_hour`, `end_hour` |
| `publication_policy.live` | A separate lane for match milestones: `enabled`, `ttl_minutes`, `source_allowlist`, `milestones`, and its own `burst`, `same_topic`, `quiet_hours` |
| `espn_scoreboard.*` | Match-state source for the live lane: `enabled` (false in the example), `team_abbreviation`, `request_timeout_seconds`, `user_agent`. The endpoint is undocumented ([NOTICE](../NOTICE)); a failed poll means articles use the normal lane |

### Entities, knowledge and LLMs (tools unless noted)

| Key | Meaning |
|---|---|
| `entity_extraction.fallback` | Service: optional LLM fallback for ambiguous entity mentions: `enabled`, `provider`, `base_url`, `model`, `temperature`, `timeout_seconds`, `max_tokens`, `min_confidence`, `max_candidates_per_article` |
| `entity_discovery.enabled`, `entity_discovery.extractor_version`, `entity_discovery.batch_size`, `entity_discovery.limit` | `entities discover` |
| `entity_discovery.llm` | OpenAI-compatible endpoint for discovery |
| `entity_discovery.wikidata` | Wikidata client: endpoint, language, limits, cache TTLs, `user_agent` (must contain a URL or e-mail), `properties`, `allow_private_hosts` |
| `entity_discovery.roster` | Local roster file, its maximum age and download URL (`entities roster sync`) |
| `entity_discovery.league` | League alias rules and the gated LLM chooser |
| `entity_discovery.verification` | `wikidata_first`, `fallback` |
| `catalog_maintenance.web_lookup` | Web search for entity verification: provider (SearXNG), endpoint, budgets, rate limits, cache TTL, a fallback provider, trusted domains |
| `offline_training.*` | `llm` (teacher model), `promotion`, `retrain_triggers`, `mining`, `model_training`, `review_scoring` |
| `dictionary_mining.path` | Noise policy for dictionary candidate mining; a term rejected in `dictionary mine-candidates` is appended to its `ignored_terms` |
| `dictionary_mining.english_words_path` | Optional English word list (one word per line, e.g. `/usr/share/dict/words`). With it, mining also proposes lower-case coined words (nicknames, slang) and skips every English word; without it only proper nouns are mined. People already in the nflverse roster (`clustering.entity_catalog.league.roster_path`) are never proposed |
| `dictionary_update.*` | Dictionary refresh from host-chosen pages: `enabled`, `transport`, `allowed_domains`, `sources`, `deactivation_policy`, `plausibility`. Fetching needs a host scraper |
| `remote.*` | `sqlite_remote_db_hint`, `retention_days` for host remote operations |
