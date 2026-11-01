// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Status is a tag's lifecycle state.
type Status string

// Tag statuses.
const (
	StatusActive  Status = "active"
	StatusMerged  Status = "merged"
	StatusRetired Status = "retired"
)

// Valid reports whether s is a defined status.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusMerged, StatusRetired:
		return true
	}
	return false
}

// Tag labels problems. Its slug is unique among the owner's active tags; old
// slugs stay resolvable through Aliases.
type Tag struct {
	ID          domain.ID
	OwnerID     domain.ID
	Slug        string
	Name        string
	Description string
	Status      Status
	MergedInto  domain.ID // Nil unless Status is StatusMerged
	Aliases     []string  // sorted
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ETag is a strong validator derived from every mutable field, so any change
// to the tag (including its aliases) yields a new value.
func ETag(t Tag) string {
	h := sha256.New()
	for _, s := range []string{
		t.ID.String(), t.Slug, t.Name, t.Description, string(t.Status), t.MergedInto.String(),
		strings.Join(t.Aliases, ","), strconv.FormatInt(t.UpdatedAt.UnixMilli(), 10),
	} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
}
