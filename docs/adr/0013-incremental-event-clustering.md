# ADR 0013: Incremental event clustering

## Status

Accepted

## Context

After deduplication, many articles are still about the same event in
different words: a signing reported by five outlets, an injury update
followed by a confirmation. The channel should carry one initial post per
event, with room for genuine updates, and every grouping decision must be
explainable and cheap.

## Decision

- **Same event, not same text.** `eventsignals.Extract` derives a small
  representation from the title and summary: actions, entities, event type,
  opponent, content cues and approximate clustering keys. It uses
  word-boundary phrase matching over a versioned rules file; no LLM or
  embedding service is involved.
- **Bounded candidates.** Up to five keys per article, from strongest
  (event type, action, entity, opponent, day) to weakest (primary entity),
  are indexed in `event_keys`. Candidates come from one indexed query
  bounded by the widest event window and `clustering.candidate_limit`. The
  articles table is never scanned.
- **Explainable decision.** `clustering.DecideJoin` compares weighted
  signals behind a hard conflict check and returns join, create or
  ambiguous, with evidence strings that are stored. A dedup verdict can be
  passed in as extra evidence but never forces the decision.
- **Ambiguity is withheld, not dropped.** An ambiguous decision opens a new
  cluster, is audited with its alternatives, and the article ends as
  `retained_no_publish` (`cluster_ambiguous`).
- **Roles and representative.** Each member gets a role (first report,
  duplicate, confirmation, substantial update, correction, analysis,
  reaction, primary source), and the cluster keeps a representative chosen
  by an explainable weighted ranking, with its history.
- **Lifecycle.** Inactive clusters are closed in bounded batches and purged
  later by retention.

The cluster publication gate then allows at most one initial publication
per cluster (see [DEDUPLICATION.md](../DEDUPLICATION.md)).

## Consequences

- Decisions are reproducible from their inputs and auditable.
- Quality depends on the rules file; `phins-news-engine-tools clustering evaluate`
  measures it against a labelled set.
- An event split across two clusters can be published twice; over-merging
  is the opposite risk.
