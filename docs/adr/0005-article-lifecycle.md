# ADR 0005: Article lifecycle as one status plus an event history

## Status

Accepted

## Context

An article moves through validation, deduplication, relevance, vetoes,
clustering, source policy, editorial scoring, freshness and publication.
Operators need to know both where an article is now and why every
transition happened, with the versions, scores and thresholds involved.

## Decision

`article.Status` is a single enum stored in an indexed, `CHECK`-constrained
column on `articles`, with a short `status_reason` next to it. Every
transition is also appended to `article_processing_events`: event type,
from and to status, reason code, free-text detail, mode, model or
dictionary version where relevant, and the cycle ID.

There are 16 states; their meaning is in
[PIPELINE.md](../PIPELINE.md#article-states). `retained_no_publish` is the
terminal state for a valid, clustered article that the engine deliberately
keeps out of the outbox (an ambiguous cluster decision, source policy, or
the cluster publication gate).

`article.IsReprocessable` defines which terminal states may be evaluated
again when a source edits an item: only states that never led, and were
never meant to lead, to an outbound effect.

## Consequences

- "What is true now" is one column; "what happened" is one table.
- A new state is an enum value plus a `CHECK` constraint change in a new
  migration.
- The event table grows with every decision and is pruned by retention.
