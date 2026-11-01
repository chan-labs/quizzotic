// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// MCQResponse is the response to an mcq problem.
type MCQResponse struct {
	Selected []string `json:"selected"`
}

// ClozeResponse is the response to a cloze problem.
type ClozeResponse struct {
	Answers map[string]string `json:"answers"`
}

// OpenResponse is the response to an open problem.
type OpenResponse struct {
	Text string `json:"text"`
}

// graded is the outcome of grading one response, before ratings apply.
type graded struct {
	// response is the normalized response to store.
	response json.RawMessage
	// auto is false for open problems.
	auto    bool
	correct bool
	score   float64
	blanks  map[string]bool
}

// gradeResponse grades raw against the problem's payload. Problems with the
// response itself (wrong shape, unknown choice or blank) are validation
// errors; a stored payload that cannot be decoded is an internal error.
func gradeResponse(subj problems.AnswerSubject, raw json.RawMessage) (graded, error) {
	switch subj.Type {
	case problems.TypeMCQ:
		return gradeMCQ(subj, raw)
	case problems.TypeCloze:
		return gradeCloze(subj, raw)
	case problems.TypeOpen:
		var r OpenResponse
		if err := decodeResponse(raw, &r); err != nil {
			return graded{}, err
		}
		return graded{response: mustJSON(r)}, nil
	}
	return graded{}, fmt.Errorf("%w: %q", grading.ErrUnknownProblemType, subj.Type)
}

func gradeMCQ(subj problems.AnswerSubject, raw json.RawMessage) (graded, error) {
	var p problems.MCQPayload
	if err := json.Unmarshal(subj.Payload, &p); err != nil {
		return graded{}, fmt.Errorf("attempts: stored mcq payload: %w", err)
	}
	var r MCQResponse
	if err := decodeResponse(raw, &r); err != nil {
		return graded{}, err
	}
	known := make(map[string]bool, len(p.Choices))
	for _, c := range p.Choices {
		known[c.ID] = true
	}
	switch {
	case len(r.Selected) == 0:
		return graded{}, responseError("response.selected", "select at least one choice")
	case !p.Multiple && len(r.Selected) != 1:
		return graded{}, responseError("response.selected", "select exactly one choice")
	}
	for _, id := range r.Selected {
		if !known[id] {
			return graded{}, responseError("response.selected", fmt.Sprintf("%q is not a choice of this problem", id))
		}
	}
	res := grading.GradeMCQ(p.Correct, r.Selected, p.Multiple)
	return graded{response: mustJSON(r), auto: true, correct: res.Correct, score: res.Score}, nil
}

func gradeCloze(subj problems.AnswerSubject, raw json.RawMessage) (graded, error) {
	var p problems.ClozePayload
	if err := json.Unmarshal(subj.Payload, &p); err != nil {
		return graded{}, fmt.Errorf("attempts: stored cloze payload: %w", err)
	}
	var r ClozeResponse
	if err := decodeResponse(raw, &r); err != nil {
		return graded{}, err
	}
	if r.Answers == nil {
		r.Answers = map[string]string{}
	}
	blanks := make([]grading.ClozeBlank, 0, len(p.Blanks))
	known := make(map[string]bool, len(p.Blanks))
	for _, b := range p.Blanks {
		known[b.ID] = true
		blanks = append(blanks, grading.ClozeBlank{ID: b.ID, Accepted: b.Accepted,
			CaseSensitive: b.CaseSensitive, MaxEdits: b.MaxEdits})
	}
	for id := range r.Answers {
		if !known[id] {
			return graded{}, responseError("response.answers", fmt.Sprintf("%q is not a blank of this problem", id))
		}
	}
	res := grading.GradeCloze(blanks, r.Answers)
	return graded{response: mustJSON(r), auto: true, correct: res.AllCorrect(), score: res.Score, blanks: res.Correct}, nil
}

// decodeResponse strictly decodes raw into dst; any mismatch is a 422 on the
// response field.
func decodeResponse(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return responseError("response", "does not match the problem type: "+err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return responseError("response", "must be a single JSON value")
	}
	return nil
}

func responseError(field, detail string) *platform.Problem {
	return platform.Validation("the response is invalid", platform.FieldError{Field: field, Detail: detail})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("attempts: marshal response: " + err.Error())
	}
	return b
}
