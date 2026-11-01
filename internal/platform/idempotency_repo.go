// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// IdempotencyTTL is how long a stored response is replayed.
const IdempotencyTTL = 24 * time.Hour

// ErrIdempotencyNotFound is returned by IdempotencyRepo.Get when the user has
// no unexpired record for the key.
var ErrIdempotencyNotFound = errors.New("platform: idempotency key not found")

// IdempotencyRecord is one stored response, keyed by (UserID, Key).
type IdempotencyRecord struct {
	UserID domain.ID
	Key    string
	// RequestHash fingerprints the request (method, path, query, body).
	RequestHash string
	Status      int
	// Header holds the replayed response headers (Content-Type, Location, ETag).
	Header    http.Header
	Body      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IdempotencyRepo persists stored responses. Keys are scoped to the owner.
type IdempotencyRepo interface {
	// Get returns the user's record for key if it has not expired at now,
	// or ErrIdempotencyNotFound.
	Get(ctx context.Context, userID domain.ID, key string, now time.Time) (IdempotencyRecord, error)
	// Put stores rec unless the user already has an unexpired record for the
	// key, reporting whether it stored. An expired record is replaced.
	Put(ctx context.Context, rec IdempotencyRecord) (bool, error)
	// PurgeExpired deletes every record that has expired at now, for all
	// users, and returns how many it removed. It is the expiry sweep and the
	// one maintenance method that is not owner scoped.
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}
