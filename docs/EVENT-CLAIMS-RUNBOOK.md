# Event claims runbook

Operator guide for the `event claims` commands of the tools binary
(`phins-news-engine-tools event claims ...`, or the name your host gives it).
They inspect and repair the claims described in
[ADR 0034](adr/0034-event-claims.md). A claim that ends `failed` keeps its key,
so the slot (for example "one injury report per day") stays burned until you
retry or release it.

All commands read the database from `--config`, like the other tools commands.

| Command | What it does |
| --- | --- |
| `event claims list [--handler H] [--state S] [--since D]` | One line per claim: handler, key, state, article, outbox status per destination, data summary and the last outbox error. `--state` is `claimed`, `published` or `failed`; `--since` is a duration (`24h`) or a date (`2026-11-01`). |
| `event claims show <handler> <key>` | The claim as JSON, with its full handler data and outbox rows. |
| `event claims retry <handler> <key> [--dry-run]` | For `failed` claims only. Puts the claim's dead outbox rows (same destination and payload) back to `pending` with attempts reset and no expiry, and sets the claim to `claimed`. |
| `event claims release <handler> <key> --yes` | Deletes a `claimed` or `failed` claim so the handler can claim the key again with a corrected post. Without `--yes` nothing happens. The operator name and time are logged and printed. |

## Refusals

`retry` refuses, changing nothing, when the claim:

- is `published` or `claimed` (only `failed` claims are retried);
- has an outbox row that is `pending`, `leased`, `retryable`, `sent_unconfirmed`
  or `quarantined`. Let it finish, or resolve ambiguous rows with the Telegram
  outbox repair command first;
- has no dead outbox row, for example because its action was never persisted.
  Release the claim instead.

`release` refuses a `published` claim, because that would allow a duplicate
post.

## Typical flows

1. A Bluesky post failed. `event claims list --state failed` shows the claim
   and the last error. Fix the cause (credentials, media), check with
   `event claims retry <handler> <key> --dry-run`, then run it without
   `--dry-run`. The next outbox run posts it; the claim settles to `published`
   or `failed` again.
2. The report was wrong and a corrected post is needed. Run
   `event claims release <handler> <key> --yes`. The handler can claim the key
   on the next matching article.

Delivery is per article: a retry re-enqueues only the dead destinations, rows
already delivered are left alone.
