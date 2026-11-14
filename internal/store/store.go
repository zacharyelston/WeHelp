package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/pgurl"
)

// NewPool opens a pgx connection pool and verifies connectivity.
//
// Migrations are Flyway's job (db/migrations/, `wehelpd migrate`,
// `make db-migrate`, or the compose `migrate` service) — the server never
// applies them itself.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pool ping: %w", err)
	}
	return pool, nil
}

// JDBCURL converts a postgres:// DSN into the JDBC URL Flyway expects.
func JDBCURL(databaseURL string) (string, error) { return pgurl.JDBC(databaseURL) }
