// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"encoding/json"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Status is a problem's lifecycle state.
type Status string

// Problem statuses.
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

// ChangeKind classifies a version: a minor edit keeps the card, a major edit
// starts a new card epoch.
type ChangeKind string

// Change kinds.
const (
	ChangeMinor ChangeKind = "minor"
	ChangeMajor ChangeKind = "major"
)

// Valid reports whether k is a defined change kind.
func (k ChangeKind) Valid() bool { return k == ChangeMinor || k == ChangeMajor }

// Problem is the mutable handle for a problem. Its content lives in versions.
type Problem struct {
	ID             domain.ID
	OwnerID        domain.ID
	Status         Status
	CurrentVersion int
	CreatedBy      domain.Actor
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time // non-nil once soft-deleted
}

// Version is one immutable revision of a problem.
type Version struct {
	ProblemID   domain.ID
	Version     int
	Type        string // TypeMCQ, TypeCloze or TypeOpen
	Prompt      string
	Payload     json.RawMessage
	Explanation string
	Source      string
	ChangeKind  ChangeKind
	CreatedBy   domain.Actor
	CreatedAt   time.Time
}
