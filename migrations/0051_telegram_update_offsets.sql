-- migrations/0051_telegram_update_offsets.sql
--
-- Next getUpdates offset per bot, so a restart does not re-read (or lose)
-- updates. bot is the numeric id of the bot token (the part before ':'),
-- never the token itself.
--
-- Rollback (operational -- this codebase has no down-migrations): purely
-- additive. To revert: `DROP TABLE telegram_update_offsets;` and delete
-- this migration's row from schema_migrations to let it reapply.

CREATE TABLE telegram_update_offsets (
    bot         TEXT PRIMARY KEY,
    next_offset INTEGER NOT NULL,
    updated_at  TIMESTAMP NOT NULL
);
