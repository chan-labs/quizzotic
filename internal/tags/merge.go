// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// maxMergeSources bounds one merge so a single transaction stays small.
const maxMergeSources = 50

// MergeInput names the tags of a merge by ID, slug or alias.
type MergeInput struct {
	Sources []string
	Target  string
}

// MergeResult is the state after a merge.
type MergeResult struct {
	Target  Tag   // with the sources' slugs and aliases added
	Sources []Tag // now merged, in input order
	// Affected lists, ascending, the problems that carried a source tag. The
	// caller recomputes mastery for them and the tags involved.
	Affected []domain.ID
}

// Merge moves every problem mapping of the source tags to the target
// (deduplicated), marks the sources merged into the target and makes their
// slugs and aliases aliases of the target, all in one transaction. Source
// and target must be active, distinct tags of the owner.
func (s *Service) Merge(ctx context.Context, ownerID domain.ID, in MergeInput) (MergeResult, error) {
	var out MergeResult
	err := s.tx.InTx(ctx, func(r Repos) error {
		var err error
		out, err = s.merge(ctx, r, ownerID, in)
		return err
	})
	return out, err
}

// merge is Merge inside an open transaction, so an operation applier can
// capture a snapshot and record the decision around it atomically.
func (s *Service) merge(ctx context.Context, r Repos, ownerID domain.ID, in MergeInput) (MergeResult, error) {
	if len(in.Sources) == 0 || len(in.Sources) > maxMergeSources {
		return MergeResult{}, platform.Validation("invalid merge", platform.FieldError{
			Field: "sources", Detail: fmt.Sprintf("give 1 to %d tags", maxMergeSources)})
	}
	var fe []platform.FieldError
	resolve := func(field, ref string) (Tag, bool, error) {
		t, err := Resolve(ctx, r.Tags, ownerID, ref)
		if errors.Is(err, ErrNotFound) {
			fe = append(fe, platform.FieldError{Field: field, Detail: "no such tag"})
			return Tag{}, false, nil
		}
		if err != nil {
			return Tag{}, false, err
		}
		if t.Status != StatusActive {
			fe = append(fe, platform.FieldError{Field: field, Detail: "tag is " + string(t.Status)})
			return Tag{}, false, nil
		}
		return t, true, nil
	}

	target, targetOK, err := resolve("target", in.Target)
	if err != nil {
		return MergeResult{}, err
	}
	var (
		ids  []domain.ID
		seen = map[domain.ID]bool{}
	)
	for i, ref := range in.Sources {
		field := fmt.Sprintf("sources[%d]", i)
		t, ok, err := resolve(field, ref)
		if err != nil {
			return MergeResult{}, err
		}
		if !ok {
			continue
		}
		switch {
		case targetOK && t.ID == target.ID:
			fe = append(fe, platform.FieldError{Field: field, Detail: "cannot merge a tag into itself"})
		case seen[t.ID]:
			fe = append(fe, platform.FieldError{Field: field, Detail: "duplicate source"})
		default:
			seen[t.ID] = true
			ids = append(ids, t.ID)
		}
	}
	if len(fe) > 0 {
		return MergeResult{}, platform.Validation("invalid merge", fe...)
	}

	var affected []domain.ID
	seenP := map[domain.ID]bool{}
	for _, id := range ids {
		for after := domain.Nil; ; {
			page, err := r.Tags.ProblemIDs(ctx, ownerID, id, after, 500)
			if err != nil {
				return MergeResult{}, err
			}
			for _, p := range page {
				if !seenP[p] {
					seenP[p] = true
					affected = append(affected, p)
				}
			}
			if len(page) < 500 {
				break
			}
			after = page[len(page)-1]
		}
	}
	slices.SortFunc(affected, func(a, b domain.ID) int { return compareIDs(a, b) })

	if err := r.Tags.Merge(ctx, ownerID, ids, target.ID, s.clock.Now()); err != nil {
		return MergeResult{}, err
	}
	res := MergeResult{Affected: affected}
	if res.Target, err = r.Tags.Get(ctx, ownerID, target.ID); err != nil {
		return MergeResult{}, err
	}
	for _, id := range ids {
		t, err := r.Tags.Get(ctx, ownerID, id)
		if err != nil {
			return MergeResult{}, err
		}
		res.Sources = append(res.Sources, t)
	}
	return res, nil
}

func compareIDs(a, b domain.ID) int {
	switch as, bs := a.String(), b.String(); {
	case as < bs:
		return -1
	case as > bs:
		return 1
	}
	return 0
}
