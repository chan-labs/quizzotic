// SPDX-License-Identifier: Apache-2.0

// Package migrate applies versioned SQL migrations from an fs.FS and records
// them in schema_migrations. Each backend passes the directory holding its
// own dialect's SQL. Bookkeeping SQL uses ? placeholders, so a backend whose
// driver needs another style must wrap db.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"
)

var nameRE = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// A version is either a legacy sequence number (up to four digits, 0001 to
// 0014 today) or a UTC timestamp of exactly 14 digits (YYYYMMDDHHMMSS). New
// migrations use timestamps so two branches adding one never pick the same
// number or edit a shared line. Timestamps sort after every legacy number, so
// the applied history keeps its order and its recorded version values.
const (
	maxLegacyDigits = 4
	timestampDigits = 14
)

func checkVersionDigits(file, digits string) error {
	if n := len(digits); n > maxLegacyDigits && n != timestampDigits {
		return fmt.Errorf("migration file %q: version must be a sequence number of up to %d digits or a %d-digit YYYYMMDDHHMMSS timestamp",
			file, maxLegacyDigits, timestampDigits)
	}
	return nil
}

// Migration is one SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Load reads and orders the migrations in dir of fsys. Versions (legacy
// sequence numbers and 14-digit timestamps, see above) must be unique; gaps
// are allowed.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == ".gitkeep" {
			continue
		}
		m := nameRE.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q: want NNNN_name.sql or YYYYMMDDHHMMSS_name.sql", e.Name())
		}
		if err := checkVersionDigits(e.Name(), m[1]); err != nil {
			return nil, err
		}
		v, _ := strconv.Atoi(m[1])
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migration version %d used by %q and %q", v, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Up creates schema_migrations if needed and applies every migration not yet
// recorded, each in its own transaction, in version order. It returns the
// versions applied; a second run applies nothing. A migration already
// recorded under a different name is an error (the history was edited).
func Up(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) ([]int, error) {
	return UpDialect(ctx, db, fsys, dir, Dialect{})
}

// Dialect adapts the bookkeeping SQL to a backend. The zero value is SQLite:
// ? placeholders and applied_at as Unix milliseconds.
type Dialect struct {
	// Rebind rewrites ? placeholders; nil leaves them alone.
	Rebind func(string) string
	// AppliedAtType is the column type of schema_migrations.applied_at.
	AppliedAtType string
	// AppliedAt converts the apply time to a value of that type.
	AppliedAt func(time.Time) any
	// VersionType is the column type of schema_migrations.version; empty is
	// INTEGER. Timestamp versions need 64 bits.
	VersionType string
	// AfterCreate, if set, runs once the table exists, to bring a table
	// created by an older release up to VersionType.
	AfterCreate string
	// Lock, if set, runs before any migration and returns a release func.
	// A backend uses it to serialize concurrent migrators.
	Lock func(ctx context.Context) (release func(), err error)
}

// UpDialect is Up for a backend with its own bookkeeping dialect.
func UpDialect(ctx context.Context, db *sql.DB, fsys fs.FS, dir string, d Dialect) ([]int, error) {
	migs, err := Load(fsys, dir)
	if err != nil {
		return nil, err
	}
	rebind := d.Rebind
	if rebind == nil {
		rebind = func(q string) string { return q }
	}
	colType := d.AppliedAtType
	if colType == "" {
		colType = "INTEGER"
	}
	appliedAt := d.AppliedAt
	if appliedAt == nil {
		appliedAt = func(t time.Time) any { return t.UnixMilli() }
	}
	verType := d.VersionType
	if verType == "" {
		verType = "INTEGER"
	}
	if d.Lock != nil {
		release, err := d.Lock(ctx)
		if err != nil {
			return nil, fmt.Errorf("lock migrations: %w", err)
		}
		defer release()
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version `+verType+` PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at `+colType+` NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	if d.AfterCreate != "" {
		if _, err := db.ExecContext(ctx, d.AfterCreate); err != nil {
			return nil, fmt.Errorf("upgrade schema_migrations: %w", err)
		}
	}
	done, err := recorded(ctx, db)
	if err != nil {
		return nil, err
	}

	var applied []int
	for _, m := range migs {
		if n, ok := done[m.Version]; ok {
			if n != m.Name {
				return applied, fmt.Errorf("migration %d recorded as %q but file is %q", m.Version, n, m.Name)
			}
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return applied, err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("migration %s: %w", fileStem(m), err)
		}
		if _, err := tx.ExecContext(ctx, rebind(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`),
			m.Version, m.Name, appliedAt(time.Now())); err != nil {
			tx.Rollback()
			return applied, err
		}
		if err := tx.Commit(); err != nil {
			return applied, err
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// fileStem is the migration's file name without .sql, padded as on disk.
func fileStem(m Migration) string {
	if m.Version >= 1e13 {
		return fmt.Sprintf("%d_%s", m.Version, m.Name)
	}
	return fmt.Sprintf("%04d_%s", m.Version, m.Name)
}

func recorded(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	done := map[int]string{}
	for rows.Next() {
		var v int
		var n string
		if err := rows.Scan(&v, &n); err != nil {
			return nil, err
		}
		done[v] = n
	}
	return done, rows.Err()
}
