# ADR 0009: Cascading deduplication

## Status

Accepted

## Context

The same story arrives many times: the same URL polled again, the same item
under another URL, syndicated copies with small edits, and different
articles about the same event. Each case needs a different, explainable
test, and none of them may scan the whole articles table.

## Decision

Deduplication is a cascade of cheap, deterministic checks, each stopping
at the first match and recording which signal matched:

1. **Stable identity.** An item already decided with unchanged content is
   skipped before any work (`seen_items`).
2. **Exact duplicates.** A SHA-256 fingerprint of the canonical URL, then
   alias signals in a fixed order: external ID, URL alias, content
   fingerprint, and an attribute combination (source, normalized title,
   rounded publication time), which is checked last because it trusts
   timestamps. Aggregator links are also checked against their origin URL.
3. **Near duplicates.** Candidates come from an inverted term index in SQL,
   bounded by a time window, a candidate limit and a minimum of shared
   terms. Title shingles are scored once with Jaccard or Dice against a
   threshold that can differ by source type and is stricter for short
   titles. Content types listed as exceptions turn a near match into a
   same-event hint instead of a rejection.

Same-event grouping is a separate concern, handled by clustering
([ADR 0013](0013-incremental-event-clustering.md)). A source policy in
`enforce` mode may retain a near duplicate instead of rejecting it, so an
official confirmation or update can still be published.

## Consequences

- Every rejection names its signal and, for near duplicates, its scores.
- Candidate generation is an indexed query, so cost does not grow with the
  size of the table.
- Thresholds need calibrating on the operator's data
  (`phins-news-engine-tools dedup evaluate`).
- Paraphrases with few shared words pass this layer by design.
