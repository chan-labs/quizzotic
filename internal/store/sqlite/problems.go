// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Problems implements store.Store.
func (s *Store) Problems() problems.Repo { return problemRepo{s.q} }

type problemRepo struct{ q execer }

const problemCols = `id, owner_id, status, current_version, created_by_kind, created_by_user_id,
	created_by_client_id, created_at, updated_at, deleted_at`

const versionCols = `problem_id, version, type, prompt, payload, explanation, source, change_kind,
	created_by_kind, created_by_user_id, created_by_client_id, created_at`

func actorUser(a domain.Actor) string {
	if a.UserID.IsNil() {
		return ""
	}
	return a.UserID.String()
}

func parseActorUser(s string) (domain.ID, error) {
	if s == "" {
		return domain.Nil, nil
	}
	return domain.ParseID(s)
}

type scanner interface{ Scan(dest ...any) error }

// problemRow holds the raw columns of a problems row (problemCols order).
type problemRow struct {
	p                 problems.Problem
	id, owner, status string
	kind, actorUID    string
	created, updated  int64
	deleted           sql.NullInt64
}

func (r *problemRow) dests() []any {
	return []any{&r.id, &r.owner, &r.status, &r.p.CurrentVersion, &r.kind, &r.actorUID,
		&r.p.CreatedBy.ClientID, &r.created, &r.updated, &r.deleted}
}

func (r *problemRow) finish() (problems.Problem, error) {
	p := r.p
	var err error
	if p.ID, err = domain.ParseID(r.id); err != nil {
		return problems.Problem{}, err
	}
	if p.OwnerID, err = domain.ParseID(r.owner); err != nil {
		return problems.Problem{}, err
	}
	if p.CreatedBy.UserID, err = parseActorUser(r.actorUID); err != nil {
		return problems.Problem{}, err
	}
	p.Status = problems.Status(r.status)
	p.CreatedBy.Kind = domain.ActorKind(r.kind)
	p.CreatedAt, p.UpdatedAt = FromMillis(r.created), FromMillis(r.updated)
	if r.deleted.Valid {
		t := FromMillis(r.deleted.Int64)
		p.DeletedAt = &t
	}
	return p, nil
}

func scanProblem(r scanner) (problems.Problem, error) {
	var row problemRow
	if err := r.Scan(row.dests()...); err != nil {
		return problems.Problem{}, err
	}
	return row.finish()
}

// versionRow holds the raw columns of a problem_versions row (versionCols order).
type versionRow struct {
	v                   problems.Version
	pid, kind, change   string
	actorKind, actorUID string
	payload             string
	created             int64
}

func (r *versionRow) dests() []any {
	return []any{&r.pid, &r.v.Version, &r.kind, &r.v.Prompt, &r.payload, &r.v.Explanation, &r.v.Source,
		&r.change, &r.actorKind, &r.actorUID, &r.v.CreatedBy.ClientID, &r.created}
}

func (r *versionRow) finish() (problems.Version, error) {
	v := r.v
	var err error
	if v.ProblemID, err = domain.ParseID(r.pid); err != nil {
		return problems.Version{}, err
	}
	if v.CreatedBy.UserID, err = parseActorUser(r.actorUID); err != nil {
		return problems.Version{}, err
	}
	v.Type, v.Payload = r.kind, []byte(r.payload)
	v.ChangeKind = problems.ChangeKind(r.change)
	v.CreatedBy.Kind = domain.ActorKind(r.actorKind)
	v.CreatedAt = FromMillis(r.created)
	return v, nil
}

func scanVersion(r scanner) (problems.Version, error) {
	var row versionRow
	if err := r.Scan(row.dests()...); err != nil {
		return problems.Version{}, err
	}
	return row.finish()
}

func insertVersion(ctx context.Context, q execer, v problems.Version) error {
	_, err := q.ExecContext(ctx, `INSERT INTO problem_versions (`+versionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ProblemID.String(), v.Version, v.Type, v.Prompt, string(v.Payload), v.Explanation, v.Source,
		string(v.ChangeKind), string(v.CreatedBy.Kind), actorUser(v.CreatedBy), v.CreatedBy.ClientID,
		Millis(v.CreatedAt))
	return err
}

