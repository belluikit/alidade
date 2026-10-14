-- Not under any sqlc configuration: tables here are still declared, but
-- nothing generates Go for them.
CREATE TABLE seed_log (
    ran_at TIMESTAMPTZ NOT NULL
);
INSERT INTO users (email) VALUES ('seed@example.com');
