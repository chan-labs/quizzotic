// SPDX-License-Identifier: Apache-2.0

package grading

import "testing"

func TestNormalizeCloze(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		match string
		want  string
	}{
		{"trim", "  sky \t\n", MatchRelaxed, "sky"},
		{"lowercase", "SkY", MatchRelaxed, "sky"},
		{"collapse whitespace", "a   b\t\tc\nd", MatchRelaxed, "a b c d"},
		{"strip each trailing punct", "sky.,;:!?", MatchRelaxed, "sky"},
		{"strip trailing punct then space", "sky . ", MatchRelaxed, "sky"},
		{"internal punct kept", "a.b, c", MatchRelaxed, "a.b, c"},
		{"leading punct kept", "?sky", MatchRelaxed, "?sky"},
		{"unicode lowercase", "ÉCOLE", MatchRelaxed, "école"},
		{"empty", "  ", MatchRelaxed, ""},
		{"empty mode is relaxed", "  SkY! ", "", "sky"},
		{"unknown mode is relaxed", "  SkY! ", "bogus", "sky"},
		{"case-insensitive folds case", "SkY", MatchCaseInsensitive, "sky"},
		{"case-insensitive keeps spaces", "  Sky  ", MatchCaseInsensitive, "  sky  "},
		{"case-insensitive keeps inner whitespace", "A  B\tC", MatchCaseInsensitive, "a  b\tc"},
		{"case-insensitive keeps punctuation", "Sky!?", MatchCaseInsensitive, "sky!?"},
		{"case-insensitive unicode", "ÉCOLE", MatchCaseInsensitive, "école"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCloze(tt.in, tt.match); got != tt.want {
				t.Fatalf("NormalizeCloze(%q, %q) = %q, want %q", tt.in, tt.match, got, tt.want)
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"abc", "", 3}, {"", "abc", 3}, {"abc", "abc", 0},
		{"kitten", "sitting", 3}, {"abc", "abd", 1}, {"abc", "ac", 1},
		{"café", "cafe", 1}, {"日本語", "日本", 1}, {"日本語", "日本人", 1},
		{"😀a", "a", 1},
	}
	for _, tt := range tests {
		if got := Levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("Levenshtein(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestGradeClozeMaxEdits(t *testing.T) {
	// "planet" vs answers at distance 0, 1, 2, 3, 4.
	answers := []struct {
		answer string
		dist   int
	}{
		{"planet", 0}, {"planit", 1}, {"plonit", 2}, {"blonit", 3}, {"blonix", 4},
	}
	for maxEdits := range 4 {
		for _, a := range answers {
			b := ClozeBlank{ID: "b", Accepted: []string{"planet"}, MaxEdits: maxEdits}
			got := GradeCloze([]ClozeBlank{b}, map[string]string{"b": a.answer}).Correct["b"]
			if want := a.dist <= maxEdits; got != want {
				t.Errorf("max_edits=%d answer=%q (dist %d): correct=%v, want %v", maxEdits, a.answer, a.dist, got, want)
			}
		}
	}
}

func TestGradeClozeMaxEditsClamped(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"abcdefgh"}, MaxEdits: 99}
	if GradeCloze([]ClozeBlank{b}, map[string]string{"b": "abcdxxxx"}).Correct["b"] {
		t.Error("distance 4 accepted with max_edits clamped to 3")
	}
	b.MaxEdits = -1
	if GradeCloze([]ClozeBlank{b}, map[string]string{"b": "abcdefgx"}).Correct["b"] {
		t.Error("distance 1 accepted with negative max_edits")
	}
}

func TestGradeClozeNormalizationApplied(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"New  York."}}
	if !GradeCloze([]ClozeBlank{b}, map[string]string{"b": "  new york!"}).Correct["b"] {
		t.Error("normalized answer should match normalized accepted value")
	}
}

