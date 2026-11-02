# ADR 0036: Media store (safe image download with retention)

## Status

Accepted

## Context

Event handlers ([ADR 0033](0033-event-handlers.md)) attach images to the
messages they enqueue ([ADR 0032](0032-message-payload.md)): a tweet's JPEG, the
featured image of an article. The image has to be downloaded from a URL the
engine does not control, kept until the outbox row is delivered (retries
included) and then removed. `EventMediaStore` existed as a port with no
implementation.

## Decision

**Adapter.** `adapters/outbound/mediafs.Store` implements
`out.EventMediaStore` (`Fetch(ctx, url) (publishing.ImageRef, error)`) and
`out.MediaSweeper` (`DeleteOlderThan`). The host-facing settings live under
`media` (`config.MediaConfig`); the store is built only when `media.dir` is
set, and then handlers receive it as `EventDeps.Media`. A host that builds its
own Bluesky publisher passes the same directory as the publisher's media root.

**Download safety.** Every request goes through `infra/netguard`: http(s) only,
no credentials in the URL, loopback/private/link-local/metadata addresses
refused (checked again at dial time), at most five redirects, no https to http
downgrade. On top of that:

- `media.allowed_hosts` is the only list of hosts images may come from. It is
  defined by the host (nothing is hard-coded), must not be empty, and is
  checked again on every redirect hop.
- The body is capped (`media.max_bytes`, default 10 MiB) both by
  `Content-Length` and while reading, and the whole download has a timeout
  (`media.timeout_seconds`, default 20 s).
- The type comes from the bytes (`http.DetectContentType`), never from the URL
  or `Content-Type`. Only JPEG, PNG and WebP are stored; HTML, video, GIF and
  anything else are rejected with `ErrNotImage`.
- Errors carry the redacted URL (query values masked).

**Naming.** A file is `<sha256 of the content>.<jpg|png|webp>` directly in the
store directory. The returned `ImageRef.Path` is that relative name, so it
survives restarts and identical images are shared. Files are written to a
temporary name and renamed, and all file access goes through `os.Root`, so a
name can never leave the directory. Fetching an image that is already stored
writes nothing and renews its modification time, which restarts its retention
clock for the new outbox row that will reference it.

**Retention.** `RunRetention` gets a media step when the engine wires
`SetMediaRetention`. Files whose modification time is older than
`media.retention_days` (default 7) are deleted, except files referenced by the
payload of an outbox row that is `pending`, `leased`, `retryable` or
`sent_unconfirmed` (`OpenPayloadRepository`). Once such a row is delivered,
dead, expired or quarantined, its images are deleted on the next run if they
are old enough. If the open rows cannot be listed or one of their payloads
cannot be decoded, nothing is deleted. A vanished file is not an error, and
leftover temp files from an interrupted download are removed by the same age
rule.

**Cap.** `media.retention_days` may not exceed `media.max_retention_days`
(default 7), so a configuration cannot silently keep images longer than a week;
raising the cap is an explicit host decision. The cleanup interval
(`scheduler.cleanup_interval_hours`, 24 by default) adds up to that much to
the effective age of a deleted file.

**Observability.** `Store.Stats()` exposes cumulative counters (downloads,
reused, rejects, bytes written, deleted). The same figures are logged on every
download, reject and sweep, and the retention summary line includes
`media_deleted` and `media_protected`.

## Consequences

- An image referenced by a row stuck in `sent_unconfirmed` or `retryable` is
  kept for as long as the row is; ExpireOutbox and the retry budget bound
  that. A `quarantined` row no longer protects its images, so repairing it to
  retry after the files expired fails with a permanent error.
- A handler that fetches an image and never enqueues a row loses the file
  after the retention window; nothing else tracks it.
- GIF and video are not supported; adding a type means adding it to the
  adapter's table and to what the destination accepts.
