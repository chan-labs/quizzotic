// SPDX-License-Identifier: Apache-2.0

package sqlite

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

func (r exportRepo) Read(ctx context.Context, owner domain.ID, t export.Table, after export.Row, limit int) ([]export.Row, error) {
	q := `SELECT ` + colNames(t) + ` FROM ` + t.Name + ` WHERE ` + t.Owner
	args := []any{owner.String()}
	if after != nil {
		ph := make([]string, len(t.Key))
		for i, k := range t.Key {
			ph[i] = "?"
			args = append(args, after[k])
		}
		q += ` AND (` + strings.Join(t.Key, ", ") + `) > (` + strings.Join(ph, ", ") + `)`
	}
	q += ` ORDER BY ` + strings.Join(t.Key, ", ") + ` LIMIT ?`
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
			case export.Text, export.JSON:
				dest[i] = new(sql.NullString)
			case export.Real:
				dest[i] = new(sql.NullFloat64)
			default: // Int, Bool, Time (Unix milliseconds)
				dest[i] = new(sql.NullInt64)
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(export.Row, len(t.Cols))
		for i, c := range t.Cols {
			row[c.Name] = fromScan(c.Kind, dest[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func fromScan(k export.Kind, d any) any {
	switch v := d.(type) {
	case *sql.NullString:
		if !v.Valid {
			return nil
		}
		if k == export.JSON {
			return json.RawMessage(v.String)
		}
		return v.String
	case *sql.NullFloat64:
		if !v.Valid {
			return nil
		}
		return v.Float64
	case *sql.NullInt64:
		if !v.Valid {
			return nil
		}
		switch k {
		case export.Bool:
			return v.Int64 != 0
		case export.Time:
			return FromMillis(v.Int64)
		}
		return v.Int64
	}
	return nil
}

func toArg(k export.Kind, v any) any {
	if v == nil {
		return nil
	}
	switch k {
	case export.Bool:
		return boolInt(v.(bool))
	case export.Time:
		return Millis(v.(time.Time))
	case export.JSON:
		return string(v.(json.RawMessage))
	}
	return v
}

func (r exportRepo) Insert(ctx context.Context, t export.Table, rows []export.Row) error {
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(t.Cols)), ", ")
	q := `INSERT INTO ` + t.Name + ` (` + colNames(t) + `) VALUES (` + ph + `)`
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
		where[i] = k + ` = ?`
		args = append(args, key[k])
	}
	_, err := r.q.ExecContext(ctx, `UPDATE `+t.Name+` SET `+column+` = ? WHERE `+strings.Join(where, " AND "), args...)
	return err
}

func (r exportRepo) HasUsers(ctx context.Context) (bool, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM users LIMIT 1)`).Scan(&n)
	return n > 0, err
}
