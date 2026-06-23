BEGIN;

DROP INDEX IF EXISTS github_mention_deliveries_request_idx;
DROP TABLE IF EXISTS github_mention_deliveries;

DROP INDEX IF EXISTS github_mention_threads_thread_idx;
DROP TABLE IF EXISTS github_mention_threads;

COMMIT;
