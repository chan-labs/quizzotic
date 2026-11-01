// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// NoDataRecall is the recall a tag with no studied cards counts as when
// weighting new problems (spec: "Tags with no data count as recall 0.5").
const NoDataRecall = 0.5

// RecallReader reports the mean recall of a problem's tags, as the review
// session builder needs it to weight new problems. It satisfies
// review.TagRecall. It only reads: it never touches a card or a mastery row.
//
// Recall is predicted at clock.Now() from the cards themselves (the same
// Compute the projection uses), so it is current even when stored rows are
// stale. The per-owner tag recalls are computed once and cached, so a reader
// is meant to be short-lived: one per session build.
type RecallReader struct {
	sc    Scope
	clock domain.Clock

	mu    sync.Mutex
	cache map[domain.ID]map[domain.ID]float64 // owner -> active tag -> recall or NoDataRecall
}

// NewRecallReader builds a RecallReader over sc.
func NewRecallReader(sc Scope, clock domain.Clock) *RecallReader {
	return &RecallReader{sc: sc, clock: clock, cache: map[domain.ID]map[domain.ID]float64{}}
}

// ProblemTagRecall returns the mean recall, in [0,1], of the owner's active
// tags on the problem. A tag with no studied cards counts as NoDataRecall; a
// problem with no active tags is NoDataRecall.
func (r *RecallReader) ProblemTagRecall(ctx context.Context, ownerID, problemID domain.ID) (float64, error) {
	byTag, err := r.owner(ctx, ownerID)
	if err != nil {
		return 0, err
	}
	ids, err := r.sc.Repo.ProblemTagIDs(ctx, ownerID, problemID)
	if err != nil {
		return 0, err
	}
	var sum float64
	var n int
	for _, id := range ids {
		if v, ok := byTag[id]; ok { // inactive tags are absent
			sum += v
			n++
		}
	}
	if n == 0 {
		return NoDataRecall, nil
	}
	return sum / float64(n), nil
}

func (r *RecallReader) owner(ctx context.Context, ownerID domain.ID) (map[domain.ID]float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.cache[ownerID]; ok {
		return m, nil
	}
	params, err := r.sc.Params.Params(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	active, err := r.sc.Repo.ActiveTagIDs(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	m := make(map[domain.ID]float64, len(active))
	if len(active) > 0 {
		cards, err := r.sc.Repo.TagCards(ctx, ownerID, active)
		if err != nil {
			return nil, err
		}
		byTag := make(map[domain.ID][]TagCard, len(active))
		for _, tc := range cards {
			byTag[tc.TagID] = append(byTag[tc.TagID], tc)
		}
		now := r.clock.Now()
		for _, id := range active {
			tm := Compute(params, now, ownerID, id, byTag[id], time.Time{})
			if tm.CardsSeen == 0 {
				m[id] = NoDataRecall
			} else {
				m[id] = tm.Recall
			}
		}
	}
	r.cache[ownerID] = m
	return m, nil
}
