// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"encoding/json"
	"testing"

	"github.com/chan-labs/quizzotic/internal/problems"
)

// Grading follows the stored match mode, and a payload stored before the
// match field existed (legacy case_sensitive, or neither key) grades as
// relaxed.
func TestGradeClozeMatchModes(t *testing.T) {
	cases := []struct {
		name, payload, answer string
		want                  bool
	}{
		{"relaxed ignores spacing and punctuation", `{"blanks":[{"id":"w","accepted":["New York"],"match":"relaxed"}]}`, " new  york. ", true},
		{"case-insensitive ignores case", `{"blanks":[{"id":"w","accepted":["New York"],"match":"case-insensitive"}]}`, "NEW YORK", true},
		{"case-insensitive keeps punctuation exact", `{"blanks":[{"id":"w","accepted":["New York"],"match":"case-insensitive"}]}`, "new york.", false},
		{"case-insensitive keeps whitespace exact", `{"blanks":[{"id":"w","accepted":["New York"],"match":"case-insensitive"}]}`, "new  york", false},
		{"legacy true grades as relaxed", `{"blanks":[{"id":"w","accepted":["New York"],"case_sensitive":true}]}`, "new york", true},
		{"legacy false grades as relaxed", `{"blanks":[{"id":"w","accepted":["New York"],"case_sensitive":false}]}`, "new york.", true},
		{"neither key grades as relaxed", `{"blanks":[{"id":"w","accepted":["New York"]}]}`, "NEW YORK", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subj := problems.AnswerSubject{Type: problems.TypeCloze, Payload: json.RawMessage(tc.payload)}
			raw := json.RawMessage(`{"answers":{"w":` + mustQuote(t, tc.answer) + `}}`)
			g, err := gradeCloze(subj, raw)
			if err != nil {
				t.Fatal(err)
			}
			if g.correct != tc.want {
				t.Errorf("correct = %v, want %v", g.correct, tc.want)
			}
		})
	}
}

func mustQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
