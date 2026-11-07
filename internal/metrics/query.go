// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"encoding/base64"
	"sort"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// TagResolver turns a tag reference (ID, slug or alias) into the owner's tag
// ID. The tags service implements it.
type TagResolver interface {
	ResolveTagFilter(ctx context.Context, ownerID domain.ID, ref string) (id domain.ID, found bool, err error)
}

// DefaultWeakProblems is how many problems WeakProblems returns when no limit
// is given.
const DefaultWeakProblems = 20

// QueryService serves the read side of the mastery metrics: the overview, the
// per-tag figures, per-tag history and the weakest problems. Every method
// reads in one transaction and never writes.
//
// Recall decays with time, so a stored mastery row is only as fresh as the last
// attempt or snapshot pass. The service therefore predicts recall at the
// service clock with Compute, the function the projection itself uses, over
// the owner's current cards: the figures equal what a rebuild of the tag_mastery
// rows would store at this instant.
type QueryService struct {
	tx    SnapshotTransactor
	clock domain.Clock
	tags  TagResolver
}

// NewQueryService builds a QueryService. A nil resolver makes tag history
// unavailable (every tag is reported as not found).
func NewQueryService(tx SnapshotTransactor, clock domain.Clock, tags TagResolver) *QueryService {
	return &QueryService{tx: tx, clock: clock, tags: tags}
}

// Rate is a retained / reviews ratio. Rate is null when there were no reviews.
type Rate struct {
	Reviews  int      `json:"reviews"`
	Retained int      `json:"retained"`
	Rate     *float64 `json:"rate"`
}

func newRate(r Retention) Rate {
	out := Rate{Reviews: r.Reviews, Retained: r.Retained}
	if v, ok := r.Rate(); ok {
		out.Rate = &v
	}
	return out
}

// TrueRetentionView is true retention over a window, to compare with the
// desired retention.
type TrueRetentionView struct {
	WindowDays       int      `json:"window_days"`
	DesiredRetention float64  `json:"desired_retention"`
	Reviews          int      `json:"reviews"`
	Retained         int      `json:"retained"`
	Rate             *float64 `json:"rate"`
}

// DueDayView is the number of cards due on one study day.
type DueDayView struct {
	Date  string `json:"date"`
	Cards int    `json:"cards"`
}

// ActivityDayView is what was done on one study day.
type ActivityDayView struct {
	Date     string `json:"date"`
	Reviews  int    `json:"reviews"`
	NewCards int    `json:"new_cards"`
}

// OverviewView is the user-level metrics.
type OverviewView struct {
	AsOf     time.Time `json:"as_of"`
	StudyDay string    `json:"study_day"`
	// Tags is the number of active tags; TagsStudied how many of them have a
	// studied card.
	Tags        int `json:"tags"`
	TagsStudied int `json:"tags_studied"`
	// CardsStudied is the number of live problems with a reviewed card; Recall
	// is their mean predicted recall now, null when there are none.
	CardsStudied  int               `json:"cards_studied"`
	Recall        *float64          `json:"recall"`
	TrueRetention TrueRetentionView `json:"true_retention"`
	DueLoad       []DueDayView      `json:"due_load"`
	Activity      []ActivityDayView `json:"activity"`
}

