// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Tokens is a set of normalized words.
type Tokens map[string]struct{}

// Tokenize lowercases s and splits it into runs of letters and digits. Cloze
// {{blank_id}} markers are dropped first: their IDs say nothing about the
// content. The result is a set, so repeated words count once.
func Tokenize(s string) Tokens {
	s = clozeMarker.ReplaceAllString(s, " ")
	set := Tokens{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		set[w] = struct{}{}
	}
	return set
}

// TokenizeAll is the token set of the union of texts.
func TokenizeAll(texts ...string) Tokens {
	set := Tokens{}
	for _, t := range texts {
		for w := range Tokenize(t) {
			set[w] = struct{}{}
		}
	}
	return set
}

// Jaccard returns |a ∩ b| / |a ∪ b|, in [0, 1]. A set with no tokens has no
// content to compare and scores 0.
func Jaccard(a, b Tokens) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for w := range small {
		if _, ok := large[w]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// AnswerTexts lists the answer-bearing strings of a stored payload: MCQ choice
// texts, cloze accepted answers, or the open reference answer. A payload that
// cannot be decoded yields none, so one corrupt row never fails a search.
func AnswerTexts(typ string, payload json.RawMessage) []string {
	var out []string
	switch typ {
	case TypeMCQ:
		var p MCQPayload
		if json.Unmarshal(payload, &p) == nil {
			for _, c := range p.Choices {
				out = append(out, c.Text)
			}
		}
	case TypeCloze:
		var p ClozePayload
		if json.Unmarshal(payload, &p) == nil {
			for _, b := range p.Blanks {
				out = append(out, b.Accepted...)
			}
		}
	case TypeOpen:
		var p OpenPayload
		if json.Unmarshal(payload, &p) == nil {
			out = append(out, p.ReferenceAnswer)
		}
	}
	return out
}
