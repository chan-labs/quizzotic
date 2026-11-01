// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
)

// AttemptLog implements metrics.Repo.
func (r masteryRepo) AttemptLog(ctx context.Context, ownerID domain.ID, since time.Time) ([]attempts.Attempt, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+attemptCols+` FROM attempts
		WHERE user_id = ?
		  AND problem_id IN (SELECT id FROM problems WHERE owner_id = ? AND deleted_at IS NULL)
		  AND EXISTS (SELECT 1 FROM attempts b WHERE b.user_id = attempts.user_id
			AND b.problem_id = attempts.problem_id AND b.card_epoch = attempts.card_epoch
			AND b.answered_at >= ?)
		ORDER BY problem_id, card_epoch, answered_at, id`,
		ownerID.String(), ownerID.String(), Millis(since))
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

// ProblemTags implements metrics.Repo.
func (r masteryRepo) ProblemTags(ctx context.Context, ownerID domain.ID) (map[domain.ID][]domain.ID, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT pt.problem_id, pt.tag_id FROM problem_tags pt
		JOIN problems p ON p.id = pt.problem_id AND p.owner_id = ?
		JOIN tags t ON t.id = pt.tag_id AND t.owner_id = ? AND t.status = 'active'
		ORDER BY pt.problem_id, pt.tag_id`, ownerID.String(), ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.ID][]domain.ID{}
	for rows.Next() {
		var ps, ts string
		if err := rows.Scan(&ps, &ts); err != nil {
			return nil, err
		}
		pid, err := domain.ParseID(ps)
		if err != nil {
			return nil, err
		}
		tid, err := domain.ParseID(ts)
		if err != nil {
			return nil, err
		}
		out[pid] = append(out[pid], tid)
	}
	return out, rows.Err()
}

// DueTimes implements metrics.Repo.
func (r masteryRepo) DueTimes(ctx context.Context, ownerID domain.ID, before time.Time) ([]time.Time, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT c.due FROM cards c
		JOIN problems p ON p.id = c.problem_id AND p.owner_id = ? AND p.status = 'live' AND p.deleted_at IS NULL
		WHERE c.user_id = ? AND c.due < ?
		ORDER BY c.due`, ownerID.String(), ownerID.String(), Millis(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var ms int64
		if err := rows.Scan(&ms); err != nil {
			return nil, err
		}
		out = append(out, FromMillis(ms))
	}
	return out, rows.Err()
}
