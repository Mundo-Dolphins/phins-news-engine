# ADR 0037: Operator alerts (handler and destination failures to Telegram)

## Status

Accepted

## Context

Some failures nobody else will notice: an event handler rejects a report
because its image could not be read ([ADR 0033](0033-event-handlers.md)), or a
Bluesky publication runs out of retries ([ADR 0031](0031-multi-destination-outbox.md)).
The operator is the only member of the news chat, so that chat is where the
notice goes.

## Decision

**Port.** `out.Alerter.Alert(ctx, out.Alert{Source, Severity, Title, Details,
DedupKey}) error`. Handlers keep the narrower `out.EventAlertSink`
(`Alert(ctx, handler, message)`); `usecase.EventAlertAdapter` turns it into an
`Alerter` call with the dedup key `event:<handler>:<start of message>`.

**Adapter.** `telegram.Alerter` reuses the `sendMessage` path (and its hardened
HTTP client) but sends plain text beginning `[alert] <severity> · <source>`,
so it cannot be mistaken for news and needs no HTML escaping. It is separate
from the outbox: an alert is never an article, is never enqueued, and is
invisible to deduplication and reconciliation.

**Rate limit.** An alert whose `DedupKey` was sent in the last
`alerts.dedup_hours` (default 24) is dropped. The window is held in memory, so
a restart may repeat an alert once. A failed delivery releases its key so the
next attempt can go out. An empty key is never deduplicated.

**Outbox hook.** `PublishOutbox` raises a critical alert when a non-Telegram
row becomes `dead` (permanent error or retries exhausted) or is quarantined by
reconciliation. It carries the destination, article, outbox id, last error and,
when event claims are wired, the claim `handler/key` of the article. The dedup
key is destination, kind and the start of the error, so a destination that
fails the same way for many articles produces one alert per window. Telegram
rows do not alert: Telegram is the channel the alert would travel on.

**Safety.** Text is scrubbed before it is sent: the bot token, extra configured
secrets, bot tokens and Bluesky app passwords by pattern, URL query values and
bearer credentials. Title and details are truncated (200 and 1500 characters).

**Failure.** Delivering an alert never fails the cycle: callers log the error
and carry on.

**Config.** `alerts.chat_id` overrides the destination chat, `alerts.dedup_hours`
the window, `alerts.disabled` turns alerts off.
