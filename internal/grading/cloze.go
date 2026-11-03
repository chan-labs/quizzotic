// SPDX-License-Identifier: Apache-2.0

package grading

import (
	"strings"
	"unicode"
)

// Cloze match modes.
const (
	// MatchRelaxed trims, lowercases, collapses internal whitespace to one
	// space and strips trailing .,;:!?
	MatchRelaxed = "relaxed"
	// MatchCaseInsensitive lowercases only: whitespace and punctuation must
	// match exactly.
	MatchCaseInsensitive = "case-insensitive"
)

// ClozeBlank is one blank of a cloze problem as stored in its payload.
type ClozeBlank struct {
	ID       string
	Accepted []string
	Match    string // MatchRelaxed (also for ""), or MatchCaseInsensitive
	MaxEdits int    // 0 (exact) to 3
}

// ClozeResult is the outcome of grading a cloze response.
type ClozeResult struct {
	// Correct maps blank ID to whether the response for it was accepted.
	Correct map[string]bool
	// Score is the fraction of blanks correct, in [0, 1].
	Score float64
}

// AllCorrect reports whether every blank was answered correctly.
func (r ClozeResult) AllCorrect() bool { return r.Score == 1 }

// NormalizeCloze normalizes an answer for the match mode. MatchCaseInsensitive
// only lowercases. Any other mode (MatchRelaxed, or an unknown or empty value
// read from an old row) trims, lowercases, collapses internal whitespace to
// one space and strips trailing .,;:!?
func NormalizeCloze(s, match string) string {
	s = strings.ToLower(s)
	if match == MatchCaseInsensitive {
		return s
	}
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	s = strings.TrimRight(s, ".,;:!?")
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// GradeCloze grades answers (blank ID to response) against blanks. A missing
// answer counts as wrong. With no blanks the score is 0.
func GradeCloze(blanks []ClozeBlank, answers map[string]string) ClozeResult {
	res := ClozeResult{Correct: make(map[string]bool, len(blanks))}
	if len(blanks) == 0 {
		return res
	}
	n := 0
	for _, b := range blanks {
		ok := clozeBlankCorrect(b, answers[b.ID])
		res.Correct[b.ID] = ok
		if ok {
			n++
		}
	}
	res.Score = float64(n) / float64(len(blanks))
	return res
}

func clozeBlankCorrect(b ClozeBlank, answer string) bool {
	got := NormalizeCloze(answer, b.Match)
	if got == "" || strings.TrimSpace(got) == "" {
		return false
	}
	maxEdits := min(max(b.MaxEdits, 0), 3)
	for _, acc := range b.Accepted {
		want := NormalizeCloze(acc, b.Match)
		if got == want {
			return true
		}
		if maxEdits > 0 && Levenshtein(got, want) <= maxEdits {
			return true
		}
	}
	return false
}

// Levenshtein returns the edit distance between a and b, counted in runes.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
