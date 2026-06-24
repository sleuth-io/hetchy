BEGIN;

DROP INDEX IF EXISTS github_mention_deliveries_request_idx;
DROP TABLE IF EXISTS github_mention_deliveries;

DROP TABLE IF EXISTS github_mention_threads;

COMMIT;
