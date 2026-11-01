// SPDX-License-Identifier: Apache-2.0

package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// ItemKind says why a problem is in a review session.
type ItemKind string

const (
	// ItemReview is a card that already exists and is due.
	ItemReview ItemKind = "review"
	// ItemNew is a live problem the user has no card for yet.
	ItemNew ItemKind = "new"
)

// Item is one entry of a review session's ordered list.
type Item struct {
	ProblemID domain.ID
	// Epoch is the card epoch the item is for.
	Epoch int
	Kind  ItemKind
}

// DefaultTagRecall is the mean recall assumed for a tag with no data (spec:
// "Tags with no data count as recall 0.5").
const DefaultTagRecall = 0.5

// TagRecall supplies the mean recall of a problem's tags, in [0,1], that
// weights the sampling of new problems. Until tag mastery exists (M7.6) the
// builder is wired with StubTagRecall.
type TagRecall interface {
	ProblemTagRecall(ctx context.Context, ownerID, problemID domain.ID) (float64, error)
}

// StubTagRecall reports DefaultTagRecall for every problem, which is what the
// spec prescribes for tags without data. M7.6 replaces it with real mastery.
type StubTagRecall struct{}

// ProblemTagRecall implements TagRecall.
func (StubTagRecall) ProblemTagRecall(context.Context, domain.ID, domain.ID) (float64, error) {
	return DefaultTagRecall, nil
}

// AttemptReader reads the attempt log. attempts.Repo implements it.
type AttemptReader interface {
	AttemptLog
	List(ctx context.Context, ownerID domain.ID, f attempts.ListFilter) ([]attempts.Attempt, error)
}

// SettingsSource resolves a user's effective scheduling settings.
// settings.Service implements it.
type SettingsSource interface {
	Get(ctx context.Context, userID domain.ID) (settings.Settings, error)
}

// BuildSource is everything the builder reads. It is read-only: building a
// session writes nothing.
type BuildSource struct {
	Cards    Repo
	Attempts AttemptReader
	Problems ProblemSource
	Params   ParamsSource
	Settings SettingsSource
	Tags     TagRecall
}

// Builder builds review sessions (spec: Review session builder). It is a pure
// function of the stored data, the clock and the session ID; persisting the
// result on a session is the session service's job (M4.7).
type Builder struct{ src BuildSource }

// NewBuilder builds a Builder over src. A nil src.Tags means StubTagRecall.
func NewBuilder(src BuildSource) *Builder {
	if src.Tags == nil {
		src.Tags = StubTagRecall{}
	}
	return &Builder{src}
}

// Usage is what the user has already done in the current study day.
type Usage struct {
	// Reviews counts answers to cards that already existed; NewCards counts
	// cards whose first answer fell in the day.
	Reviews, NewCards int
}

// Plan is a built session: the ordered list to freeze on the session.
type Plan struct {
	Items []Item
	// Day is the study day the limits were applied to, and Used what was
	// already spent in it before this session.
	Day  settings.StudyDay
	Used Usage
}

