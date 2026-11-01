// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
)

// WebSessions implements store.Store.
func (s *Store) WebSessions() auth.WebSessionRepo { return webSessionRepo{s.q} }

// LoginStates implements store.Store.
func (s *Store) LoginStates() auth.LoginStateRepo { return loginStateRepo{s.q} }

type webSessionRepo struct{ q execer }

func (r webSessionRepo) Create(ctx context.Context, s auth.WebSession) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO web_sessions
		(id, user_id, token_hash, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID.String(), s.UserID.String(), s.TokenHash, s.CSRFToken,
		Millis(s.CreatedAt), Millis(s.LastSeenAt), Millis(s.ExpiresAt))
	return err
}

func (r webSessionRepo) GetByTokenHash(ctx context.Context, tokenHash string) (auth.WebSession, error) {
	var id, uid string
	var created, seen, exp int64
	s := auth.WebSession{TokenHash: tokenHash}
	err := r.q.QueryRowContext(ctx, `SELECT id, user_id, csrf_token, created_at, last_seen_at, expires_at
		FROM web_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&id, &uid, &s.CSRFToken, &created, &seen, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.WebSession{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.WebSession{}, err
	}
	if s.ID, err = domain.ParseID(id); err != nil {
		return auth.WebSession{}, err
	}
	if s.UserID, err = domain.ParseID(uid); err != nil {
		return auth.WebSession{}, err
	}
	s.CreatedAt, s.LastSeenAt, s.ExpiresAt = FromMillis(created), FromMillis(seen), FromMillis(exp)
	return s, nil
}

func (r webSessionRepo) Touch(ctx context.Context, userID, sessionID domain.ID, lastSeen, expiresAt time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE web_sessions SET last_seen_at = ?, expires_at = ?
		WHERE id = ? AND user_id = ?`, Millis(lastSeen), Millis(expiresAt), sessionID.String(), userID.String())
	return requireRow(res, err)
}

func (r webSessionRepo) Delete(ctx context.Context, userID, sessionID domain.ID) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM web_sessions WHERE id = ? AND user_id = ?`,
		sessionID.String(), userID.String())
	return requireRow(res, err)
}

type loginStateRepo struct{ q execer }

func (r loginStateRepo) Create(ctx context.Context, s auth.LoginState) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO login_states (state_hash, nonce, code_verifier, return_to, expires_at)
		VALUES (?, ?, ?, ?, ?)`, s.StateHash, s.Nonce, s.CodeVerifier, s.ReturnTo, Millis(s.ExpiresAt))
	return err
}

func (r loginStateRepo) Consume(ctx context.Context, stateHash string, now time.Time) (auth.LoginState, error) {
	s := auth.LoginState{StateHash: stateHash}
	var exp int64
	err := r.q.QueryRowContext(ctx, `DELETE FROM login_states WHERE state_hash = ?
		RETURNING nonce, code_verifier, return_to, expires_at`, stateHash).
		Scan(&s.Nonce, &s.CodeVerifier, &s.ReturnTo, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.LoginState{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.LoginState{}, err
	}
	s.ExpiresAt = FromMillis(exp)
	if !now.Before(s.ExpiresAt) {
		return auth.LoginState{}, auth.ErrNotFound
	}
	return s, nil
}

func (r loginStateRepo) PurgeExpired(ctx context.Context, now time.Time) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM login_states WHERE expires_at <= ?`, Millis(now))
	return err
}
