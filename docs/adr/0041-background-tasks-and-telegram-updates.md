# ADR 0041: Background tasks and Telegram updates

## Status

Accepted

## Context

Event handlers ([ADR 0033](0033-event-handlers.md),
[ADR 0038](0038-timed-event-handlers.md)) run once per event, inside a
bounded call. Some work does not fit that shape: it needs a long LLM
exchange, then a human approval (a reply or a button press in Telegram), then
an action, with minutes or hours between steps. The host also needs the full
text of some RSS items, which the pipeline never kept.

## Decision

**Tasks.** A handler returns the `out.EnqueueTask` action with a kind, a key
and an initial state. The pair (kind, key) is unique, so enqueueing the same
work twice is a no-op. Migration 0050 adds `tasks` (status `pending`,
`waiting`, `done`, `failed`; opaque JSON `state`; `attempts`; `run_after`)
and `task_signals` (external input attached to a task).

**Runners.** The host registers `out.TaskRunner` values in
`Options.TaskRunners`. Every `scheduler.task_interval_seconds` the task loop
asks `out.TaskRepository.Due(ctx, now, kinds, limit)` for tasks whose
`run_after` has passed, or that have unconsumed signals while `attempts` is 0,
and calls the runner with a `TaskContext`. The runner answers with a
`TaskOutcome`: new state, next `run_after`, done or failed, and the signals
it handled in `Consumed`. A runner that does not list a signal leaves it for
the next run. `Options.TaskTimeout` bounds one call. A failed call retries
with backoff 30s * 2^(n-1) capped at 1h, and alerts the operator at 3
attempts. A runner whose Kind equals an EventHandler name shares its
`events.<name>` configuration block; otherwise `events.<kind>.enabled` turns
it on.

**Single writer.** Only the task runner writes `tasks` rows. Producers of
external input insert `task_signals` rows only, so the two never contend on a
table.

**Telegram updates.** `telegram.BotClient` sends, force-replies, edits,
answers callbacks and calls `getUpdates`. With `Options.TelegramUpdates` set,
`telegramupdates.Poller` long-polls `getUpdates`, drops every update whose
sender is not `AllowedUserID` or whose chat is not `AllowedChatID`, and asks
the host's `out.TelegramUpdateRouter` which task an update belongs to. A
claimed update becomes a signal on that task. The next offset is stored per
bot in `telegram_update_offsets` (migration 0051), keyed by the numeric bot id
and never by the token, and advanced only after the signals are saved.

**RSS content.** A feed with `keep_content: true` in `sources.yml` stores the
item's full text (HTML stripped, capped at 64 KiB) in
`article.Metadata[article.MetadataFullContent]`. It is never copied to `Body`,
so classification, dedup and clustering see the same text as before.

**Graphemes.** `bluesky.CountGraphemes` is exported so hosts can size thread
parts with the same counter the publisher enforces.

## Consequences

- Telegram allows one `getUpdates` consumer per bot token. Two processes with
  the same token, or another poller on it, make each other fail with a
  conflict.
- Only one user and one chat are served. Other senders are ignored without a
  reply.
- Telegram messages are at-least-once: if saving the task state fails after a
  message was sent, the next attempt sends it again.
- Signals are durable, so a restart loses neither updates nor approvals.
- A runner must list handled signals in `Consumed`, or they wake it again.
