// SPDX-License-Identifier: Apache-2.0

package quizzes

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Status is a quiz's lifecycle state.
type Status string

// Quiz statuses.
const (
	StatusDraft    Status = "draft"
	StatusLive     Status = "live"
	StatusArchived Status = "archived"
)

// Valid reports whether s is a defined status.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusLive, StatusArchived:
		return true
	}
	return false
}

// Quiz is an authored, ordered set of problems. Items holds problem IDs in
// quiz order (position = index).
type Quiz struct {
	ID          domain.ID
	OwnerID     domain.ID
	Title       string
	Description string
	Status      Status
	CreatedBy   domain.Actor
	Items       []domain.ID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ETag is a strong validator derived from every mutable field, including the
// ordered items, so any change to the quiz yields a new value.
func ETag(q Quiz) string {
	h := sha256.New()
	for _, s := range []string{q.ID.String(), q.Title, q.Description, string(q.Status),
		strconv.FormatInt(q.UpdatedAt.UnixMilli(), 10), strconv.Itoa(len(q.Items))} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, id := range q.Items {
		h.Write([]byte(id.String()))
		h.Write([]byte{0})
	}
	return `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
}
