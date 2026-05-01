// Package db provides a Postgres connection pool and access to sqlc-generated
// queries. Wire it up by calling Open with a DATABASE_URL and passing the
// returned *Store wherever query access is needed.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

var errEmptyURL = errors.New("database url is empty")

// Store bundles the pgx pool with sqlc's Queries so callers can both run
// generated queries and start transactions on the same pool.
type Store struct {
	Pool    *pgxpool.Pool
	Queries *sqlc.Queries
}

// Open parses the URL, opens a pgx pool, and pings to surface bad creds early.
// Supabase note: when pointing at the transaction pooler (port 6543) you must
// disable prepared statements via the connection string, e.g. append
// `?default_query_exec_mode=exec`. Direct (5432) and session pooler connections
// support prepared statements normally.
func Open(ctx context.Context, url string) (*Store, error) {
	if url == "" {
		return nil, errEmptyURL
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{Pool: pool, Queries: sqlc.New(pool)}, nil
}

// Close releases pool resources. Safe to call on a nil Store.
func (s *Store) Close() {
	if s == nil || s.Pool == nil {
		return
	}
	s.Pool.Close()
}