func TestGradeClozeCaseInsensitiveIsExactOtherwise(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"New York"}, Match: MatchCaseInsensitive}
	g := func(ans string) bool { return GradeCloze([]ClozeBlank{b}, map[string]string{"b": ans}).Correct["b"] }
	if !g("new york") || !g("NEW YORK") {
		t.Error("case must not matter")
	}
	for _, ans := range []string{" new york", "new york ", "new  york", "new york.", "new\tyork"} {
		if g(ans) {
			t.Errorf("%q accepted: whitespace and punctuation must stay exact", ans)
		}
	}
	// The same answers pass in relaxed mode.
	b.Match = MatchRelaxed
	for _, ans := range []string{" new york", "new york ", "new  york", "new york.", "new\tyork"} {
		if !g(ans) {
			t.Errorf("%q rejected in relaxed mode", ans)
		}
	}
	// Accepted answers keep their own spacing in case-insensitive mode.
	b = ClozeBlank{ID: "b", Accepted: []string{"Mr. Smith!"}, Match: MatchCaseInsensitive}
	if !g("mr. smith!") || g("mr. smith") {
		t.Error("accepted punctuation must match exactly")
	}
}

func TestGradeClozeCaseInsensitiveMaxEdits(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"Paris"}, Match: MatchCaseInsensitive, MaxEdits: 1}
	g := func(ans string) bool { return GradeCloze([]ClozeBlank{b}, map[string]string{"b": ans}).Correct["b"] }
	if !g("PARIS") {
		t.Error("case difference costs no edit")
	}
	if !g("pariz") {
		t.Error("one substitution is within max_edits=1")
	}
	// A trailing period is one edit, not stripped.
	if !g("paris.") {
		t.Error("one extra character is within max_edits=1")
	}
	if g("paris..") {
		t.Error("two extra characters exceed max_edits=1")
	}
	b.MaxEdits = 0
	if g("paris.") {
		t.Error("extra character with max_edits=0 should fail")
	}
}

func TestGradeClozeWhitespaceOnlyAnswerNeverCorrect(t *testing.T) {
	for _, m := range []string{MatchRelaxed, MatchCaseInsensitive} {
		b := ClozeBlank{ID: "b", Accepted: []string{"a"}, Match: m, MaxEdits: 3}
		for _, ans := range []string{"", " ", " \t\n"} {
			if GradeCloze([]ClozeBlank{b}, map[string]string{"b": ans}).Correct["b"] {
				t.Errorf("match %q: blank answer %q graded correct", m, ans)
			}
		}
	}
}

func TestGradeClozeUnicode(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"Café"}, MaxEdits: 1}
	g := func(ans string) bool { return GradeCloze([]ClozeBlank{b}, map[string]string{"b": ans}).Correct["b"] }
	if !g("CAFÉ") {
		t.Error("unicode lowercase should match")
	}
	// A byte-wise distance would count the two-byte é as 2 edits.
	if !g("cafe") {
		t.Error("é to e is one rune edit")
	}
	if g("cxxé") {
		t.Error("two rune edits must exceed max_edits=1")
	}
}

func TestGradeClozeAnyAccepted(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"color", "colour"}}
	for _, ans := range []string{"color", "Colour."} {
		if !GradeCloze([]ClozeBlank{b}, map[string]string{"b": ans}).Correct["b"] {
			t.Errorf("%q should match an accepted answer", ans)
		}
	}
}

func TestGradeClozeScore(t *testing.T) {
	blanks := []ClozeBlank{
		{ID: "a", Accepted: []string{"x"}},
		{ID: "b", Accepted: []string{"y"}},
		{ID: "c", Accepted: []string{"z"}},
		{ID: "d", Accepted: []string{"w"}},
	}
	tests := []struct {
		name    string
		answers map[string]string
		want    float64
	}{
		{"all", map[string]string{"a": "x", "b": "y", "c": "z", "d": "w"}, 1},
		{"half", map[string]string{"a": "x", "b": "y", "c": "no", "d": ""}, 0.5},
		{"one missing", map[string]string{"a": "x", "b": "y", "c": "z"}, 0.75},
		{"none", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := GradeCloze(blanks, tt.answers)
			if res.Score != tt.want {
				t.Errorf("score = %v, want %v", res.Score, tt.want)
			}
			if res.AllCorrect() != (tt.want == 1) {
				t.Errorf("AllCorrect = %v", res.AllCorrect())
			}
		})
	}
	if res := GradeCloze(nil, nil); res.Score != 0 || res.AllCorrect() {
		t.Error("no blanks should score 0")
	}
}

func TestGradeClozeEmptyAnswerNeverMatches(t *testing.T) {
	b := ClozeBlank{ID: "b", Accepted: []string{"a"}, MaxEdits: 3}
	if GradeCloze([]ClozeBlank{b}, map[string]string{"b": " . "}).Correct["b"] {
		t.Error("blank answer must not match via edit distance")
	}
}
