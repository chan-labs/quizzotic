// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
)

// MasterySnapshots implements store.Store.
func (s *Store) MasterySnapshots() metrics.SnapshotRepo { return snapshotRepo{s.q} }

type snapshotRepo struct{ q execer }

const snapshotCols = `user_id, tag_id, date, recall, cards_seen`

// InsertIfAbsent inserts only for the owner's tag (the SELECT yields nothing
// for another owner's), which is reported as ErrNotFound, and never overwrites.
func (r snapshotRepo) InsertIfAbsent(ctx context.Context, ownerID domain.ID, s metrics.MasterySnapshot) (bool, error) {
	if s.UserID != ownerID {
		return false, errors.New("metrics: snapshot user does not match owner id")
	}
	var owned int
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE id = ? AND owner_id = ?`,
		s.TagID.String(), ownerID.String()).Scan(&owned); err != nil {
		return false, err
	}
	if owned == 0 {
		return false, metrics.ErrNotFound
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO mastery_snapshots (`+snapshotCols+`) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, tag_id, date) DO NOTHING`,
		ownerID.String(), s.TagID.String(), s.Date, s.Recall, s.CardsSeen)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r snapshotRepo) query(ctx context.Context, q string, args ...any) ([]metrics.MasterySnapshot, error) {
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.MasterySnapshot
	for rows.Next() {
		var (
			s        metrics.MasterySnapshot
			uid, tid string
		)
		if err := rows.Scan(&uid, &tid, &s.Date, &s.Recall, &s.CardsSeen); err != nil {
			return nil, err
		}
		if s.UserID, err = domain.ParseID(uid); err != nil {
			return nil, err
		}
		if s.TagID, err = domain.ParseID(tid); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r snapshotRepo) List(ctx context.Context, ownerID, tagID domain.ID, from, to string) ([]metrics.MasterySnapshot, error) {
	return r.query(ctx, `SELECT `+snapshotCols+` FROM mastery_snapshots
		WHERE user_id = ? AND tag_id = ? AND (? = '' OR date >= ?) AND (? = '' OR date <= ?)
		ORDER BY date`,
		ownerID.String(), tagID.String(), from, from, to, to)
}

func (r snapshotRepo) ListDate(ctx context.Context, ownerID domain.ID, date string) ([]metrics.MasterySnapshot, error) {
	return r.query(ctx, `SELECT `+snapshotCols+` FROM mastery_snapshots WHERE user_id = ? AND date = ? ORDER BY tag_id`,
		ownerID.String(), date)
}

func (r snapshotRepo) UserIDs(ctx context.Context) ([]domain.ID, error) {
	return masteryRepo(r).idList(ctx, `SELECT id FROM users ORDER BY id`)
}
