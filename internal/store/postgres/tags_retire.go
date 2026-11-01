// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func (r tagRepo) Retire(ctx context.Context, ownerID, id domain.ID, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE tags SET status = ?, updated_at = ? WHERE id = ? AND owner_id = ?`,
		string(tags.StatusRetired), ts(at), id.String(), ownerID.String())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tags.ErrNotFound
	}
	return nil
}
