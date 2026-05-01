-- Example scaffolding — delete this migration once you add a real schema.
CREATE TABLE health_checks (
    id          BIGSERIAL PRIMARY KEY,
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    note        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX health_checks_checked_at_idx ON health_checks (checked_at DESC);
