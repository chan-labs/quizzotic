// SPDX-License-Identifier: Apache-2.0

package grading

// MCQResult is the outcome of grading one multiple-choice response.
type MCQResult struct {
	// Score is in [0, 1].
	Score float64
	// Correct is true only when the selection exactly matches the correct set.
	Correct bool
}

// GradeMCQ grades a multiple-choice response.
//
// correct holds the choice IDs that are right; selected holds the IDs the
// learner picked. Duplicate IDs are counted once, and any selected ID not in
// correct counts as wrong.
//
// Single-answer (multiple == false): score is 1 for exactly the one correct
// pick, else 0. Multiple-answer: score is (right - wrong) / len(correct),
// floored at 0. Correct is true only on an exact set match. If correct is
// empty the result is a zero score and not correct (create-time validation
// should prevent this).
func GradeMCQ(correct, selected []string, multiple bool) MCQResult {
	want := make(map[string]struct{}, len(correct))
	for _, id := range correct {
		want[id] = struct{}{}
	}
	if len(want) == 0 {
		return MCQResult{}
	}
	seen := make(map[string]struct{}, len(selected))
	right, wrong := 0, 0
	for _, id := range selected {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := want[id]; ok {
			right++
		} else {
			wrong++
		}
	}
	exact := wrong == 0 && right == len(want)
	if !multiple {
		if exact && len(want) == 1 {
			return MCQResult{Score: 1, Correct: true}
		}
		return MCQResult{}
	}
	score := float64(right-wrong) / float64(len(want))
	if score < 0 {
		score = 0
	}
	return MCQResult{Score: score, Correct: exact}
}
