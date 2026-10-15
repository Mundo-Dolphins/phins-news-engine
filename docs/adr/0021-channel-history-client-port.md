# ADR 0021: Channel-history client port

## Status

Accepted

## Context

The Telegram Bot API cannot read the history of the channel a bot posts to.
Two features need that history: checking whether a send whose outcome is
unknown actually arrived ([ADR 0030](0030-outbox-delivery-guarantee.md)),
and importing past posts for analysis. Reading history requires a
different kind of client with its own credentials and session handling,
which is a deployment choice of the operator.

## Decision

The library defines the port and ships no implementation:

```go
type TelegramChannelImporter interface {
	ExportChannelMessages(ctx context.Context, req TelegramImportRequest) ([]telegramimport.Message, error)
}
```

`out.TelegramImportRequest` names the channel, a message limit and whether
to reset the session. The parsing and matching logic that works on the
returned messages (`domain/telegramimport`, `app/telegramimport`,
`app/outboxreconcile`) is part of the library.

A host provides the history client in two places:

- `engine.Options.ChannelImporter` for the service; it is built only when
  `publication.reconciliation.enabled` is true. A client built for the
  long-running service must never prompt for input; a host that builds it
  from `out.TelegramImportClientConfig` sets `NonInteractive`.
- `shared.SetTelegramImportClient` for host tool commands.

Credentials for such a client come only from environment variables
(`TELEGRAM_API_ID`, `TELEGRAM_API_HASH`, `TELEGRAM_PHONE`) and the session
file path from `telegram.session_path`.

## Consequences

- Without a history client, reconciliation cannot confirm anything: every
  send with an unknown outcome is quarantined for an explicit decision.
- Matching is a client-side comparison of canonical URLs over a bounded page
  of recent history, so a link whose shape drifted can be missed.
- The library carries no client library for user sessions.
