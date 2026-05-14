// Package db provides a Postgres connection pool and access to sqlc-generated
// queries. Wire it up by calling Open with a DATABASE_URL and passing the
// returned *Store wherever query access is needed.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

var errEmptyURL = errors.New("database url is empty")

// Store wraps the pgx pool and exposes sqlc's generated Queries. Callers
// should go through Queries for single-statement reads and writes, and
// WithTx for anything that needs to be atomic. The underlying pool is kept
// unexported so callers can't bypass the type-safe layer with raw SQL.
type Store struct {
	pool    *pgxpool.Pool
	Queries *sqlc.Queries
}

// Open parses the URL, opens a pgx pool, and pings to surface bad creds early.
// Supabase note: when pointing at the transaction pooler (port 6543) you must
// disable prepared statements via the connection string, e.g. append
// `?default_query_exec_mode=exec`. Direct (5432) and session pooler connections
// support prepared statements normally.
//
// maxConns sets pgxpool's MaxConns. Zero leaves the pgx default in place
// (max(4, runtime.NumCPU())), which is fine for tests and for boxes
// where the connection string already pins pool_max_conns. Production
// callers should pass an explicit value tuned to Postgres
// max_connections / number of replicas.
func Open(ctx context.Context, url string, maxConns int32) (*Store, error) {
	if url == "" {
		return nil, errEmptyURL
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool, Queries: sqlc.New(pool)}, nil
}

// Close releases pool resources. Safe to call on a nil Store.
func (s *Store) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// WithTx runs fn inside a transaction, passing it a *sqlc.Queries scoped to
// that transaction. The transaction is committed if fn returns nil and rolled
// back otherwise. Errors from Begin / Commit / Rollback are wrapped.
func (s *Store) WithTx(ctx context.Context, fn func(*sqlc.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
