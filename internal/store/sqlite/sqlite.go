// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/migrate"
	"github.com/chan-labs/quizzotic/migrations"
)

// execer is the part of *sql.DB and *sql.Tx that repositories use.
type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Store is the SQLite implementation of store.Store.
type Store struct {
	db *sql.DB
	q  platform.TracedExecer // db, or the open transaction, traced
}

var _ store.Store = (*Store)(nil)

// DSN builds the connection string for a database file: WAL, a 5 s
// busy_timeout, foreign keys on, and write transactions taken immediately so
// concurrent writers queue on busy_timeout instead of failing mid-transaction.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if needed) the SQLite file at path.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return &Store{db: db, q: platform.NewTracedExecer(db, "sqlite")}, nil
}

// DB exposes the pool for tests and the migration runner.
func (s *Store) DB() *sql.DB { return s.db }

// WithTx implements store.Store.
func (s *Store) WithTx(ctx context.Context, fn func(tx store.Store) error) error {
	if s.q.InTx() {
		return fn(s)
	}
	txCtx, span := s.q.TraceTx(ctx)
	tx, err := s.db.BeginTx(txCtx, nil)
	if err != nil {
		platform.EndSpan(span, err)
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			span.End()
			panic(p)
		}
	}()
	if err := fn(&Store{db: s.db, q: s.q.Tx(tx, span, ctx)}); err != nil {
		tx.Rollback()
		platform.EndSpan(span, err)
		return err
	}
	err = tx.Commit()
	platform.EndSpan(span, err)
	return err
}

// Migrate implements store.Store.
func (s *Store) Migrate(ctx context.Context) ([]int64, error) {
	return migrate.Up(ctx, s.db, migrations.FS, "sqlite")
}

// Close implements store.Store.
func (s *Store) Close() error { return s.db.Close() }

// Millis converts a time to the Unix-millisecond integer stored in SQLite.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// FromMillis is the inverse of Millis, returning UTC.
func FromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
