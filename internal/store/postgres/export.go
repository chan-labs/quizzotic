// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/export"
)

// Export implements store.Store.
func (s *Store) Export() export.Repo { return exportRepo{s.q} }

type exportRepo struct{ q execer }

func colNames(t export.Table) string {
	names := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// pgType is the cast that gives a bound parameter the column's type.
func pgType(k export.Kind) string {
	switch k {
	case export.Int:
		return "bigint"
	case export.Real:
		return "double precision"
	case export.Bool:
		return "boolean"
	case export.Time:
		return "timestamptz"
	case export.JSON:
		return "jsonb"
	}
	return "text"
}

func (r exportRepo) Read(ctx context.Context, owner domain.ID, t export.Table, after export.Row, limit int) ([]export.Row, error) {
	q := `SELECT ` + colNames(t) + ` FROM ` + t.Name + ` WHERE ` + t.Owner
	args := []any{owner.String()}
	if after != nil {
		left := make([]string, len(t.Key))
		right := make([]string, len(t.Key))
		for i, k := range t.Key {
			c, _ := t.Col(k)
			left[i] = k
			if c.Kind == export.Text {
				// Sort and compare bytewise, as SQLite does.
				left[i] = k + ` COLLATE "C"`
			}
			right[i] = `?::` + pgType(c.Kind)
			args = append(args, after[k])
		}
		q += ` AND (` + strings.Join(left, ", ") + `) > (` + strings.Join(right, ", ") + `)`
	}
	order := make([]string, len(t.Key))
	for i, k := range t.Key {
		order[i] = k
		if c, _ := t.Col(k); c.Kind == export.Text {
			order[i] = k + ` COLLATE "C"`
		}
	}
	q += ` ORDER BY ` + strings.Join(order, ", ") + ` LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []export.Row
	for rows.Next() {
		dest := make([]any, len(t.Cols))
		for i, c := range t.Cols {
			switch c.Kind {
			case export.Text:
				dest[i] = new(sql.NullString)
			case export.JSON:
				dest[i] = new([]byte)
			case export.Real:
				dest[i] = new(sql.NullFloat64)
			case export.Bool:
				dest[i] = new(sql.NullBool)
			case export.Time:
				dest[i] = new(sql.NullTime)
			default:
				dest[i] = new(sql.NullInt64)
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(export.Row, len(t.Cols))
		for i, c := range t.Cols {
			row[c.Name] = fromScan(dest[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func fromScan(d any) any {
	switch v := d.(type) {
	case *sql.NullString:
		if v.Valid {
			return v.String
		}
	case *[]byte:
		if *v != nil {
			return json.RawMessage(*v)
		}
	case *sql.NullFloat64:
		if v.Valid {
			return v.Float64
		}
	case *sql.NullBool:
		if v.Valid {
			return v.Bool
		}
	case *sql.NullTime:
		if v.Valid {
			return v.Time.UTC()
		}
	case *sql.NullInt64:
		if v.Valid {
			return v.Int64
		}
	}
	return nil
}

func toArg(k export.Kind, v any) any {
	if v == nil {
		return nil
	}
	switch k {
	case export.Time:
		return ts(v.(time.Time))
	case export.JSON:
		return string(v.(json.RawMessage))
	}
	return v
}

func (r exportRepo) Insert(ctx context.Context, t export.Table, rows []export.Row) error {
	ph := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		ph[i] = `?::` + pgType(c.Kind)
	}
	q := `INSERT INTO ` + t.Name + ` (` + colNames(t) + `) VALUES (` + strings.Join(ph, ", ") + `)`
	for _, row := range rows {
		args := make([]any, len(t.Cols))
		for i, c := range t.Cols {
			args[i] = toArg(c.Kind, row[c.Name])
		}
		if _, err := r.q.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}

func (r exportRepo) SetColumn(ctx context.Context, t export.Table, key export.Row, column string, value any) error {
	c, ok := t.Col(column)
	if !ok {
		return fmt.Errorf("export: %s has no column %q", t.Name, column)
	}
	where := make([]string, len(t.Key))
	args := []any{toArg(c.Kind, value)}
	for i, k := range t.Key {
		kc, _ := t.Col(k)
		where[i] = k + ` = ?::` + pgType(kc.Kind)
		args = append(args, key[k])
	}
	_, err := r.q.ExecContext(ctx, `UPDATE `+t.Name+` SET `+column+` = ?::`+pgType(c.Kind)+` WHERE `+strings.Join(where, " AND "), args...)
	return err
}

func (r exportRepo) HasUsers(ctx context.Context) (bool, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM users LIMIT 1) u`).Scan(&n)
	return n > 0, err
}