func (r problemRepo) Create(ctx context.Context, ownerID domain.ID, p problems.Problem, v problems.Version) error {
	if p.OwnerID != ownerID {
		return errors.New("problems: problem owner does not match owner id")
	}
	if p.CurrentVersion != 1 || v.Version != 1 || v.ProblemID != p.ID {
		return errors.New("problems: create needs version 1 of the same problem")
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO problems (`+problemCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		p.ID.String(), ownerID.String(), string(p.Status), p.CurrentVersion, string(p.CreatedBy.Kind),
		actorUser(p.CreatedBy), p.CreatedBy.ClientID, Millis(p.CreatedAt), Millis(p.UpdatedAt))
	if err != nil {
		return err
	}
	return insertVersion(ctx, r.q, v)
}

func (r problemRepo) Get(ctx context.Context, ownerID, id domain.ID) (problems.Problem, error) {
	p, err := scanProblem(r.q.QueryRowContext(ctx, `SELECT `+problemCols+` FROM problems
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, id.String(), ownerID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return problems.Problem{}, problems.ErrNotFound
	}
	return p, err
}

func (r problemRepo) GetVersion(ctx context.Context, ownerID, id domain.ID, version int) (problems.Version, error) {
	v, err := scanVersion(r.q.QueryRowContext(ctx, `SELECT `+prefixed("v.", versionCols)+`
		FROM problem_versions v JOIN problems p ON p.id = v.problem_id
		WHERE v.problem_id = ? AND v.version = ? AND p.owner_id = ? AND p.deleted_at IS NULL`,
		id.String(), version, ownerID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return problems.Version{}, problems.ErrNotFound
	}
	return v, err
}

func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func (r problemRepo) ListVersions(ctx context.Context, ownerID, id domain.ID) ([]problems.Version, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+prefixed("v.", versionCols)+`
		FROM problem_versions v JOIN problems p ON p.id = v.problem_id
		WHERE v.problem_id = ? AND p.owner_id = ? AND p.deleted_at IS NULL
		ORDER BY v.version`, id.String(), ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []problems.Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, problems.ErrNotFound
	}
	return out, nil
}

func (r problemRepo) List(ctx context.Context, ownerID domain.ID, f problems.ListFilter) ([]problems.Problem, error) {
	cur, err := r.ListCurrent(ctx, ownerID, f)
	if err != nil {
		return nil, err
	}
	var out []problems.Problem
	for _, c := range cur {
		out = append(out, c.Problem)
	}
	return out, nil
}

// likeEscaper makes a string literal inside a LIKE pattern (ESCAPE '\').
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (r problemRepo) ListCurrent(ctx context.Context, ownerID domain.ID, f problems.ListFilter) ([]problems.Current, error) {
	// LIKE is case-insensitive for ASCII in SQLite, which is the contract.
	q := `SELECT ` + prefixed("p.", problemCols) + `, ` + prefixed("v.", versionCols) + `
		FROM problems p JOIN problem_versions v ON v.problem_id = p.id AND v.version = p.current_version
		WHERE p.owner_id = ?`
	args := []any{ownerID.String()}
	if !f.IncludeDeleted {
		q += ` AND p.deleted_at IS NULL`
	}
	if f.Status != "" {
		q += ` AND p.status = ?`
		args = append(args, string(f.Status))
	}
	if f.Type != "" {
		q += ` AND v.type = ?`
		args = append(args, f.Type)
	}
	if f.CreatedByKind != "" {
		q += ` AND p.created_by_kind = ?`
		args = append(args, string(f.CreatedByKind))
	}
	if !f.TagID.IsNil() {
		q += ` AND EXISTS (SELECT 1 FROM problem_tags pt WHERE pt.problem_id = p.id AND pt.tag_id = ?)`
		args = append(args, f.TagID.String())
	}
	if f.Query != "" {
		q += ` AND v.prompt LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscaper.Replace(f.Query)+"%")
	}
	if !f.After.IsNil() {
		q += ` AND p.id > ?`
		args = append(args, f.After.String())
	}
	q += ` ORDER BY p.id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []problems.Current
	for rows.Next() {
		var pr problemRow
		var vr versionRow
		if err := rows.Scan(append(pr.dests(), vr.dests()...)...); err != nil {
			return nil, err
		}
		p, err := pr.finish()
		if err != nil {
			return nil, err
		}
		v, err := vr.finish()
		if err != nil {
			return nil, err
		}
		out = append(out, problems.Current{Problem: p, Version: v})
	}
	return out, rows.Err()
}

func (r problemRepo) AppendVersion(ctx context.Context, ownerID, id domain.ID, v problems.Version) (problems.Version, error) {
	var next int
	err := r.q.QueryRowContext(ctx, `UPDATE problems SET current_version = current_version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL RETURNING current_version`,
		Millis(v.CreatedAt), id.String(), ownerID.String()).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return problems.Version{}, problems.ErrNotFound
	}
	if err != nil {
		return problems.Version{}, err
	}
	v.ProblemID, v.Version = id, next
	if err := insertVersion(ctx, r.q, v); err != nil {
		return problems.Version{}, err
	}
	return v, nil
}

func (r problemRepo) exec(ctx context.Context, q string, args ...any) error {
	res, err := r.q.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return problems.ErrNotFound
	}
	return nil
}

func (r problemRepo) SetStatus(ctx context.Context, ownerID, id domain.ID, s problems.Status, at time.Time) error {
	return r.exec(ctx, `UPDATE problems SET status = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		string(s), Millis(at), id.String(), ownerID.String())
}

func (r problemRepo) SoftDelete(ctx context.Context, ownerID, id domain.ID, at time.Time) error {
	return r.exec(ctx, `UPDATE problems SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		Millis(at), Millis(at), id.String(), ownerID.String())
}
