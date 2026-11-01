// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/quizzes"
)

// Quizzes implements store.Store.
func (s *Store) Quizzes() quizzes.Repo { return quizRepo{s.q} }

type quizRepo struct{ q execer }

const quizCols = `id, owner_id, title, description, status, created_by_kind, created_by_user_id,
	created_by_client_id, created_at, updated_at`

func scanQuiz(r scanner) (quizzes.Quiz, error) {
	var (
		q                       quizzes.Quiz
		id, owner, status, kind string
		actorUID                string
		created, updated        time.Time
	)
	if err := r.Scan(&id, &owner, &q.Title, &q.Description, &status, &kind, &actorUID,
		&q.CreatedBy.ClientID, &created, &updated); err != nil {
		return quizzes.Quiz{}, err
	}
	var err error
	if q.ID, err = domain.ParseID(id); err != nil {
		return quizzes.Quiz{}, err
	}
	if q.OwnerID, err = domain.ParseID(owner); err != nil {
		return quizzes.Quiz{}, err
	}
	if q.CreatedBy.UserID, err = parseActorUser(actorUID); err != nil {
		return quizzes.Quiz{}, err
	}
	q.Status = quizzes.Status(status)
	q.CreatedBy.Kind = domain.ActorKind(kind)
	q.CreatedAt, q.UpdatedAt = created.UTC(), updated.UTC()
	q.Items = []domain.ID{}
	return q, nil
}

// withItems fills in Items (in position order) for every quiz in qs with one
// query. The quizzes were read for ownerID.
func (r quizRepo) withItems(ctx context.Context, qs []quizzes.Quiz) error {
	if len(qs) == 0 {
		return nil
	}
	idx := make(map[string]int, len(qs))
	args := make([]any, len(qs))
	marks := make([]string, len(qs))
	for i, q := range qs {
		idx[q.ID.String()] = i
		args[i] = q.ID.String()
		marks[i] = "?"
	}
	rows, err := r.q.QueryContext(ctx, `SELECT quiz_id, problem_id FROM quiz_items
		WHERE quiz_id IN (`+strings.Join(marks, ",")+`) ORDER BY quiz_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var qid, pid string
		if err := rows.Scan(&qid, &pid); err != nil {
			return err
		}
		id, err := domain.ParseID(pid)
		if err != nil {
			return err
		}
		i := idx[qid]
		qs[i].Items = append(qs[i].Items, id)
	}
	return rows.Err()
}

func (r quizRepo) Create(ctx context.Context, ownerID domain.ID, q quizzes.Quiz) error {
	if q.OwnerID != ownerID {
		return errors.New("quizzes: quiz owner does not match owner id")
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO quizzes (`+quizCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.ID.String(), ownerID.String(), q.Title, q.Description, string(q.Status), string(q.CreatedBy.Kind),
		actorUser(q.CreatedBy), q.CreatedBy.ClientID, ts(q.CreatedAt), ts(q.UpdatedAt))
	return err
}

func (r quizRepo) Get(ctx context.Context, ownerID, id domain.ID) (quizzes.Quiz, error) {
	q, err := scanQuiz(r.q.QueryRowContext(ctx, `SELECT `+quizCols+` FROM quizzes
		WHERE owner_id = ? AND id = ? AND deleted_at IS NULL`, ownerID.String(), id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return quizzes.Quiz{}, quizzes.ErrNotFound
	}
	if err != nil {
		return quizzes.Quiz{}, err
	}
	qs := []quizzes.Quiz{q}
	if err := r.withItems(ctx, qs); err != nil {
		return quizzes.Quiz{}, err
	}
	return qs[0], nil
}

func (r quizRepo) List(ctx context.Context, ownerID domain.ID, f quizzes.ListFilter) ([]quizzes.Quiz, error) {
	q := `SELECT ` + quizCols + ` FROM quizzes WHERE owner_id = ? AND deleted_at IS NULL`
	args := []any{ownerID.String()}
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
	var out []quizzes.Quiz
	for rows.Next() {
		qz, err := scanQuiz(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, qz)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, r.withItems(ctx, out)
}

func (r quizRepo) Update(ctx context.Context, ownerID domain.ID, q quizzes.Quiz) error {
	res, err := r.q.ExecContext(ctx, `UPDATE quizzes SET title = ?, description = ?, status = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		q.Title, q.Description, string(q.Status), ts(q.UpdatedAt), q.ID.String(), ownerID.String())
	return oneRow(res, err)
}

func (r quizRepo) SetItems(ctx context.Context, ownerID, id domain.ID, problemIDs []domain.ID, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE quizzes SET updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, ts(at), id.String(), ownerID.String())
	if err := oneRow(res, err); err != nil {
		return err
	}
	if _, err := r.q.ExecContext(ctx, `DELETE FROM quiz_items WHERE quiz_id = ?`, id.String()); err != nil {
		return err
	}
	for i, pid := range problemIDs {
		// The owner and deleted checks live in the statement itself, so another
		// owner's problem can never be attached.
		res, err := r.q.ExecContext(ctx, `INSERT INTO quiz_items (quiz_id, position, problem_id)
			SELECT ?::text, ?::integer, id FROM problems WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
			id.String(), i, pid.String(), ownerID.String())
		if isUnique(err) {
			return quizzes.ErrItemNotFound // a duplicate is not a valid item list
		}
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return quizzes.ErrItemNotFound
		}
	}
	return nil
}

func (r quizRepo) SoftDelete(ctx context.Context, ownerID, id domain.ID, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE quizzes SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, ts(at), ts(at), id.String(), ownerID.String())
	return oneRow(res, err)
}

// oneRow maps a statement that must touch exactly one quiz row to ErrNotFound.
func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return quizzes.ErrNotFound
	}
	return nil
}
