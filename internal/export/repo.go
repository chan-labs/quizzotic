// SPDX-License-Identifier: Apache-2.0

package export

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Errors returned by the service.
var (
	// ErrNotFound: there is no such user to export.
	ErrNotFound = errors.New("export: no such user")
	// ErrNotEmpty: an import needs an empty instance, and this one has users.
	ErrNotEmpty = errors.New("export: the instance is not empty")
	// ErrInvalid: the document is not a valid export; the error wraps the reason.
	ErrInvalid = errors.New("export: invalid document")
)

// Row is one table row: column name to a value of the column's Kind (string,
// int64, float64, bool, time.Time in UTC or json.RawMessage), nil for NULL.
type Row map[string]any

// Repo reads and writes whole rows of the tables in Tables. Backends build
// their SQL from the Table description, so adding a column is a change to
// schema.go and a migration, nothing else.
type Repo interface {
	// Read returns up to limit of the owner's rows of t in key order, those
	// after the row after (nil: from the start; otherwise a row previously
	// returned, of which only the key columns are used).
	Read(ctx context.Context, owner domain.ID, t Table, after Row, limit int) ([]Row, error)

	// Insert writes rows of t exactly as given.
	Insert(ctx context.Context, t Table, rows []Row) error

	// SetColumn sets one column of the row of t with the given key columns.
	SetColumn(ctx context.Context, t Table, key Row, column string, value any) error

	// HasUsers reports whether any user exists, in any owner's scope. An
	// import is allowed only into an instance without users.
	HasUsers(ctx context.Context) (bool, error)
}

// Transactor runs fn with a Repo bound to one store transaction.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

// Rebuilder recomputes the projections (cards, mastery and its history) of a
// user from the imported attempt log, after the import has committed.
type Rebuilder interface {
	Rebuild(ctx context.Context, userID domain.ID) error
}
