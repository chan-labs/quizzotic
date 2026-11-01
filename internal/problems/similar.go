// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"sort"
	"strings"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Limits for a similarity search.
const (
	DefaultSimilarLimit = 5
	MaxSimilarLimit     = 50
)

// SimilarInput is a duplicate-check request.
type SimilarInput struct {
	Prompt  string
	Answers []string
	// Limit caps the result; <= 0 means DefaultSimilarLimit.
	Limit int
	// MinScore drops candidates scoring below it. Candidates scoring 0 are
	// never returned.
	MinScore float64
}

// Similar is one candidate with its Jaccard score.
type Similar struct {
	Score   float64 `json:"score"`
	Problem View    `json:"problem"`
}

// SimilarResult is the body of a duplicate-check response.
type SimilarResult struct {
	Items []Similar `json:"items"`
}

// Similar ranks the owner's non-deleted problems by token-set Jaccard
// similarity between in and each problem's current prompt and answers, best
// first, ties broken by problem ID.
func (s *Service) Similar(ctx context.Context, ownerID domain.ID, in SimilarInput) (SimilarResult, error) {
	var fields []platform.FieldError
	if strings.TrimSpace(in.Prompt) == "" {
		fields = append(fields, platform.FieldError{Field: "prompt", Detail: "required"})
	}
	if in.Limit < 0 || in.Limit > MaxSimilarLimit {
		fields = append(fields, platform.FieldError{Field: "limit", Detail: "must be between 1 and 50"})
	}
	if in.MinScore < 0 || in.MinScore > 1 {
		fields = append(fields, platform.FieldError{Field: "min_score", Detail: "must be between 0 and 1"})
	}
	if len(fields) > 0 {
		return SimilarResult{}, platform.Validation("invalid similarity request", fields...)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultSimilarLimit
	}

	query := TokenizeAll(append([]string{in.Prompt}, in.Answers...)...)
	rows, err := s.repo.ListCurrent(ctx, ownerID, ListFilter{})
	if err != nil {
		return SimilarResult{}, err
	}
	found := make([]Similar, 0, len(rows))
	for _, c := range rows {
		cand := TokenizeAll(append([]string{c.Version.Prompt}, AnswerTexts(c.Version.Type, c.Version.Payload)...)...)
		score := Jaccard(query, cand)
		if score <= 0 || score < in.MinScore {
			continue
		}
		found = append(found, Similar{Score: score, Problem: newView(c.Problem, c.Version)})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Score != found[j].Score {
			return found[i].Score > found[j].Score
		}
		return found[i].Problem.ID.String() < found[j].Problem.ID.String()
	})
	if len(found) > limit {
		found = found[:limit]
	}
	return SimilarResult{Items: found}, nil
}
