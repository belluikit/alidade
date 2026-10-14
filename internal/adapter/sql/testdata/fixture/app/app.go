// Package app is the hand-written caller of the generated queries.
package app

import (
	"context"

	"example.com/fixture/internal/db"
)

// Lookup reaches the users table through GetUser.
func Lookup(ctx context.Context, q *db.Queries, id int64) (db.User, error) {
	return q.GetUser(ctx, id)
}
