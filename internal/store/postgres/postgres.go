// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

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

// rebinder lets repository SQL be written with ? placeholders (as in the
// SQLite backend) by rewriting them to $1, $2, ... before they reach pgx.
type rebinder struct{ q execer }

func (r rebinder) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return r.q.ExecContext(ctx, Rebind(q), args...)
}

func (r rebinder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return r.q.QueryContext(ctx, Rebind(q), args...)
}

func (r rebinder) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return r.q.QueryRowContext(ctx, Rebind(q), args...)
}

// Rebind rewrites each ? outside a quoted string to $n. Repository SQL
// contains no literal question marks other than placeholders.
func Rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n, inStr := 0, false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inStr = !inStr
			b.WriteByte(c)
		case c == '?' && !inStr:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Store is the Postgres implementation of store.Store.
type Store struct {
	db *sql.DB
	q  execer // db, or the open transaction (always behind a rebinder)
	tx *sql.Tx
}

var _ store.Store = (*Store)(nil)

// Open connects to the database named by a postgres:// or postgresql:// URL and verifies the
// connection. It does not migrate.
func Open(ctx context.Context, url string) (*Store, error) {
	db, err := openDB(url, nil)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		// The pgx error text can echo connection details; keep it generic.
		return nil, fmt.Errorf("open postgres: ping failed: %w", sanitize(err))
	}
	return &Store{db: db, q: rebinder{db}}, nil
}

// OpenSchema is Open with search_path pinned to schema, which must exist.
// The conformance tests use it to give every case an empty namespace.
func OpenSchema(ctx context.Context, url, schema string) (*Store, error) {
	db, err := openDB(url, map[string]string{"search_path": schema})
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open postgres: ping failed: %w", sanitize(err))
	}
	return &Store{db: db, q: rebinder{db}}, nil
}

func openDB(url string, params map[string]string) (*sql.DB, error) {
	cfg, err := pgxParse(url)
	if err != nil {
		return nil, err
	}
	for k, v := range params {
		cfg.RuntimeParams[k] = v
	}
	return stdlib.OpenDB(*cfg), nil
}

// sanitize drops everything but the SQLSTATE and message from a server error
// so a URL with a password can never leak through an error string.
func sanitize(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s (SQLSTATE %s)", pe.Message, pe.Code)
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return errors.New("cannot connect to the database")
	}
	return err
}

// DB exposes the pool for tests and the migration runner.
func (s *Store) DB() *sql.DB { return s.db }

// WithTx implements store.Store.
func (s *Store) WithTx(ctx context.Context, fn func(tx store.Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Store{db: s.db, q: rebinder{tx}, tx: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// migrateLockKey is the pg_advisory_lock key that serializes migrators, so two
// instances starting together cannot both apply a migration.
const migrateLockKey = 0x717a7a6f // "qzzo"

// Migrate implements store.Store.
func (s *Store) Migrate(ctx context.Context) ([]int, error) {
	return migrate.UpDialect(ctx, s.db, migrations.FS, "postgres", migrate.Dialect{
		Rebind:        Rebind,
		AppliedAtType: "TIMESTAMPTZ",
		// Timestamp versions (YYYYMMDDHHMMSS) overflow INTEGER; older databases are widened.
		VersionType: "BIGINT",
		AfterCreate: `ALTER TABLE schema_migrations ALTER COLUMN version TYPE BIGINT`,
		AppliedAt:   func(t time.Time) any { return t.UTC() },
		Lock: func(ctx context.Context) (func(), error) {
			c, err := s.db.Conn(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := c.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(migrateLockKey)); err != nil {
				c.Close()
				return nil, err
			}
			return func() {
				// Background: the caller's ctx may already be cancelled.
				c.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(migrateLockKey))
				c.Close()
			}, nil
		},
	})
}

// Close implements store.Store.
func (s *Store) Close() error { return s.db.Close() }

// ts converts a time to the UTC value bound to a timestamptz column.
func ts(t time.Time) time.Time { return t.UTC() }

// nullTS is ts for an optional time: the zero time is NULL.
func nullTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ts(t)
}

// isUnique reports whether err is a unique-constraint violation.
func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// compactJSON strips the whitespace jsonb adds on output, so a payload read
// back is compact JSON (key order follows jsonb's, which consumers must not
// depend on). Invalid input is returned unchanged.
func compactJSON(b []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return b
	}
	return out.Bytes()
}
