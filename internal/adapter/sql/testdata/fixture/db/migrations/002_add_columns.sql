-- +goose Up
ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN deleted_at;
