// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// TagResolver turns a tag reference (ID, slug or alias) into the owner's tag
// ID. The tags service implements it.
type TagResolver interface {
	ResolveTagFilter(ctx context.Context, ownerID domain.ID, ref string) (id domain.ID, found bool, err error)
}

// WithTagResolver enables the tag filter of List. Without it a request that
// sets a tag is answered 422.
func WithTagResolver(r TagResolver) Option { return func(s *Service) { s.tags = r } }

// WithReader sets the repo that serves reads (List and Get). Without it reads
// run through the transactor.
func WithReader(r Repo) Option { return func(s *Service) { s.reads = r } }

// read runs fn against the read repo, or inside a transaction when none is set.
func (s *Service) read(ctx context.Context, fn func(Repo) error) error {
	if s.reads != nil {
		return fn(s.reads)
	}
	return s.tx.InTx(ctx, fn)
}

// ListQuery is the parsed query of GET /attempts.
type ListQuery struct {
	Problem string // problem ID
	Tag     string // tag ID, slug or alias
	Since   string // RFC 3339, inclusive
	Until   string // RFC 3339, exclusive
	Cursor  string
	Limit   int
}

// Get returns one of the owner's attempts.
func (s *Service) Get(ctx context.Context, ownerID, id domain.ID) (View, error) {
	var a Attempt
	err := s.read(ctx, func(r Repo) (err error) {
		a, err = r.Get(ctx, ownerID, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return View{}, platform.NotFound("no such attempt")
	}
	if err != nil {
		return View{}, err
	}
	return newView(a), nil
}

// List returns one page of the owner's attempts, oldest first.
func (s *Service) List(ctx context.Context, ownerID domain.ID, q ListQuery) (platform.PageResult[View], error) {
	var fields []platform.FieldError
	var f ListFilter
	if q.Problem != "" {
		id, err := domain.ParseID(q.Problem)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "problem", Detail: "not a valid ID"})
		}
		f.ProblemID = id
	}
	if q.Tag != "" {
		if s.tags == nil {
			fields = append(fields, platform.FieldError{Field: "tag", Detail: "filtering by tag is not enabled"})
		} else {
			id, found, err := s.tags.ResolveTagFilter(ctx, ownerID, q.Tag)
			switch {
			case err != nil:
				return platform.PageResult[View]{}, err
			case !found:
				fields = append(fields, platform.FieldError{Field: "tag", Detail: "no such tag"})
			default:
				f.TagID = id
			}
		}
	}
	parseTime := func(field, v string) time.Time {
		if v == "" {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: field, Detail: "must be an RFC 3339 timestamp"})
		}
		return t
	}
	f.Since = parseTime("since", q.Since)
	f.Until = parseTime("until", q.Until)
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		fields = append(fields, platform.FieldError{Field: "until", Detail: "must be after since"})
	}
	if q.Cursor != "" {
		after, err := decodeCursor(q.Cursor)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "cursor", Detail: "invalid cursor"})
		}
		f.After = after
	}
	if len(fields) > 0 {
		return platform.PageResult[View]{}, platform.Validation("invalid list parameters", fields...)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = platform.DefaultLimit
	}
	f.Limit = limit + 1 // one extra row says whether another page follows
	var rows []Attempt
	err := s.read(ctx, func(r Repo) (err error) {
		rows, err = r.List(ctx, ownerID, f)
		return err
	})
	if err != nil {
		return platform.PageResult[View]{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(rows[limit-1].ID)
	}
	items := make([]View, len(rows))
	for i, a := range rows {
		items[i] = newView(a)
	}
	return platform.NewPageResult(items, next), nil
}

func encodeCursor(id domain.ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func decodeCursor(c string) (domain.ID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return domain.Nil, err
	}
	return domain.ParseID(string(b))
}
