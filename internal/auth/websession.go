// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// WebSession is a server-side browser session. The cookie carries an opaque
// token; only its hash is stored.
type WebSession struct {
	ID         domain.ID
	UserID     domain.ID
	TokenHash  string
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// WebSessionRepo persists web sessions.
type WebSessionRepo interface {
	// Create inserts a session.
	Create(ctx context.Context, s WebSession) error
	// GetByTokenHash resolves a cookie token's hash to its session, or
	// ErrNotFound. Like Identities.GetByIssuerSubject it is the lookup made
	// before the owner is known; it is how the owner is established.
	GetByTokenHash(ctx context.Context, tokenHash string) (WebSession, error)
	// Touch sets last_seen_at and expires_at on one of the user's sessions.
	Touch(ctx context.Context, userID, sessionID domain.ID, lastSeen, expiresAt time.Time) error
	// Delete removes one of the user's sessions, or returns ErrNotFound.
	Delete(ctx context.Context, userID, sessionID domain.ID) error
}

// LoginState is the pre-login state of one authorization request.
type LoginState struct {
	StateHash    string
	Nonce        string
	CodeVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

// LoginStateRepo persists pre-login state. No user exists yet, so these
// methods have no owner; the state value is an unguessable single-use key.
type LoginStateRepo interface {
	// Create stores a state.
	Create(ctx context.Context, s LoginState) error
	// Consume atomically removes and returns the state, or ErrNotFound when it
	// is unknown, already consumed, or expired at now.
	Consume(ctx context.Context, stateHash string, now time.Time) (LoginState, error)
	// PurgeExpired removes states that expired at or before now.
	PurgeExpired(ctx context.Context, now time.Time) error
}

// SessionRepos is what the login service needs from the store; store.Store
// satisfies it.
type SessionRepos interface {
	WebSessions() WebSessionRepo
	LoginStates() LoginStateRepo
}
