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

func scanProblem(r scanner) (problems.Problem, error) {
	var (
		p                 problems.Problem
		id, owner, status string
		kind, actorUID    string
		created, updated  int64
		deleted           sql.NullInt64
	)
	err := r.Scan(&id, &owner, &status, &p.CurrentVersion, &kind, &actorUID, &p.CreatedBy.ClientID,
		&created, &updated, &deleted)
	if err != nil {
		return problems.Problem{}, err
	}
	if p.ID, err = domain.ParseID(id); err != nil {
		return problems.Problem{}, err
	}
	if p.OwnerID, err = domain.ParseID(owner); err != nil {
		return problems.Problem{}, err
	}
	if p.CreatedBy.UserID, err = parseActorUser(actorUID); err != nil {
		return problems.Problem{}, err
	}
	p.Status = problems.Status(status)
	p.CreatedBy.Kind = domain.ActorKind(kind)
	p.CreatedAt, p.UpdatedAt = FromMillis(created), FromMillis(updated)
	if deleted.Valid {
		t := FromMillis(deleted.Int64)
		p.DeletedAt = &t
	}
	return p, nil
}

func scanVersion(r scanner) (problems.Version, error) {
	var (
		v                   problems.Version
		pid, kind, change   string
		actorKind, actorUID string
		payload             string
		created             int64
	)
	err := r.Scan(&pid, &v.Version, &kind, &v.Prompt, &payload, &v.Explanation, &v.Source, &change,
		&actorKind, &actorUID, &v.CreatedBy.ClientID, &created)
	if err != nil {
		return problems.Version{}, err
	}
	if v.ProblemID, err = domain.ParseID(pid); err != nil {
		return problems.Version{}, err
	}
	if v.CreatedBy.UserID, err = parseActorUser(actorUID); err != nil {
		return problems.Version{}, err
	}
	v.Type, v.Payload = kind, []byte(payload)
	v.ChangeKind = problems.ChangeKind(change)
	v.CreatedBy.Kind = domain.ActorKind(actorKind)
	v.CreatedAt = FromMillis(created)
	return v, nil
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
	q := `SELECT ` + problemCols + ` FROM problems WHERE owner_id = ?`
	args := []any{ownerID.String()}
	if !f.IncludeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if !f.After.IsNil() {
		q += ` AND id > ?`
		args = append(args, f.After.String())
	}
	q += ` ORDER BY id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []problems.Problem
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
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
