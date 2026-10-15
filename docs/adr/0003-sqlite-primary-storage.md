# ADR 0003: SQLite as the only datastore

## Status

Accepted

## Context

The engine is meant to run as a single process on modest hardware, such as
a single-board computer, with no database server to operate. Its data
volume (recent articles, audit events, clusters, an outbox) fits easily in
one file.

## Decision

SQLite is the only datastore, through `modernc.org/sqlite`, a driver that
needs no cgo. The connection uses WAL journaling, `synchronous=NORMAL`,
foreign keys and a configurable busy timeout, with a single open
connection. Migrations are plain SQL files embedded in the module and
applied in name order by a small migrator that records each file in
`schema_migrations`, one transaction per file. Migrations are forward-only.

## Consequences

- No second datastore to deploy, back up or keep in sync.
- One writer: the pipeline processes articles sequentially, and the atomic
  claim and reservation operations rely on that serialisation.
- Backups must use SQLite's online backup, not a file copy of a live
  database (see [DATABASE.md](../DATABASE.md)).
- Feeding an external consumer, if ever needed, is a separate adapter or
  export, not a second primary store.
