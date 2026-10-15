# ADR 0001: Ports and adapters

## Status

Accepted

## Context

The engine makes many decisions (deduplication, relevance, clustering,
editorial scoring, freshness, publication policy) and talks to many outside
systems (feeds, APIs, SQLite, a messaging channel, LLM and search services).
The decisions must be testable without any of those systems, and the
outside systems must be replaceable, including by a host program that adds
its own sources.

## Decision

The code is split into layers with dependencies pointing inward:

- `domain`: pure rules and value objects. No I/O and no imports from the
  rest of the module.
- `ports`: interfaces. `ports/in` for what drives the application (process
  a cycle, publish the outbox, close clusters, run retention); `ports/out`
  for what the application needs (repositories, fetchers, publisher,
  scraper, clients).
- `app`: use cases and workflows that orchestrate domain rules through
  ports. No adapter imports.
- `adapters` and `infra`: implementations of the ports and technical
  concerns (configuration, SQLite, HTTP hardening, logging).
- `engine` and `cli`: composition only.

Outcomes an adapter must report to a use case (a permanent failure, a rate
limit, an unknown outcome) are types in `ports/out`, so use cases never
import adapters.

## Consequences

- Domain rules are unit-tested with no database or network.
- A host extends the engine by implementing ports (`out.Scraper`,
  `out.TelegramChannelImporter`, remote operations) or registering fetch
  category builders, without touching use cases.
- More packages and interfaces than a flat layout. Accepted for the
  testability it buys.
