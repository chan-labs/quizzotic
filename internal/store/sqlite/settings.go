// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// Settings implements store.Store.
func (s *Store) Settings() settings.Repo { return settingsRepo{s.q} }

type settingsRepo struct{ q execer }

func (r settingsRepo) Get(ctx context.Context, userID domain.ID) (settings.Overrides, error) {
	var (
		o   settings.Overrides
		ret sql.NullFloat64
		npd sql.NullInt64
		mrd sql.NullInt64
		ni  sql.NullInt64
		wtb sql.NullFloat64
		roh sql.NullInt64
		tz  sql.NullString
	)
	err := r.q.QueryRowContext(ctx, `SELECT desired_retention, new_per_day, max_reviews_per_day,
		new_interval, weak_tag_boost, rollover_hour, timezone FROM users WHERE id = ?`, userID.String()).
		Scan(&ret, &npd, &mrd, &ni, &wtb, &roh, &tz)
	if errors.Is(err, sql.ErrNoRows) {
		return settings.Overrides{}, settings.ErrNotFound
	}
	if err != nil {
		return settings.Overrides{}, err
	}
	if ret.Valid {
		o.DesiredRetention = &ret.Float64
	}
	if wtb.Valid {
		o.WeakTagBoost = &wtb.Float64
	}
	if tz.Valid {
		o.Timezone = &tz.String
	}
	o.NewPerDay, o.MaxReviewsPerDay = intPtr(npd), intPtr(mrd)
	o.NewInterval, o.RolloverHour = intPtr(ni), intPtr(roh)
	return o, nil
}

func (r settingsRepo) Replace(ctx context.Context, userID domain.ID, o settings.Overrides) error {
	res, err := r.q.ExecContext(ctx, `UPDATE users SET desired_retention = ?, new_per_day = ?,
		max_reviews_per_day = ?, new_interval = ?, weak_tag_boost = ?, rollover_hour = ?, timezone = ?
		WHERE id = ?`,
		o.DesiredRetention, o.NewPerDay, o.MaxReviewsPerDay, o.NewInterval, o.WeakTagBoost,
		o.RolloverHour, o.Timezone, userID.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return settings.ErrNotFound
	}
	return nil
}
