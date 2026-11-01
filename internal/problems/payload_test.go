// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestValidatePayload(t *testing.T) {
	type fe = [2]string // field, detail
	const two = `[{"id":"a","text":"A"},{"id":"b","text":"B"}]`
	cases := []struct {
		name    string
		typ     string
		prompt  string
		payload string
		want    []fe
	}{
		// mcq
		{"mcq valid single", "mcq", "q", `{"choices":` + two + `,"correct":["a"]}`, nil},
		{"mcq valid multiple", "mcq", "q", `{"choices":` + two + `,"correct":["a","b"],"multiple":true,"shuffle":true}`, nil},
		{"mcq one choice", "mcq", "q", `{"choices":[{"id":"a","text":"A"}],"correct":["a"]}`, []fe{{"payload.choices", "need at least 2 choices"}}},
		{"mcq no choices", "mcq", "q", `{"correct":["a"]}`, []fe{
			{"payload.choices", "need at least 2 choices"},
			{"payload.correct[0]", "does not match any choice id"},
		}},
		{"mcq no correct", "mcq", "q", `{"choices":` + two + `,"correct":[]}`, []fe{{"payload.correct", "need at least 1 correct choice"}}},
		{"mcq multiple no correct", "mcq", "q", `{"choices":` + two + `,"multiple":true}`, []fe{{"payload.correct", "need at least 1 correct choice"}}},
		{"mcq single with two correct", "mcq", "q", `{"choices":` + two + `,"correct":["a","b"]}`, []fe{{"payload.correct", "single-answer question needs exactly 1 correct choice"}}},
		{"mcq unknown correct", "mcq", "q", `{"choices":` + two + `,"correct":["z"]}`, []fe{{"payload.correct[0]", "does not match any choice id"}}},
		{"mcq duplicate correct", "mcq", "q", `{"choices":` + two + `,"correct":["a","a"],"multiple":true}`, []fe{{"payload.correct[1]", "duplicate correct id a"}}},
		{"mcq choice id and text", "mcq", "q", `{"choices":[{"id":"","text":"A"},{"id":"b","text":" "}],"correct":["b"]}`, []fe{
			{"payload.choices[0].id", "required"},
			{"payload.choices[1].text", "required"},
		}},
		{"mcq duplicate choice id", "mcq", "q", `{"choices":[{"id":"a","text":"A"},{"id":"a","text":"B"}],"correct":["a"]}`, []fe{{"payload.choices[1].id", "duplicate choice id a"}}},

		// cloze
		{"cloze valid", "cloze", "The {{b1}} is {{b2}}.", `{"blanks":[{"id":"b1","accepted":["sky"]},{"id":"b2","accepted":["blue"],"max_edits":3}]}`, nil},
		{"cloze marker without blank", "cloze", "The {{b1}} is {{b2}}.", `{"blanks":[{"id":"b1","accepted":["sky"]}]}`, []fe{{"prompt", "marker {{b2}} has no matching entry in payload.blanks"}}},
		{"cloze blank without marker", "cloze", "The {{b1}}.", `{"blanks":[{"id":"b1","accepted":["sky"]},{"id":"b2","accepted":["x"]}]}`, []fe{{"payload.blanks[1].id", `blank "b2" has no {{b2}} marker in the prompt`}}},
		{"cloze duplicate marker", "cloze", "{{b1}} and {{b1}}", `{"blanks":[{"id":"b1","accepted":["x"]}]}`, []fe{{"prompt", "marker {{b1}} appears 2 times; each blank may be used once"}}},
		{"cloze no markers", "cloze", "plain", `{"blanks":[{"id":"b1","accepted":["x"]}]}`, []fe{
			{"payload.blanks[0].id", `blank "b1" has no {{b1}} marker in the prompt`},
			{"prompt", "must contain at least 1 {{blank_id}} marker"},
		}},
		{"cloze no blanks", "cloze", "{{b1}}", `{"blanks":[]}`, []fe{
			{"payload.blanks", "need at least 1 blank"},
			{"prompt", "marker {{b1}} has no matching entry in payload.blanks"},
		}},
		{"cloze no accepted", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":[]}]}`, []fe{{"payload.blanks[0].accepted", "need at least 1 accepted answer"}}},
		{"cloze empty accepted entry", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":["ok"," "]}]}`, []fe{{"payload.blanks[0].accepted[1]", "must not be empty"}}},
		{"cloze max_edits too high", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":["x"],"max_edits":4}]}`, []fe{{"payload.blanks[0].max_edits", "must be between 0 and 3"}}},
		{"cloze max_edits negative", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":["x"],"max_edits":-1}]}`, []fe{{"payload.blanks[0].max_edits", "must be between 0 and 3"}}},
		{"cloze max_edits 0 ok", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":["x"],"max_edits":0}]}`, nil},
		{"cloze blank id required", "cloze", "{{b1}}", `{"blanks":[{"id":"","accepted":["x"]}]}`, []fe{
			{"payload.blanks[0].id", "required"},
			{"prompt", "marker {{b1}} has no matching entry in payload.blanks"},
		}},
		{"cloze duplicate blank id", "cloze", "{{b1}}", `{"blanks":[{"id":"b1","accepted":["x"]},{"id":"b1","accepted":["y"]}]}`, []fe{{"payload.blanks[1].id", "duplicate blank id b1"}}},

		// open
		{"open valid", "open", "q", `{"reference_answer":"because","rubric":["a","b"]}`, nil},
		{"open empty reference", "open", "q", `{"reference_answer":"  "}`, []fe{{"payload.reference_answer", "required"}}},
		{"open missing reference", "open", "q", `{}`, []fe{{"payload.reference_answer", "required"}}},
		{"open empty rubric item", "open", "q", `{"reference_answer":"x","rubric":["a",""]}`, []fe{{"payload.rubric[1]", "must not be empty"}}},

		// envelope
		{"missing payload", "mcq", "q", ``, []fe{{"payload", "required"}}},
		{"null payload", "open", "q", `null`, []fe{{"payload", "required"}}},
		{"unknown field", "open", "q", `{"reference_answer":"x","bogus":1}`, []fe{{"payload", `invalid payload: json: unknown field "bogus"`}}},
		{"unknown type", "essay", "q", `{}`, []fe{{"type", "must be one of mcq, cloze, open"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []fe
			for _, e := range ValidatePayload(tc.typ, tc.prompt, json.RawMessage(tc.payload)) {
				got = append(got, fe{e.Field, e.Detail})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestValidatePayloadWrongShape(t *testing.T) {
	errs := ValidatePayload("mcq", "q", json.RawMessage(`{"choices":"nope"}`))
	if len(errs) != 1 || errs[0].Field != "payload" {
		t.Errorf("got %v", errs)
	}
}

func TestClozeMarkers(t *testing.T) {
	got := ClozeMarkers("a {{ x }} b {{y}} {{x}} {{}} {single}")
	want := []string{"x", "y", "x", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}
