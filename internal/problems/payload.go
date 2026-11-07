// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Problem types.
const (
	TypeMCQ   = "mcq"
	TypeCloze = "cloze"
	TypeOpen  = "open"
)

// MaxClozeEdits is the largest allowed max_edits for a cloze blank.
const MaxClozeEdits = 3

// MCQPayload is the payload of an mcq problem.
type MCQPayload struct {
	Choices  []Choice `json:"choices"`
	Correct  []string `json:"correct"`
	Multiple bool     `json:"multiple"`
	Shuffle  bool     `json:"shuffle"`
}

// Choice is one MCQ answer option.
type Choice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ClozePayload is the payload of a cloze problem. The prompt carries the
// {{blank_id}} markers.
type ClozePayload struct {
	Blanks []Blank `json:"blanks"`
}

// Cloze match modes; see grading.NormalizeCloze.
const (
	MatchRelaxed         = grading.MatchRelaxed
	MatchCaseInsensitive = grading.MatchCaseInsensitive
)

// Blank is one cloze blank. Match is MatchRelaxed or MatchCaseInsensitive;
// stored payloads and output carry only `match`.
type Blank struct {
	ID       string   `json:"id"`
	Accepted []string `json:"accepted"`
	Match    string   `json:"match"`
	MaxEdits int      `json:"max_edits"`
}

