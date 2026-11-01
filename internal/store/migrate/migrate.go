// SPDX-License-Identifier: Apache-2.0

// Package migrate applies numbered SQL migrations from an fs.FS and records
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

// Migration is one SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Load reads and orders the migrations in dir of fsys. Versions must be
// unique; gaps are allowed.
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
			return nil, fmt.Errorf("migration file %q: want NNNN_name.sql", e.Name())
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
	migs, err := Load(fsys, dir)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
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
			return applied, fmt.Errorf("migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, time.Now().UnixMilli()); err != nil {
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
