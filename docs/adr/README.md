# Architecture decision records

Short records of the decisions that shape the engine. Each one states the
context, the decision and its consequences as they apply to the library
today.

| ADR | Decision |
|---|---|
| [0001](0001-hexagonal-architecture.md) | Ports and adapters |
| [0003](0003-sqlite-primary-storage.md) | SQLite as the only datastore |
| [0005](0005-article-lifecycle.md) | Article lifecycle: one status plus an event history |
| [0007](0007-transactional-outbox.md) | Transactional outbox for publication |
| [0009](0009-cascading-deduplication.md) | Cascading deduplication |
| [0011](0011-scraper-port.md) | The scraper port |
| [0013](0013-incremental-event-clustering.md) | Incremental event clustering |
| [0021](0021-channel-history-client-port.md) | Channel-history client port |
| [0030](0030-outbox-delivery-guarantee.md) | Outbox delivery guarantee for sends with an unknown outcome |

The numbering has gaps. The engine was extracted from a larger project
whose other records cover operations and deployment-specific choices and
are not published. Code comments that cite an ADR number missing from this
list refer to one of those records; the behaviour they describe is
documented in the code itself and in the guides under `docs`.
