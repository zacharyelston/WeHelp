package store

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
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

// JDBCURL converts a postgres:// DSN into the JDBC URL Flyway expects,
// carrying user/password as query params. DSNs must be in URL form.
func JDBCURL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("database_url must be a postgres:// URL: %w", err)
	}
	q := u.Query()
	if u.User != nil {
		q.Set("user", u.User.Username())
		if pw, ok := u.User.Password(); ok {
			q.Set("password", pw)
		}
	}
	return "jdbc:postgresql://" + u.Host + u.Path + "?" + q.Encode(), nil
}
