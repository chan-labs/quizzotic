// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// HistoryProblems implements metrics.Repo.
func (r masteryRepo) HistoryProblems(ctx context.Context, ownerID domain.ID) ([]metrics.HistoryProblem, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, status, created_at, updated_at, deleted_at
		FROM problems WHERE owner_id = ? ORDER BY id`, ownerID.String())
	if err != nil {
		return nil, err
	}
	var out []metrics.HistoryProblem
	idx := map[domain.ID]int{}
	for rows.Next() {
		var (
			id, status       string
			created, updated time.Time
			deleted          sql.NullTime
		)
		if err := rows.Scan(&id, &status, &created, &updated, &deleted); err != nil {
			rows.Close()
			return nil, err
		}
		pid, err := domain.ParseID(id)
		if err != nil {
			rows.Close()
			return nil, err
		}
		p := metrics.HistoryProblem{ID: pid, Status: problems.Status(status),
			CreatedAt: created.UTC(), UpdatedAt: updated.UTC()}
		if deleted.Valid {
			p.DeletedAt = deleted.Time.UTC()
		}
		idx[pid] = len(out)
		out = append(out, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	vrows, err := r.q.QueryContext(ctx, `SELECT v.problem_id, v.created_at
		FROM problem_versions v JOIN problems p ON p.id = v.problem_id AND p.owner_id = ?
		WHERE v.change_kind = 'major' ORDER BY v.problem_id, v.version`, ownerID.String())
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var (
			id string
			at time.Time
		)
		if err := vrows.Scan(&id, &at); err != nil {
			return nil, err
		}
		pid, err := domain.ParseID(id)
		if err != nil {
			return nil, err
		}
		i := idx[pid]
		out[i].MajorAt = append(out[i].MajorAt, at.UTC())
	}
	if err := vrows.Err(); err != nil {
		return nil, err
	}

	tags, err := r.ProblemTags(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TagIDs = tags[out[i].ID]
	}
	return out, nil
}

// HistoryAttempts implements metrics.Repo.
func (r masteryRepo) HistoryAttempts(ctx context.Context, ownerID domain.ID) ([]attempts.Attempt, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+attemptCols+` FROM attempts
		WHERE user_id = ? AND problem_id IN (SELECT id FROM problems WHERE owner_id = ?)
		ORDER BY problem_id, card_epoch, answered_at, id`, ownerID.String(), ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []attempts.Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActiveTags implements metrics.Repo.
func (r masteryRepo) ActiveTags(ctx context.Context, ownerID domain.ID) ([]metrics.HistoryTag, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, created_at FROM tags
		WHERE owner_id = ? AND status = 'active' ORDER BY id`, ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.HistoryTag
	for rows.Next() {
		var (
			id string
			at time.Time
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		tid, err := domain.ParseID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, metrics.HistoryTag{ID: tid, CreatedAt: at.UTC()})
	}
	return out, rows.Err()
}

// Put implements metrics.SnapshotRepo: an upsert for the owner's tag.
func (r snapshotRepo) Put(ctx context.Context, ownerID domain.ID, s metrics.MasterySnapshot) error {
	if s.UserID != ownerID {
		return errors.New("metrics: snapshot user does not match owner id")
	}
	var owned int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE id = ? AND owner_id = ?`,
		s.TagID.String(), ownerID.String()).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		return metrics.ErrNotFound
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO mastery_snapshots (`+snapshotCols+`)
		VALUES (?::text, ?::text, ?::text, ?::double precision, ?::integer)
		ON CONFLICT (user_id, tag_id, date) DO UPDATE SET recall = excluded.recall, cards_seen = excluded.cards_seen`,
		ownerID.String(), s.TagID.String(), s.Date, s.Recall, s.CardsSeen)
	return err
}

// DeleteTags implements metrics.SnapshotRepo.
func (r snapshotRepo) DeleteTags(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) error {
	in, args := inClause("tag_id", tagIDs)
	if in == "" {
		return nil
	}
	_, err := r.q.ExecContext(ctx, `DELETE FROM mastery_snapshots WHERE user_id = ?`+in,
		append([]any{ownerID.String()}, args...)...)
	return err
}
