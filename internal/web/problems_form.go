// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/chan-labs/quizzotic/internal/problems"
)

// minChoiceRows and minBlankRows are how many editor rows are shown at least;
// the editor adds spareRows empty rows each time it is rendered so no script
// is needed to grow a list.
const (
	minChoiceRows = 4
	minBlankRows  = 2
	spareRows     = 2
)

type choiceRow struct {
	ID      string
	Text    string
	Correct bool
}

type blankRow struct {
	ID            string
	Accepted      string // one accepted answer per line
	CaseSensitive bool
	MaxEdits      int
}

// problemForm is the editor's state: what the browser submitted, or the
// current version of a stored problem.
type problemForm struct {
	ID          string
	ETag        string
	Version     int
	Type        string
	Status      string
	Prompt      string
	Explanation string
	Source      string
	ChangeKind  string
	Tags        map[string]bool

	Choices  []choiceRow
	Multiple bool
	Shuffle  bool

	Blanks []blankRow

	Reference string
	Rubric    string // one criterion per line
}

// IsNew reports whether the form creates a problem.
func (f problemForm) IsNew() bool { return f.ID == "" }

// pad adds the spare rows the editor shows.
func (f *problemForm) pad() {
	if f.Type == problems.TypeMCQ {
		n := max(len(f.Choices)+spareRows, minChoiceRows)
		for len(f.Choices) < n {
			f.Choices = append(f.Choices, choiceRow{})
		}
	}
	if f.Type == problems.TypeCloze {
		n := max(len(f.Blanks)+spareRows, minBlankRows)
		for len(f.Blanks) < n {
			f.Blanks = append(f.Blanks, blankRow{})
		}
	}
}

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func norm(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// parseProblemForm reads the editor's fields. It does not validate: the
// problems service does, and its field errors are shown beside the form.
func parseProblemForm(r *http.Request) problemForm {
	_ = r.ParseForm()
	v := r.PostForm
	f := problemForm{
		ETag:        v.Get("etag"),
		Type:        v.Get("type"),
		Status:      v.Get("status"),
		Prompt:      norm(v.Get("prompt")),
		Explanation: norm(v.Get("explanation")),
		Source:      strings.TrimSpace(v.Get("source")),
		ChangeKind:  v.Get("change_kind"),
		Tags:        map[string]bool{},
		Multiple:    v.Get("multiple") != "",
		Shuffle:     v.Get("shuffle") != "",
		Reference:   norm(v.Get("reference_answer")),
		Rubric:      norm(v.Get("rubric")),
	}
	for _, t := range v["tags"] {
		f.Tags[t] = true
	}

	correct := indexSet(v["correct"])
	ids, texts := v["choice_id"], v["choice_text"]
	used := map[string]bool{}
	for _, id := range ids {
		used[strings.TrimSpace(id)] = true
	}
	next := 1
	for i, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		id := ""
		if i < len(ids) {
			id = strings.TrimSpace(ids[i])
		}
		for id == "" {
			cand := "c" + strconv.Itoa(next)
			next++
			if !used[cand] {
				id = cand
				used[cand] = true
			}
		}
		f.Choices = append(f.Choices, choiceRow{ID: id, Text: text, Correct: correct[i]})
	}

	caseSens := indexSet(v["blank_case"])
	accepted, edits := v["blank_accepted"], v["blank_edits"]
	for i, raw := range v["blank_id"] {
		id := strings.TrimSpace(raw)
		acc := ""
		if i < len(accepted) {
			acc = strings.Join(lines(accepted[i]), "\n")
		}
		if id == "" && acc == "" {
			continue
		}
		n := 0
		if i < len(edits) {
			n, _ = strconv.Atoi(strings.TrimSpace(edits[i]))
		}
		f.Blanks = append(f.Blanks, blankRow{ID: id, Accepted: acc, CaseSensitive: caseSens[i], MaxEdits: n})
	}
	return f
}

func indexSet(vals []string) map[int]bool {
	out := map[int]bool{}
	for _, s := range vals {
		if n, err := strconv.Atoi(s); err == nil {
			out[n] = true
		}
	}
	return out
}

// payload builds the type-specific payload JSON the problems service
// validates.
func (f problemForm) payload() (json.RawMessage, error) {
	switch f.Type {
	case problems.TypeMCQ:
		p := problems.MCQPayload{Choices: []problems.Choice{}, Correct: []string{}, Multiple: f.Multiple, Shuffle: f.Shuffle}
		for _, c := range f.Choices {
			p.Choices = append(p.Choices, problems.Choice{ID: c.ID, Text: c.Text})
			if c.Correct {
				p.Correct = append(p.Correct, c.ID)
			}
		}
		return json.Marshal(p)
	case problems.TypeCloze:
		p := problems.ClozePayload{Blanks: []problems.Blank{}}
		for _, b := range f.Blanks {
			p.Blanks = append(p.Blanks, problems.Blank{ID: b.ID, Accepted: lines(b.Accepted),
				CaseSensitive: b.CaseSensitive, MaxEdits: b.MaxEdits})
		}
		return json.Marshal(p)
	case problems.TypeOpen:
		return json.Marshal(problems.OpenPayload{ReferenceAnswer: f.Reference, Rubric: lines(f.Rubric)})
	}
	return nil, fmt.Errorf("unknown problem type %q", f.Type)
}

// formFromView loads a stored problem into editor state.
func formFromView(v problems.View) (problemForm, error) {
	f := problemForm{ID: v.ID.String(), ETag: v.ETag(), Version: v.Version, Type: v.Type, Status: string(v.Status),
		Prompt: v.Prompt, Explanation: v.Explanation, Source: v.Source, ChangeKind: string(problems.ChangeMinor),
		Tags: map[string]bool{}}
	switch v.Type {
	case problems.TypeMCQ:
		var p problems.MCQPayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return f, err
		}
		correct := map[string]bool{}
		for _, id := range p.Correct {
			correct[id] = true
		}
		for _, c := range p.Choices {
			f.Choices = append(f.Choices, choiceRow{ID: c.ID, Text: c.Text, Correct: correct[c.ID]})
		}
		f.Multiple, f.Shuffle = p.Multiple, p.Shuffle
	case problems.TypeCloze:
		var p problems.ClozePayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return f, err
		}
		for _, b := range p.Blanks {
			f.Blanks = append(f.Blanks, blankRow{ID: b.ID, Accepted: strings.Join(b.Accepted, "\n"),
				CaseSensitive: b.CaseSensitive, MaxEdits: b.MaxEdits})
		}
	case problems.TypeOpen:
		var p problems.OpenPayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return f, err
		}
		f.Reference, f.Rubric = p.ReferenceAnswer, strings.Join(p.Rubric, "\n")
	}
	return f, nil
}
