// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"net/http"
	"strconv"
)

// Pagination bounds from the REST conventions.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is the parsed cursor/limit query.
type Page struct {
	Cursor string
	Limit  int
}

// ParsePage reads ?cursor=&limit=. A missing limit yields DefaultLimit; a
// non-integer or out-of-range (<1 or >MaxLimit) limit is a validation error.
// Cursors are opaque to the platform; the repo that issued one validates it.
func ParsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()
	p := Page{Cursor: q.Get("cursor"), Limit: DefaultLimit}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxLimit {
			return Page{}, Validation("invalid pagination parameters", FieldError{
				Field:  "limit",
				Detail: "must be an integer between 1 and " + strconv.Itoa(MaxLimit),
			})
		}
		p.Limit = n
	}
	return p, nil
}

// PageResult is the list response envelope. NextCursor is null on the last page.
type PageResult[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// NewPageResult builds the envelope. Items is never nil so it encodes as [].
// An empty nextCursor means there are no more pages.
func NewPageResult[T any](items []T, nextCursor string) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	res := PageResult[T]{Items: items}
	if nextCursor != "" {
		res.NextCursor = &nextCursor
	}
	return res
}