// Build returns the ordered item list for the review session sessionID at now.
//
// The same sessionID over the same stored data and now always yields the same
// list. Daily limits hold across sessions because they are measured against
// the attempt log of the user's study day, not against earlier sessions.
func (b *Builder) Build(ctx context.Context, userID, sessionID domain.ID, now time.Time) (Plan, error) {
	cfg, err := b.src.Settings.Get(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	day, err := cfg.StudyDay(now)
	if err != nil {
		return Plan{}, err
	}
	params, err := b.src.Params.Params(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	used, err := b.usage(ctx, userID, day)
	if err != nil {
		return Plan{}, err
	}
	live, err := b.liveEpochs(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	cards, err := b.src.Cards.List(ctx, userID)
	if err != nil {
		return Plan{}, err
	}

	// 1. Due cards, lowest recall first, within the review budget.
	type dueCard struct {
		c      Card
		recall float64
	}
	var due []dueCard
	hasCard := make(map[domain.ID]bool, len(cards))
	for _, c := range cards {
		if e, ok := live[c.ProblemID]; !ok || e != c.Epoch {
			continue // not live, or a stale-epoch row: never reviewed
		}
		hasCard[c.ProblemID] = true
		if !c.Due.After(now) {
			due = append(due, dueCard{c, params.Retrievability(c.Card, now)})
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].recall != due[j].recall {
			return due[i].recall < due[j].recall
		}
		return lessID(due[i].c.ProblemID, due[j].c.ProblemID)
	})
	if budget := max(0, cfg.MaxReviewsPerDay-used.Reviews); len(due) > budget {
		due = due[:budget]
	}
	reviews := make([]Item, len(due))
	for i, d := range due {
		reviews[i] = Item{ProblemID: d.c.ProblemID, Epoch: d.c.Epoch, Kind: ItemReview}
	}

	// 2-3. New problems (live, no card), sampled by weight within the budget.
	var cands []ProblemEpoch
	for id, e := range live {
		if !hasCard[id] {
			cands = append(cands, ProblemEpoch{ID: id, Live: true, Epoch: e})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return lessID(cands[i].ID, cands[j].ID) })
	news, err := b.sampleNew(ctx, userID, sessionID, cfg, cands, max(0, cfg.NewPerDay-used.NewCards))
	if err != nil {
		return Plan{}, err
	}

	// 4. Interleave.
	return Plan{Items: interleave(reviews, news, cfg.NewInterval), Day: day, Used: used}, nil
}

// AppendDue returns the learning-step cards that have come due at now and are
// not already waiting in the session, ordered by due time. The session service
// appends them to the frozen list mid-session; they do not count against the
// daily review cap, which only limits the list a session starts with.
// outstanding is the set of problems on the list that have not been answered
// yet; a card answered earlier in the session and due again is appended anew.
func (b *Builder) AppendDue(ctx context.Context, userID domain.ID, outstanding []domain.ID, now time.Time) ([]Item, error) {
	waiting := make(map[domain.ID]bool, len(outstanding))
	for _, id := range outstanding {
		waiting[id] = true
	}
	live, err := b.liveEpochs(ctx, userID)
	if err != nil {
		return nil, err
	}
	cards, err := b.src.Cards.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	var got []Card
	for _, c := range cards {
		if e, ok := live[c.ProblemID]; !ok || e != c.Epoch || waiting[c.ProblemID] {
			continue
		}
		if (c.State == fsrs.Learning || c.State == fsrs.Relearning) && !c.Due.After(now) {
			got = append(got, c)
		}
	}
	sort.SliceStable(got, func(i, j int) bool {
		if !got[i].Due.Equal(got[j].Due) {
			return got[i].Due.Before(got[j].Due)
		}
		return lessID(got[i].ProblemID, got[j].ProblemID)
	})
	out := make([]Item, len(got))
	for i, c := range got {
		out[i] = Item{ProblemID: c.ProblemID, Epoch: c.Epoch, Kind: ItemReview}
	}
	return out, nil
}

// liveEpochs maps every live problem of the user to its card epoch.
func (b *Builder) liveEpochs(ctx context.Context, userID domain.ID) (map[domain.ID]int, error) {
	list, err := b.src.Problems.LiveEpochs(ctx, userID)
	if err != nil {
		return nil, err
	}
	m := make(map[domain.ID]int, len(list))
	for _, pe := range list {
		if pe.Live {
			m[pe.ID] = pe.Epoch
		}
	}
	return m, nil
}

// usage counts the answers given in the study day. A card's first answer is
// the introduction of a new card; every other answer is a review.
func (b *Builder) usage(ctx context.Context, userID domain.ID, day settings.StudyDay) (Usage, error) {
	today, err := b.src.Attempts.List(ctx, userID, attempts.ListFilter{Since: day.Start, Until: day.End})
	if err != nil {
		return Usage{}, err
	}
	type cardKey struct {
		problem domain.ID
		epoch   int
	}
	seen := map[cardKey]bool{}
	var u Usage
	for _, a := range today {
		k := cardKey{a.ProblemID, a.CardEpoch}
		if seen[k] {
			continue
		}
		seen[k] = true
		log, err := b.src.Attempts.ForCard(ctx, userID, a.ProblemID, a.CardEpoch)
		if err != nil {
			return Usage{}, err
		}
		if len(log) > 0 && day.Contains(log[0].AnsweredAt) {
			u.NewCards++
		}
	}
	u.Reviews = len(today) - u.NewCards
	return u, nil
}

// sampleNew draws up to n problems from cands without replacement, with
// probability proportional to 1 + weak_tag_boost * (1 - mean tag recall), using
// a generator seeded by the session ID (Efraimidis-Spirakis keys). cands must
// be in a stable order.
func (b *Builder) sampleNew(ctx context.Context, userID, sessionID domain.ID, cfg settings.Settings, cands []ProblemEpoch, n int) ([]Item, error) {
	if n <= 0 || len(cands) == 0 {
		return nil, nil
	}
	sum := sha256.Sum256(sessionID[:])
	rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])))
	type keyed struct {
		pe  ProblemEpoch
		key float64
	}
	ks := make([]keyed, len(cands))
	for i, pe := range cands {
		recall, err := b.src.Tags.ProblemTagRecall(ctx, userID, pe.ID)
		if err != nil {
			return nil, err
		}
		recall = math.Min(1, math.Max(0, recall))
		w := 1 + cfg.WeakTagBoost*(1-recall)
		u := 1 - rng.Float64() // (0,1]
		ks[i] = keyed{pe, math.Pow(u, 1/w)}
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].key > ks[j].key })
	if len(ks) > n {
		ks = ks[:n]
	}
	out := make([]Item, len(ks))
	for i, k := range ks {
		out[i] = Item{ProblemID: k.pe.ID, Epoch: k.pe.Epoch, Kind: ItemNew}
	}
	return out, nil
}

// interleave places one new item after every interval reviews. New items that
// find no more reviews to follow go at the end.
func interleave(reviews, news []Item, interval int) []Item {
	interval = max(1, interval)
	out := make([]Item, 0, len(reviews)+len(news))
	ni := 0
	for i, r := range reviews {
		out = append(out, r)
		if (i+1)%interval == 0 && ni < len(news) {
			out = append(out, news[ni])
			ni++
		}
	}
	return append(out, news[ni:]...)
}

func lessID(a, b domain.ID) bool { return bytes.Compare(a[:], b[:]) < 0 }
