# ADR 0035: Bluesky publisher (write client and reconciliation)

## Status

Accepted

## Context

The Bluesky adapter only read public feeds (a hand-written XRPC client, no
`indigo` dependency). The multi-destination outbox
([ADR 0031](0031-multi-destination-outbox.md)) and the payload-aware
publisher port ([ADR 0032](0032-message-payload.md)) let a host enqueue
Bluesky rows with their own text and images, but no publisher could send
them, and an ambiguous send ([ADR 0030](0030-outbox-delivery-guarantee.md))
to Bluesky could only be quarantined.

## Decision

**Publisher.** `bluesky.NewPublisher(Config)` returns a publisher for one
account that implements `out.PayloadPublisher` and
`out.DestinationReconciler`. A host registers it for the `bluesky`
destination (`engine.Options.Publishers`). Handle, app password, PDS host,
media root and timeout are constructor arguments; the adapter reads nothing
from the environment and never puts the app password or a token in an error
or log line. It uses `infra/netguard` (allow-private policy, redirect check,
bounded response bodies, default 30 s timeout, 1 MiB body cap).

**Session.** `createSession` logs in with the handle and app password. The
session is kept under a mutex; the access token is renewed with
`refreshSession` 2 minutes before its `exp`, and a failed refresh falls back to
a new login. A reply that says the token is expired renews the session and
repeats the call once (the server rejected it, so nothing was processed). A
rejected login is a temporary error, not a permanent one: bad credentials are
the operator's to fix and must not discard queued posts.

**Post.** `createRecord` writes an `app.bsky.feed.post` with `text`,
`createdAt` (UTC, milliseconds), `langs: ["es"]`, facets and, with images, an
`app.bsky.embed.images` embed carrying each image's alt text and aspect ratio.
Text comes from the payload; `payload.link` is appended on its own paragraph
when the text does not already contain it. An empty payload posts the article
title and canonical URL. Link facets (trailing punctuation excluded) and
hashtag facets are built with UTF-8 byte offsets.

**Limits, all `PermanentError` and checked before any network call.** 300
graphemes (counted by an in-package segmenter covering combining marks,
variation selectors, skin tones, ZWJ sequences, flags, keycaps and tag
sequences, since the module carries no segmentation dependency) and 3000
bytes of text; at most 4 images; alt text at most 2000 graphemes. Text is
never truncated, so an article-derived post whose title and URL exceed 300
graphemes fails: hosts that need long headlines enqueue a payload.

**Images.** `payload.images[].path` is resolved inside `Config.MediaRoot` with
`os.Root`, so neither `..` nor a symlink can leave the store. The type must be
`image/jpeg`, `image/png` or `image/webp` and must match the file's content.
A file over Bluesky's 1,000,000-byte limit is decoded (JPEG or PNG, with a
pixel cap), flattened onto white, and re-encoded as JPEG at decreasing quality
and then decreasing size until it fits; a webp that is too large, or an image
that cannot fit above 256 px, is a `PermanentError`.

**Error mapping.** HTTP 429 is `RateLimitedError`, with `RetryAfter` from
`RateLimit-Reset` (a Unix timestamp), then `Retry-After`, else 60 s, capped at
1 h. Any other 4xx is `PermanentError`. A 5xx with an API error body is a
temporary error. Only `createRecord` can be ambiguous: once its request is
sent, a timeout, dropped connection, unreadable reply, success without a URI,
or a 5xx with no API error body (a gateway) is `AmbiguousError`. A failure at
login or `uploadBlob` posted nothing and is never ambiguous. The message ID is
the post's `at://` URI.

**Reconciliation.** `out.DestinationReconciler.FindSent` is the optional
extension that lets `PublishOutbox.Reconcile` resolve a `sent_unconfirmed`
row with no recorded message ID for a non-Telegram destination: the publisher
rebuilds the post text and reads the account's `getAuthorFeed` (100 newest
posts, the account's own only). `outboxreconcile.MatchPost` accepts a post with
identical text and a `createdAt` no earlier than `SentAt` minus the clock
skew. A match finalises the row with the post URI; a feed that reaches back past
that cutoff with no match makes it retryable; an empty or too shallow feed, a
lookup error or an unreadable payload quarantines it. A destination whose
publisher has no reconciler keeps the old behaviour (quarantine).

## Consequences

- A Bluesky send is at most once per row unless reconciliation produces a
  false negative: `getAuthorFeed` is served by the AppView, which indexes a new
  post after a short delay, so a lookup run immediately after the send can miss
  a post that exists. Reconciliation runs on later passes, which makes this
  rare; reading the repo directly with `com.atproto.repo.listRecords` would
  remove the lag and is the alternative if it is observed.
- A busy account can push the send out of the 100-post window; the row is then
  quarantined, not resent.
- Two rows with identical text inside the window cannot be told apart.
- The grapheme counter is an approximation of Unicode extended grapheme
  clusters (no Hangul jamo sequences, no Indic conjuncts). It is exact for
  the text this bot posts; the 3000-byte check and the server's own limit stay
  as the backstop.
- Images are read into memory (32 MiB cap) and re-encoding is lossy; hosts
  should store images already under 1 MB.
