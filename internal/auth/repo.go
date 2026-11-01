// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound is returned when a user or identity does not exist.
var ErrNotFound = errors.New("auth: not found")

// Scheduling holds a user's overrides of the config scheduling defaults. A nil
// field means "use the configured default".
type Scheduling struct {
	DesiredRetention *float64
	LearningSteps    []string // nil means default; durations such as "1m"
	NewPerDay        *int
	MaxReviewsPerDay *int
	NewInterval      *int
	WeakTagBoost     *float64
	RolloverHour     *int
	Timezone         *string
}

// User is a reference row; profile fields are copied from claims.
type User struct {
	ID          domain.ID
	DisplayName string
	Email       string
	CreatedAt   time.Time
	Scheduling  Scheduling
}

// Identity binds a user to one (issuer, subject) pair.
type Identity struct {
	ID         domain.ID
	UserID     domain.ID
	Issuer     string
	Subject    string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// UserRepo persists users. Every method is scoped to the user it names: there
// is no listing and no cross-user read.
type UserRepo interface {
	// Create inserts a new user row.
	Create(ctx context.Context, u User) error
	// Get returns the user with the given ID, or ErrNotFound.
	Get(ctx context.Context, userID domain.ID) (User, error)
	// UpdateProfile replaces the claim-derived display name and email.
	UpdateProfile(ctx context.Context, userID domain.ID, displayName, email string) error
}

// IdentityRepo persists identities.
type IdentityRepo interface {
	// GetByIssuerSubject resolves a token's (issuer, subject) to its identity,
	// or ErrNotFound. This is the one lookup made before the owner is known; it
	// is how the owner is established.
	GetByIssuerSubject(ctx context.Context, issuer, subject string) (Identity, error)
	// InsertIfAbsent inserts the identity unless (issuer, subject) already
	// exists, reporting whether it inserted. It never overwrites.
	InsertIfAbsent(ctx context.Context, id Identity) (bool, error)
	// TouchLastSeen sets last_seen_at on one of the user's identities.
	TouchLastSeen(ctx context.Context, userID, identityID domain.ID, at time.Time) error
}
