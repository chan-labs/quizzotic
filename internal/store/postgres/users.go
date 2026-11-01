// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
)

// Users implements store.Store.
func (s *Store) Users() auth.UserRepo { return userRepo{s.q} }

// Identities implements store.Store.
func (s *Store) Identities() auth.IdentityRepo { return identityRepo{s.q} }

type userRepo struct{ q execer }

func (r userRepo) Create(ctx context.Context, u auth.User) error {
	var steps *string
	if u.Scheduling.LearningSteps != nil {
		b, err := json.Marshal(u.Scheduling.LearningSteps)
		if err != nil {
			return err
		}
		v := string(b)
		steps = &v
	}
	sc := u.Scheduling
	_, err := r.q.ExecContext(ctx, `INSERT INTO users
		(id, display_name, email, created_at, desired_retention, learning_steps,
		 new_per_day, max_reviews_per_day, new_interval, weak_tag_boost, rollover_hour, timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID.String(), u.DisplayName, u.Email, ts(u.CreatedAt), sc.DesiredRetention, steps,
		sc.NewPerDay, sc.MaxReviewsPerDay, sc.NewInterval, sc.WeakTagBoost, sc.RolloverHour, sc.Timezone)
	return err
}

func (r userRepo) Get(ctx context.Context, userID domain.ID) (auth.User, error) {
	var (
		u     auth.User
		id    string
		ms    time.Time
		steps []byte
		ret   sql.NullFloat64
		npd   sql.NullInt64
		mrd   sql.NullInt64
		ni    sql.NullInt64
		wtb   sql.NullFloat64
		roh   sql.NullInt64
		tz    sql.NullString
	)
	err := r.q.QueryRowContext(ctx, `SELECT id, display_name, email, created_at, desired_retention,
		learning_steps, new_per_day, max_reviews_per_day, new_interval, weak_tag_boost, rollover_hour, timezone
		FROM users WHERE id = ?`, userID.String()).
		Scan(&id, &u.DisplayName, &u.Email, &ms, &ret, &steps, &npd, &mrd, &ni, &wtb, &roh, &tz)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	if u.ID, err = domain.ParseID(id); err != nil {
		return auth.User{}, err
	}
	u.CreatedAt = ms.UTC()
	sc := &u.Scheduling
	if ret.Valid {
		sc.DesiredRetention = &ret.Float64
	}
	if steps != nil {
		if err := json.Unmarshal(steps, &sc.LearningSteps); err != nil {
			return auth.User{}, err
		}
	}
	sc.NewPerDay = intPtr(npd)
	sc.MaxReviewsPerDay = intPtr(mrd)
	sc.NewInterval = intPtr(ni)
	sc.RolloverHour = intPtr(roh)
	if wtb.Valid {
		sc.WeakTagBoost = &wtb.Float64
	}
	if tz.Valid {
		sc.Timezone = &tz.String
	}
	return u, nil
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func (r userRepo) UpdateProfile(ctx context.Context, userID domain.ID, displayName, email string) error {
	res, err := r.q.ExecContext(ctx, `UPDATE users SET display_name = ?, email = ? WHERE id = ?`,
		displayName, email, userID.String())
	return requireRow(res, err)
}

func (r userRepo) Sole(ctx context.Context) (auth.User, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 2`)
	if err != nil {
		return auth.User{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return auth.User{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return auth.User{}, err
	}
	switch len(ids) {
	case 0:
		return auth.User{}, auth.ErrNotFound
	case 1:
		uid, err := domain.ParseID(ids[0])
		if err != nil {
			return auth.User{}, err
		}
		return r.Get(ctx, uid)
	}
	return auth.User{}, auth.ErrAmbiguous
}

type identityRepo struct{ q execer }

func (r identityRepo) GetByIssuerSubject(ctx context.Context, issuer, subject string) (auth.Identity, error) {
	var id, uid string
	var created, seen time.Time
	err := r.q.QueryRowContext(ctx, `SELECT id, user_id, created_at, last_seen_at
		FROM identities WHERE issuer = ? AND subject = ?`, issuer, subject).
		Scan(&id, &uid, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Identity{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Identity{}, err
	}
	out := auth.Identity{Issuer: issuer, Subject: subject, CreatedAt: created.UTC(), LastSeenAt: seen.UTC()}
	if out.ID, err = domain.ParseID(id); err != nil {
		return auth.Identity{}, err
	}
	if out.UserID, err = domain.ParseID(uid); err != nil {
		return auth.Identity{}, err
	}
	return out, nil
}

func (r identityRepo) InsertIfAbsent(ctx context.Context, i auth.Identity) (bool, error) {
	res, err := r.q.ExecContext(ctx, `INSERT INTO identities (id, user_id, issuer, subject, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (issuer, subject) DO NOTHING`,
		i.ID.String(), i.UserID.String(), i.Issuer, i.Subject, ts(i.CreatedAt), ts(i.LastSeenAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r identityRepo) TouchLastSeen(ctx context.Context, userID, identityID domain.ID, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE identities SET last_seen_at = ? WHERE id = ? AND user_id = ?`,
		ts(at), identityID.String(), userID.String())
	return requireRow(res, err)
}

func requireRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return auth.ErrNotFound
	}
	return nil
}
