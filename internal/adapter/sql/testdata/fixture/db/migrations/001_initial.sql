-- +goose Up
CREATE SCHEMA app;

CREATE TABLE users (
    id          BIGSERIAL   PRIMARY KEY,
    email       TEXT        NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata    JSONB       NOT NULL DEFAULT '{}'  -- a trailing comment, with a (paren)
);

CREATE TABLE IF NOT EXISTS api_keys (
    user_id     BIGINT NOT NULL REFERENCES users(id),
    "key"       TEXT   NOT NULL,
    expires_at  TIMESTAMPTZ,
    PRIMARY KEY (user_id, "key"),
    CONSTRAINT api_keys_key_len CHECK (length("key") > 8)
);

CREATE TABLE app.sessions (
    token    TEXT PRIMARY KEY,
    user_id  BIGINT NOT NULL,
    /* a block comment, with CREATE TABLE inside it */
    ttl      INTERVAL
);

CREATE INDEX users_email_idx ON users (email);

-- +goose Down
DROP TABLE app.sessions;
DROP TABLE IF EXISTS api_keys;
DROP TABLE users;