// UnmarshalJSON decodes a blank strictly. The legacy boolean `case_sensitive`
// is still read (for one version) and means MatchRelaxed; when `match` is
// present it wins. A blank with neither is MatchRelaxed. An unknown `match`
// value is kept so ValidateCloze can report it.
func (b *Blank) UnmarshalJSON(data []byte) error {
	var aux struct {
		ID            string   `json:"id"`
		Accepted      []string `json:"accepted"`
		Match         *string  `json:"match"`
		CaseSensitive *bool    `json:"case_sensitive"` // legacy, ignored
		MaxEdits      int      `json:"max_edits"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aux); err != nil {
		return err
	}
	*b = Blank{ID: aux.ID, Accepted: aux.Accepted, Match: MatchRelaxed, MaxEdits: aux.MaxEdits}
	if aux.Match != nil {
		b.Match = *aux.Match
	}
	return nil
}

// CanonicalPayload rewrites a valid cloze payload to its stored form: legacy
// `case_sensitive` becomes `match: "relaxed"` and an absent `match` is made
// explicit. Other types, and payloads that do not decode, are returned as is
// (ValidatePayload reports those). Call it after validation.
func CanonicalPayload(typ string, raw json.RawMessage) json.RawMessage {
	if typ != TypeCloze {
		return raw
	}
	var p ClozePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return raw
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// OpenPayload is the payload of an open problem.
type OpenPayload struct {
	ReferenceAnswer string   `json:"reference_answer"`
	Rubric          []string `json:"rubric,omitempty"`
}

var clozeMarker = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// ClozeMarkers returns the blank IDs referenced by {{id}} markers in prompt,
// in order of appearance (duplicates preserved, IDs trimmed).
func ClozeMarkers(prompt string) []string {
	var ids []string
	for _, m := range clozeMarker.FindAllStringSubmatch(prompt, -1) {
		ids = append(ids, strings.TrimSpace(m[1]))
	}
	return ids
}

// ValidatePayload decodes raw as the payload for problem type typ and
// validates it against prompt (needed for cloze markers). It returns
// field-level errors; paths are rooted at the problem body, for example
// "payload.choices" or "payload.blanks[1].accepted".
func ValidatePayload(typ, prompt string, raw json.RawMessage) []platform.FieldError {
	switch typ {
	case TypeMCQ:
		var p MCQPayload
		if fe := decodePayload(raw, &p); fe != nil {
			return fe
		}
		return ValidateMCQ(p)
	case TypeCloze:
		var p ClozePayload
		if fe := decodePayload(raw, &p); fe != nil {
			return fe
		}
		return ValidateCloze(prompt, p)
	case TypeOpen:
		var p OpenPayload
		if fe := decodePayload(raw, &p); fe != nil {
			return fe
		}
		return ValidateOpen(p)
	default:
		return []platform.FieldError{{Field: "type", Detail: "must be one of mcq, cloze, open"}}
	}
}

func decodePayload(raw json.RawMessage, v any) []platform.FieldError {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []platform.FieldError{{Field: "payload", Detail: "required"}}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return []platform.FieldError{{Field: "payload", Detail: "invalid payload: " + err.Error()}}
	}
	return nil
}

// ValidateMCQ checks an mcq payload.
func ValidateMCQ(p MCQPayload) []platform.FieldError {
	var errs []platform.FieldError
	add := func(field, detail string) {
		errs = append(errs, platform.FieldError{Field: field, Detail: detail})
	}
	if len(p.Choices) < 2 {
		add("payload.choices", "need at least 2 choices")
	}
	ids := map[string]bool{}
	for i, c := range p.Choices {
		path := fmt.Sprintf("payload.choices[%d]", i)
		switch {
		case strings.TrimSpace(c.ID) == "":
			add(path+".id", "required")
		case ids[c.ID]:
			add(path+".id", "duplicate choice id "+c.ID)
		default:
			ids[c.ID] = true
		}
		if strings.TrimSpace(c.Text) == "" {
			add(path+".text", "required")
		}
	}
	if len(p.Correct) < 1 {
		add("payload.correct", "need at least 1 correct choice")
	} else if !p.Multiple && len(p.Correct) != 1 {
		add("payload.correct", "single-answer question needs exactly 1 correct choice")
	}
	seen := map[string]bool{}
	for i, id := range p.Correct {
		path := fmt.Sprintf("payload.correct[%d]", i)
		switch {
		case seen[id]:
			add(path, "duplicate correct id "+id)
		case !ids[id]:
			add(path, "does not match any choice id")
		}
		seen[id] = true
	}
	return errs
}

// ValidateCloze checks a cloze payload against its prompt.
func ValidateCloze(prompt string, p ClozePayload) []platform.FieldError {
	var errs []platform.FieldError
	add := func(field, detail string) {
		errs = append(errs, platform.FieldError{Field: field, Detail: detail})
	}
	if len(p.Blanks) == 0 {
		add("payload.blanks", "need at least 1 blank")
	}
	blankIDs := map[string]bool{}
	for i, b := range p.Blanks {
		path := fmt.Sprintf("payload.blanks[%d]", i)
		switch {
		case strings.TrimSpace(b.ID) == "":
			add(path+".id", "required")
		case blankIDs[b.ID]:
			add(path+".id", "duplicate blank id "+b.ID)
		default:
			blankIDs[b.ID] = true
		}
		if len(b.Accepted) < 1 {
			add(path+".accepted", "need at least 1 accepted answer")
		}
		for j, a := range b.Accepted {
			if strings.TrimSpace(a) == "" {
				add(fmt.Sprintf("%s.accepted[%d]", path, j), "must not be empty")
			}
		}
		if b.Match != MatchRelaxed && b.Match != MatchCaseInsensitive {
			add(path+".match", "must be relaxed or case-insensitive")
		}
		if b.MaxEdits < 0 || b.MaxEdits > MaxClozeEdits {
			add(path+".max_edits", fmt.Sprintf("must be between 0 and %d", MaxClozeEdits))
		}
	}

	markers := map[string]int{}
	for _, id := range ClozeMarkers(prompt) {
		markers[id]++
	}
	if len(markers) == 0 {
		add("prompt", "must contain at least 1 {{blank_id}} marker")
	}
	for id, n := range markers {
		if n > 1 {
			add("prompt", fmt.Sprintf("marker {{%s}} appears %d times; each blank may be used once", id, n))
		}
		if !blankIDs[id] {
			add("prompt", fmt.Sprintf("marker {{%s}} has no matching entry in payload.blanks", id))
		}
	}
	for i, b := range p.Blanks {
		if blankIDs[b.ID] && markers[b.ID] == 0 {
			add(fmt.Sprintf("payload.blanks[%d].id", i), fmt.Sprintf("blank %q has no {{%s}} marker in the prompt", b.ID, b.ID))
		}
	}
	// Map iteration above is unordered; keep the output deterministic.
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Field != errs[j].Field {
			return errs[i].Field < errs[j].Field
		}
		return errs[i].Detail < errs[j].Detail
	})
	return errs
}

// ValidateOpen checks an open payload.
func ValidateOpen(p OpenPayload) []platform.FieldError {
	var errs []platform.FieldError
	if strings.TrimSpace(p.ReferenceAnswer) == "" {
		errs = append(errs, platform.FieldError{Field: "payload.reference_answer", Detail: "required"})
	}
	for i, r := range p.Rubric {
		if strings.TrimSpace(r) == "" {
			errs = append(errs, platform.FieldError{Field: fmt.Sprintf("payload.rubric[%d]", i), Detail: "must not be empty"})
		}
	}
	return errs
}
