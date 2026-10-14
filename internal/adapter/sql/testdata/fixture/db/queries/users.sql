-- name: GetUser :one
SELECT id, email, created_at, metadata, deleted_at
FROM users
WHERE id = $1;

-- name: ListUsers :many
SELECT id, email FROM users ORDER BY id;

-- name: CreateUser :exec
INSERT INTO users (email) VALUES ($1);

-- name: ListUserKeys :many
-- Keys of a user, with the owner's email.
SELECT k.user_id, k."key", u.email
FROM api_keys AS k
JOIN users u ON u.id = k.user_id
WHERE k.user_id = $1;

-- name: DeleteSession :execrows
DELETE FROM app.sessions WHERE token = $1;

-- name: RecentUsers :many
-- A CTE named like a table is not that table; api_keys is read.
WITH users AS (SELECT user_id AS id FROM api_keys WHERE expires_at > now())
SELECT id FROM users;

-- name: UsersWithKeys :many
SELECT id, email FROM users WHERE id IN (SELECT user_id FROM api_keys) LIMIT sqlc.arg('n');

-- name: TouchUser :exec
UPDATE users SET email = sqlc.arg(email) FROM app.sessions s WHERE s.user_id = users.id AND s.token = @token;

-- name: CopyUsers :exec
INSERT INTO seed_log (ran_at) SELECT created_at FROM users;

-- name: Broken :exec
-- The parser rejects this; the token match stands in and names users.
SELEC id FROM users WHERE;

-- name: KeysWithOwner :many
-- Qualified through aliases; an unqualified name unique to one table.
SELECT k."key", u.email, label
FROM api_keys AS k JOIN users u ON u.id = k.user_id
WHERE expires_at > now();

-- name: SessionsAndKeys :many
-- user_id is a column of both tables in scope: ambiguous, no edge.
SELECT token FROM app.sessions s JOIN api_keys k ON true WHERE user_id = $1;

-- name: AllKeys :many
SELECT * FROM api_keys;
