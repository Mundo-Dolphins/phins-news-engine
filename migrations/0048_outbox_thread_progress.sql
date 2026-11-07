-- migrations/0048_outbox_thread_progress.sql
--
-- Bluesky threads: the already-posted parts of a thread (JSON list of
-- {uri, cid}) are persisted per outbox row so a retry resumes the thread
-- instead of reposting it.

ALTER TABLE publication_outbox ADD COLUMN thread_progress TEXT NOT NULL DEFAULT '[]';
