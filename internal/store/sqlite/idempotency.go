// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Idempotency implements store.Store.
func (s *Store) Idempotency() platform.IdempotencyRepo { return idempotencyRepo{s.q} }

type idempotencyRepo struct{ q execer }

func (r idempotencyRepo) Get(ctx context.Context, userID domain.ID, key string, now time.Time) (platform.IdempotencyRecord, error) {
	rec := platform.IdempotencyRecord{UserID: userID, Key: key}
	var headers string
	var created, exp int64
	err := r.q.QueryRowContext(ctx, `SELECT request_hash, status, headers, body, created_at, expires_at
		FROM idempotency_keys WHERE user_id = ? AND key = ? AND expires_at > ?`,
		userID.String(), key, Millis(now)).
		Scan(&rec.RequestHash, &rec.Status, &headers, &rec.Body, &created, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return platform.IdempotencyRecord{}, platform.ErrIdempotencyNotFound
	}
	if err != nil {
		return platform.IdempotencyRecord{}, err
	}
	if err := json.Unmarshal([]byte(headers), &rec.Header); err != nil {
		return platform.IdempotencyRecord{}, err
	}
	rec.CreatedAt, rec.ExpiresAt = FromMillis(created), FromMillis(exp)
	return rec, nil
}

func (r idempotencyRepo) Put(ctx context.Context, rec platform.IdempotencyRecord) (bool, error) {
	h := rec.Header
	if h == nil {
		h = http.Header{}
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return false, err
	}
	body := rec.Body
	if body == nil {
		body = []byte{}
	}
	// An existing row is replaced only if it has already expired.
	res, err := r.q.ExecContext(ctx, `INSERT INTO idempotency_keys
		(user_id, key, request_hash, status, headers, body, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET
			request_hash = excluded.request_hash, status = excluded.status,
			headers = excluded.headers, body = excluded.body,
			created_at = excluded.created_at, expires_at = excluded.expires_at
		WHERE idempotency_keys.expires_at <= excluded.created_at`,
		rec.UserID.String(), rec.Key, rec.RequestHash, rec.Status, string(hb), body,
		Millis(rec.CreatedAt), Millis(rec.ExpiresAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r idempotencyRepo) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ?`, Millis(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