// TagMasteryView is one active tag's mastery now.
type TagMasteryView struct {
	TagID domain.ID `json:"tag_id"`
	Slug  string    `json:"slug"`
	Name  string    `json:"name"`
	// Recall is the mean predicted recall of the studied cards, null when none.
	Recall          *float64 `json:"recall"`
	Coverage        float64  `json:"coverage"`
	StabilityMedian float64  `json:"stability_median"`
	LapseRate       float64  `json:"lapse_rate"`
	CardsTotal      int      `json:"cards_total"`
	CardsSeen       int      `json:"cards_seen"`
	Reviews         int      `json:"reviews"`
	Lapses          int      `json:"lapses"`
	// TrueRetention is over the default window.
	TrueRetention Rate       `json:"true_retention"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
}

// HistoryPoint is one tag's recall on one study day.
type HistoryPoint struct {
	Date      string  `json:"date"`
	Recall    float64 `json:"recall"`
	CardsSeen int     `json:"cards_seen"`
}

// TagHistoryView is a tag's daily snapshots.
type TagHistoryView struct {
	TagID domain.ID      `json:"tag_id"`
	Slug  string         `json:"slug"`
	Name  string         `json:"name"`
	From  *string        `json:"from"`
	To    *string        `json:"to"`
	Items []HistoryPoint `json:"items"`
}

// WeakProblemView is a studied problem and its predicted recall now.
type WeakProblemView struct {
	ProblemID domain.ID `json:"problem_id"`
	Type      string    `json:"type"`
	Prompt    string    `json:"prompt"`
	Recall    float64   `json:"recall"`
	Stability float64   `json:"stability"`
	Due       time.Time `json:"due"`
	Reps      int       `json:"reps"`
	Lapses    int       `json:"lapses"`
	// Tags are the slugs of the problem's active tags.
	Tags []string `json:"tags"`
}

func ptr(v float64) *float64 { return &v }

// Overview returns the user-level metrics at the service clock.
func (s *QueryService) Overview(ctx context.Context, userID domain.ID) (_ OverviewView, err error) {
	ctx, end := platform.TraceMethod(ctx, "metrics.QueryService.Overview")
	defer end(&err)
	now := s.clock.Now()
	var out OverviewView
	err = s.tx.InTx(ctx, func(sc SnapshotScope) error {
		cfg, err := sc.Settings.Get(ctx, userID)
		if err != nil {
			return err
		}
		st, err := userStatsIn(ctx, sc.Scope, now, userID, cfg, StatsOptions{})
		if err != nil {
			return err
		}
		params, err := sc.Scope.Params.Params(ctx, userID)
		if err != nil {
			return err
		}
		cards, err := sc.Scope.Repo.StudiedCards(ctx, userID)
		if err != nil {
			return err
		}
		tagMastery, err := s.tagViews(ctx, sc, userID, now, st.ByTag)
		if err != nil {
			return err
		}

		out = OverviewView{
			AsOf: now, StudyDay: st.Day.Date, Tags: len(tagMastery), CardsStudied: len(cards),
			TrueRetention: TrueRetentionView{WindowDays: st.RetentionDays, DesiredRetention: params.DesiredRetention,
				Reviews: st.Retention.Reviews, Retained: st.Retention.Retained},
			DueLoad:  make([]DueDayView, len(st.DueLoad)),
			Activity: make([]ActivityDayView, len(st.Activity)),
		}
		if v, ok := st.Retention.Rate(); ok {
			out.TrueRetention.Rate = &v
		}
		var sum float64
		for _, pc := range cards {
			sum += params.Retrievability(pc.Card.Card, now)
		}
		if len(cards) > 0 {
			out.Recall = ptr(sum / float64(len(cards)))
		}
		for _, t := range tagMastery {
			if t.CardsSeen > 0 {
				out.TagsStudied++
			}
		}
		for i, d := range st.DueLoad {
			out.DueLoad[i] = DueDayView{Date: d.Date, Cards: d.Cards}
		}
		for i, a := range st.Activity {
			out.Activity[i] = ActivityDayView{Date: a.Date, Reviews: a.Reviews, NewCards: a.NewCards}
		}
		return nil
	})
	return out, err
}

// Tags returns one page of the owner's active tags with their mastery now,
// ordered by tag ID. cursor is a previous page's next cursor; limit is the page
// size (platform.DefaultLimit when not positive).
func (s *QueryService) Tags(ctx context.Context, userID domain.ID, cursor string, limit int) (_ platform.PageResult[TagMasteryView], err error) {
	ctx, end := platform.TraceMethod(ctx, "metrics.QueryService.Tags")
	defer end(&err)
	var after domain.ID
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err == nil {
			after, err = domain.ParseID(string(b))
		}
		if err != nil {
			return platform.PageResult[TagMasteryView]{}, platform.Validation("invalid list parameters",
				platform.FieldError{Field: "cursor", Detail: "invalid cursor"})
		}
	}
	if limit <= 0 {
		limit = platform.DefaultLimit
	}
	now := s.clock.Now()
	var all []TagMasteryView
	err = s.tx.InTx(ctx, func(sc SnapshotScope) (err error) {
		cfg, err := sc.Settings.Get(ctx, userID)
		if err != nil {
			return err
		}
		st, err := userStatsIn(ctx, sc.Scope, now, userID, cfg, StatsOptions{ActivityDays: 1})
		if err != nil {
			return err
		}
		all, err = s.tagViews(ctx, sc, userID, now, st.ByTag)
		return err
	})
	if err != nil {
		return platform.PageResult[TagMasteryView]{}, err
	}
	if cursor != "" {
		i := sort.Search(len(all), func(i int) bool { return all[i].TagID.String() > after.String() })
		all = all[i:]
	}
	next := ""
	if len(all) > limit {
		all = all[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(all[limit-1].TagID.String()))
	}
	return platform.NewPageResult(all, next), nil
}

// tagViews computes the mastery of every active tag at now, ordered by tag ID.
// retention is each tag's true retention over the default window.
func (s *QueryService) tagViews(ctx context.Context, sc SnapshotScope, userID domain.ID, now time.Time, retention map[domain.ID]Retention) ([]TagMasteryView, error) {
	labels, err := sc.Scope.Repo.TagLabels(ctx, userID)
	if err != nil || len(labels) == 0 {
		return nil, err
	}
	// Sorted here, not trusted to the store's collation, so that the cursor
	// comparison in Tags agrees with the order.
	sort.Slice(labels, func(i, j int) bool { return labels[i].ID.String() < labels[j].ID.String() })
	ids := make([]domain.ID, len(labels))
	for i, l := range labels {
		ids[i] = l.ID
	}
	params, err := sc.Scope.Params.Params(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	cards, err := sc.Scope.Repo.TagCards(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	last, err := sc.Scope.Repo.LastAttempts(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	byTag := make(map[domain.ID][]TagCard, len(ids))
	for _, tc := range cards {
		byTag[tc.TagID] = append(byTag[tc.TagID], tc)
	}
	out := make([]TagMasteryView, len(labels))
	for i, l := range labels {
		m := Compute(params, now, userID, l.ID, byTag[l.ID], last[l.ID])
		v := TagMasteryView{TagID: l.ID, Slug: l.Slug, Name: l.Name, Coverage: m.Coverage(),
			StabilityMedian: m.StabilityMedian, LapseRate: m.LapseRate(), CardsTotal: m.CardsTotal,
			CardsSeen: m.CardsSeen, Reviews: m.Reviews, Lapses: m.Lapses,
			TrueRetention: newRate(retention[l.ID])}
		if m.CardsSeen > 0 {
			v.Recall = ptr(m.Recall)
		}
		if !m.LastAttemptAt.IsZero() {
			t := m.LastAttemptAt
			v.LastAttemptAt = &t
		}
		out[i] = v
	}
	return out, nil
}

// TagHistory returns the daily snapshots of the tag (ID, slug or alias) with
// from <= date <= to, oldest first. Dates are study days, YYYY-MM-DD in the
// user's time zone, the convention the snapshot job writes; an empty bound is
// open. A tag that is not an active tag of the owner is a 404 problem.
func (s *QueryService) TagHistory(ctx context.Context, userID domain.ID, ref, from, to string) (_ TagHistoryView, err error) {
	ctx, end := platform.TraceMethod(ctx, "metrics.QueryService.TagHistory")
	defer end(&err)
	var fields []platform.FieldError
	for _, b := range []struct{ name, v string }{{"from", from}, {"to", to}} {
		if b.v == "" {
			continue
		}
		if t, err := time.Parse("2006-01-02", b.v); err != nil || t.Format("2006-01-02") != b.v {
			fields = append(fields, platform.FieldError{Field: b.name, Detail: "must be a date as YYYY-MM-DD"})
		}
	}
	if len(fields) == 0 && from != "" && to != "" && from > to {
		fields = append(fields, platform.FieldError{Field: "from", Detail: "must not be after to"})
	}
	if len(fields) > 0 {
		return TagHistoryView{}, platform.Validation("invalid history parameters", fields...)
	}
	if s.tags == nil {
		return TagHistoryView{}, platform.NotFound("no such tag")
	}
	id, found, err := s.tags.ResolveTagFilter(ctx, userID, ref)
	if err != nil {
		return TagHistoryView{}, err
	}
	if !found {
		return TagHistoryView{}, platform.NotFound("no such tag")
	}
	out := TagHistoryView{TagID: id, Items: []HistoryPoint{}}
	if from != "" {
		out.From = &from
	}
	if to != "" {
		out.To = &to
	}
	err = s.tx.InTx(ctx, func(sc SnapshotScope) error {
		labels, err := sc.Scope.Repo.TagLabels(ctx, userID)
		if err != nil {
			return err
		}
		known := false
		for _, l := range labels {
			if l.ID == id {
				out.Slug, out.Name, known = l.Slug, l.Name, true
			}
		}
		if !known {
			return platform.NotFound("no such tag")
		}
		snaps, err := sc.Snapshots.List(ctx, userID, id, from, to)
		if err != nil {
			return err
		}
		for _, sn := range snaps {
			out.Items = append(out.Items, HistoryPoint{Date: sn.Date, Recall: sn.Recall, CardsSeen: sn.CardsSeen})
		}
		return nil
	})
	return out, err
}

// WeakProblems returns the owner's studied live problems with the lowest
// predicted recall now, weakest first (ties by problem ID), at most limit of
// them (DefaultWeakProblems when not positive).
func (s *QueryService) WeakProblems(ctx context.Context, userID domain.ID, limit int) (_ []WeakProblemView, err error) {
	ctx, end := platform.TraceMethod(ctx, "metrics.QueryService.WeakProblems")
	defer end(&err)
	if limit <= 0 {
		limit = DefaultWeakProblems
	}
	now := s.clock.Now()
	var out []WeakProblemView
	err = s.tx.InTx(ctx, func(sc SnapshotScope) error {
		params, err := sc.Scope.Params.Params(ctx, userID)
		if err != nil {
			return err
		}
		if err := params.Validate(); err != nil {
			return err
		}
		cards, err := sc.Scope.Repo.StudiedCards(ctx, userID)
		if err != nil {
			return err
		}
		for _, pc := range cards {
			out = append(out, WeakProblemView{ProblemID: pc.ProblemID, Type: pc.Type, Prompt: pc.Prompt,
				Recall: params.Retrievability(pc.Card.Card, now), Stability: pc.Card.Stability, Due: pc.Card.Due,
				Reps: pc.Card.Reps, Lapses: pc.Card.Lapses, Tags: []string{}})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Recall < out[j].Recall })
		if len(out) > limit {
			out = out[:limit]
		}
		if len(out) == 0 {
			return nil
		}
		labels, err := sc.Scope.Repo.TagLabels(ctx, userID)
		if err != nil {
			return err
		}
		slug := make(map[domain.ID]string, len(labels))
		for _, l := range labels {
			slug[l.ID] = l.Slug
		}
		byProblem, err := sc.Scope.Repo.ProblemTags(ctx, userID)
		if err != nil {
			return err
		}
		for i := range out {
			for _, t := range byProblem[out[i].ProblemID] {
				if sl, ok := slug[t]; ok {
					out[i].Tags = append(out[i].Tags, sl)
				}
			}
			sort.Strings(out[i].Tags)
		}
		return nil
	})
	if out == nil {
		out = []WeakProblemView{}
	}
	return out, err
}
