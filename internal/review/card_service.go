// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// TagResolver turns a tag reference (ID, slug or alias) into the owner's tag
// ID. The tags service implements it.
type TagResolver interface {
	ResolveTagFilter(ctx context.Context, ownerID domain.ID, ref string) (id domain.ID, found bool, err error)
}

// CardService serves the read side of the card projection.
type CardService struct {
	cards Repo
	tags  TagResolver
}

// NewCardService builds a CardService over the card repo. A nil resolver
// refuses the tag filter with a validation error.
func NewCardService(cards Repo, tags TagResolver) *CardService {
	return &CardService{cards: cards, tags: tags}
}

// CardView is a card as the API shows it.
type CardView struct {
	ProblemID  domain.ID  `json:"problem_id"`
	CardEpoch  int        `json:"card_epoch"`
	State      fsrs.State `json:"state"`
	Step       int        `json:"step"`
	Stability  float64    `json:"stability"`
	Difficulty float64    `json:"difficulty"`
	Due        time.Time  `json:"due"`
	LastReview *time.Time `json:"last_review"`
	Reps       int        `json:"reps"`
	Lapses     int        `json:"lapses"`
}

func newCardView(c Card) CardView {
	v := CardView{ProblemID: c.ProblemID, CardEpoch: c.Epoch, State: c.State, Step: c.Step, Stability: c.Stability,
		Difficulty: c.Difficulty, Due: c.Due, Reps: c.Reps, Lapses: c.Lapses}
	if !c.LastReview.IsZero() {
		t := c.LastReview
		v.LastReview = &t
	}
	return v
}

// CardQuery is the parsed query of GET /cards.
type CardQuery struct {
	DueBefore string // RFC 3339, exclusive
	Tag       string // tag ID, slug or alias
	Cursor    string
	Limit     int
}

// Get returns the owner's card for a problem, or a 404 problem when there is
// none: the problem is unknown, or has not been answered, or is not live.
func (s *CardService) Get(ctx context.Context, ownerID, problemID domain.ID) (_ CardView, err error) {
	ctx, end := platform.TraceMethod(ctx, "review.CardService.Get")
	defer end(&err)
	c, err := s.cards.Get(ctx, ownerID, problemID)
	if errors.Is(err, ErrNotFound) {
		return CardView{}, platform.NotFound("no card for this problem")
	}
	if err != nil {
		return CardView{}, err
	}
	return newCardView(c), nil
}

// List returns one page of the owner's cards ordered by problem ID.
func (s *CardService) List(ctx context.Context, ownerID domain.ID, q CardQuery) (_ platform.PageResult[CardView], err error) {
	ctx, end := platform.TraceMethod(ctx, "review.CardService.List")
	defer end(&err)
	var fields []platform.FieldError
	var f CardFilter
	if q.DueBefore != "" {
		t, err := time.Parse(time.RFC3339, q.DueBefore)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "due_before", Detail: "must be an RFC 3339 timestamp"})
		}
		f.DueBefore = t
	}
	if q.Tag != "" {
		if s.tags == nil {
			fields = append(fields, platform.FieldError{Field: "tag", Detail: "filtering by tag is not enabled"})
		} else {
			id, found, err := s.tags.ResolveTagFilter(ctx, ownerID, q.Tag)
			switch {
			case err != nil:
				return platform.PageResult[CardView]{}, err
			case !found:
				fields = append(fields, platform.FieldError{Field: "tag", Detail: "no such tag"})
			default:
				f.TagID = id
			}
		}
	}
	if q.Cursor != "" {
		after, err := decodeCardCursor(q.Cursor)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "cursor", Detail: "invalid cursor"})
		}
		f.After = after
	}
	if len(fields) > 0 {
		return platform.PageResult[CardView]{}, platform.Validation("invalid list parameters", fields...)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = platform.DefaultLimit
	}
	f.Limit = limit + 1 // one extra row says whether another page follows
	rows, err := s.cards.Query(ctx, ownerID, f)
	if err != nil {
		return platform.PageResult[CardView]{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCardCursor(rows[limit-1].ProblemID)
	}
	items := make([]CardView, len(rows))
	for i, c := range rows {
		items[i] = newCardView(c)
	}
	return platform.NewPageResult(items, next), nil
}

func encodeCardCursor(id domain.ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func decodeCardCursor(c string) (domain.ID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return domain.Nil, err
	}
	return domain.ParseID(string(b))
}
