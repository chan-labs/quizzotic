// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
)

// ReviewSessions is what the Review page needs from the session service the
// API uses. *review.SessionService implements it.
type ReviewSessions interface {
	Get(ctx context.Context, userID, id domain.ID) (review.SessionView, error)
	Next(ctx context.Context, userID, id domain.ID) (review.NextView, error)
	Complete(ctx context.Context, userID, id domain.ID) (review.SessionView, error)
}

// ReviewAnswers is what the Review page needs from the answer flow.
// *attempts.Service implements it.
type ReviewAnswers interface {
	Answer(ctx context.Context, actor domain.Actor, in attempts.AnswerInput) (attempts.Result, error)
	Rate(ctx context.Context, actor domain.Actor, id domain.ID, rating int) (attempts.Result, error)
	// Get reads the owner's stored attempt; the result page is built from it.
	Get(ctx context.Context, ownerID, id domain.ID) (attempts.View, error)
}

// ReviewPrompts reads a problem in prompt view. *problems.Service implements it.
type ReviewPrompts interface {
	GetPrompt(ctx context.Context, ownerID, id domain.ID) (problems.PromptView, error)
	// Get returns the full problem (answer and explanation) for a result.
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
}

type reviewPages struct {
	sessions ReviewSessions
	answers  ReviewAnswers
	prompts  ReviewPrompts
}

// UseReview enables the Review page (review and quiz sessions). Call it
// before Register.
func (w *Web) UseReview(s ReviewSessions, a ReviewAnswers, p ReviewPrompts) {
	w.reviewPg = reviewPages{sessions: s, answers: a, prompts: p}
}

func (w *Web) registerReview(mux *http.ServeMux) {
	if w.reviewPg.sessions == nil {
		return
	}
	mux.Handle("GET /review/{id}", w.gate(http.HandlerFunc(w.reviewShow)))
	mux.Handle("GET /review/{id}/result/{attempt}", w.gate(http.HandlerFunc(w.reviewResult)))
	mux.Handle("POST /review/{id}/answer", w.gate(http.HandlerFunc(w.reviewAnswer)))
	mux.Handle("POST /review/{id}/rate", w.gate(http.HandlerFunc(w.reviewRate)))
}

// ---- view models ----

type reviewChoice struct {
	Key    int // 1-9, or 0 for no shortcut
	ID     string
	Text   string
	Picked bool
	Right  bool
}

type reviewBlank struct {
	ID, Accepted string
	Right        bool
}

type reviewItem struct {
	ProblemID string
	Version   int
	Type      string
	Prompt    template.HTML
	Choices   []reviewChoice
	Multiple  bool
}

type reviewResult struct {
	AttemptID   string
	Type        string
	Prompt      template.HTML
	Choices     []reviewChoice
	Verdict     string // right, wrong, or empty for an open problem
	Blanks      []reviewBlank
	Reference   template.HTML
	Rubric      []string
	Explanation template.HTML
	NextDue     string
	Ratings     []reviewRating // the buttons offered; empty when none apply
	Applied     int            // rating already in force, 0 when unrated
	NeedsRating bool
}

type reviewRating struct {
	Value   int
	Label   string
	Default bool
}

type reviewPage struct {
	Phase     string // prompt, result or done
	SessionID string
	Quiz      bool
	Remaining int
	Answered  int
	Error     string
	Item      *reviewItem
	Result    *reviewResult
}

var ratingLabels = map[int]string{1: "Again", 2: "Hard", 3: "Good", 4: "Easy"}

// ---- handlers ----

func reviewSessionID(rw http.ResponseWriter, r *http.Request) (domain.ID, bool) {
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return domain.ID{}, false
	}
	return id, true
}

// reviewShow serves the next unanswered problem, or the completion page.
func (w *Web) reviewShow(rw http.ResponseWriter, r *http.Request) {
	id, ok := reviewSessionID(rw, r)
	if !ok {
		return
	}
	ws, _ := auth.SessionFrom(r.Context())
	rp := w.reviewPg
	sess, err := rp.sessions.Get(r.Context(), ws.UserID, id)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	if sess.CompletedAt == nil {
		next, err := rp.sessions.Next(r.Context(), ws.UserID, id)
		if err != nil {
			w.failReview(rw, r, err)
			return
		}
		if next.Item != nil {
			w.renderPrompt(rw, r, http.StatusOK, sess, next.Item.Problem, next.Remaining, "")
			return
		}
		if sess, err = rp.sessions.Complete(r.Context(), ws.UserID, id); err != nil {
			w.failReview(rw, r, err)
			return
		}
	}
	w.render(rw, r, "review", page{Title: reviewTitle(sess), Data: reviewPage{
		Phase: "done", SessionID: id.String(), Quiz: sess.Kind == review.SessionQuiz, Answered: sess.Answered}})
}

func reviewTitle(s review.SessionView) string {
	if s.Kind == review.SessionQuiz {
		return "Quiz"
	}
	return "Review"
}

func (w *Web) renderPrompt(rw http.ResponseWriter, r *http.Request, status int, sess review.SessionView, pv problems.PromptView, remaining int, msg string) {
	item, err := buildReviewItem(pv)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	w.renderStatus(rw, r, status, "review", page{Title: reviewTitle(sess), Data: reviewPage{
		Phase: "prompt", SessionID: sess.ID.String(), Quiz: sess.Kind == review.SessionQuiz,
		Remaining: remaining, Error: msg, Item: item}})
}

func buildReviewItem(pv problems.PromptView) (*reviewItem, error) {
	it := &reviewItem{ProblemID: pv.ID.String(), Version: pv.Version, Type: pv.Type}
	switch pv.Type {
	case problems.TypeMCQ:
		var p struct {
			Choices  []problems.Choice `json:"choices"`
			Multiple bool              `json:"multiple"`
		}
		if err := json.Unmarshal(pv.Payload, &p); err != nil {
			return nil, fmt.Errorf("review: mcq prompt payload: %w", err)
		}
		it.Prompt, it.Multiple = RenderMarkdown(pv.Prompt), p.Multiple
		for i, c := range p.Choices {
			ch := reviewChoice{ID: c.ID, Text: c.Text}
			if i < 9 {
				ch.Key = i + 1
			}
			it.Choices = append(it.Choices, ch)
		}
	case problems.TypeCloze:
		ids, err := clozePromptIDs(pv.Payload)
		if err != nil {
			return nil, err
		}
		it.Prompt = fillBlanks(RenderMarkdown(pv.Prompt), ids, func(id string) string {
			return `<input type="text" class="blank-input" name="blank_` + html.EscapeString(id) +
				`" aria-label="Blank ` + html.EscapeString(id) + `" autocomplete="off" autocapitalize="off" spellcheck="false">`
		})
	default:
		it.Prompt = RenderMarkdown(pv.Prompt)
	}
	return it, nil
}

func clozePromptIDs(payload json.RawMessage) (map[string]bool, error) {
	var p struct {
		Blanks []struct {
			ID string `json:"id"`
		} `json:"blanks"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("review: cloze prompt payload: %w", err)
	}
	ids := make(map[string]bool, len(p.Blanks))
	for _, b := range p.Blanks {
		ids[b.ID] = true
	}
	return ids, nil
}

var reviewMarker = regexp.MustCompile(`\{\{([^{}<>]*)\}\}`)

// fillBlanks swaps each {{id}} marker of already-rendered (inert) Markdown for
// the markup render builds. The result is only as trusted as render's output.
func fillBlanks(src template.HTML, known map[string]bool, render func(id string) string) template.HTML {
	return template.HTML(reviewMarker.ReplaceAllStringFunc(string(src), func(m string) string {
		id := strings.TrimSpace(m[2 : len(m)-2])
		if !known[id] {
			return m
		}
		return render(id)
	}))
}

// reviewAnswer grades a submitted answer and shows the result.
func (w *Web) reviewAnswer(rw http.ResponseWriter, r *http.Request) {
	id, ok := reviewSessionID(rw, r)
	if !ok {
		return
	}
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	rp := w.reviewPg
	pid, err := domain.ParseID(r.PostFormValue("problem_id"))
	version, verr := strconv.Atoi(r.PostFormValue("problem_version"))
	if err != nil || verr != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	sess, err := rp.sessions.Get(r.Context(), ws.UserID, id)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	if sess.CompletedAt != nil {
		http.Error(rw, "the session is completed", http.StatusConflict)
		return
	}
	// A problem can be in the list more than once (a learning-step card comes
	// back); it is a replay only when every one of its items is answered, which
	// is exactly when Next would not serve it again.
	inSession, pending := false, false
	for _, it := range sess.Items {
		if it.ProblemID == pid {
			inSession, pending = true, pending || !it.Answered
		}
	}
	if !inSession {
		http.Error(rw, "that problem is not part of this session", http.StatusBadRequest)
		return
	}
	if !pending {
		// A replayed post (refresh, double submit) must not append a second
		// attempt: attempts are append-only and a duplicate skews the card.
		http.Redirect(rw, r, reviewPath(id), http.StatusSeeOther)
		return
	}
	pv, err := rp.prompts.GetPrompt(r.Context(), ws.UserID, pid)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	response, err := reviewResponse(pv, r)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	latency, _ := strconv.Atoi(r.PostFormValue("latency_ms"))
	if latency < 0 {
		latency = 0
	}
	res, err := rp.answers.Answer(r.Context(), domain.Actor{Kind: domain.ActorHuman, UserID: ws.UserID, ClientID: webClientID},
		attempts.AnswerInput{ProblemID: pid, ProblemVersion: version, Response: response, LatencyMS: latency, SessionID: id.String()})
	var pb *platform.Problem
	if errors.As(err, &pb) && pb.Status == http.StatusUnprocessableEntity {
		// The answer itself is unusable (e.g. nothing picked): ask again.
		w.renderPrompt(rw, r, http.StatusUnprocessableEntity, sess, pv, len(sess.Items)-sess.Answered, problemMessage(pb))
		return
	}
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	// Post/Redirect/Get: the result lives at its own URL, so a refresh or
	// back/forward only re-reads it.
	http.Redirect(rw, r, reviewPath(id)+"/result/"+res.Attempt.ID.String(), http.StatusSeeOther)
}

// reviewResult shows the stored result of one attempt of the session.
func (w *Web) reviewResult(rw http.ResponseWriter, r *http.Request) {
	id, ok := reviewSessionID(rw, r)
	if !ok {
		return
	}
	aid, err := domain.ParseID(r.PathValue("attempt"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	ws, _ := auth.SessionFrom(r.Context())
	rp := w.reviewPg
	sess, err := rp.sessions.Get(r.Context(), ws.UserID, id)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	a, err := rp.answers.Get(r.Context(), ws.UserID, aid)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	if a.SessionID == nil || *a.SessionID != id.String() {
		http.NotFound(rw, r)
		return
	}
	full, err := rp.prompts.Get(r.Context(), ws.UserID, a.ProblemID)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	res, err := storedResult(a, full)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	pv := problems.PromptView{ID: full.ID, Version: a.ProblemVersion, Type: full.Type, Prompt: full.Prompt, Payload: full.Payload}
	out, err := buildReviewResult(pv, res)
	if err != nil {
		w.failReview(rw, r, err)
		return
	}
	w.render(rw, r, "review", page{Title: reviewTitle(sess), Data: reviewPage{
		Phase: "result", SessionID: id.String(), Quiz: sess.Kind == review.SessionQuiz, Result: out}})
}

// storedResult rebuilds what answering returned from the stored attempt and
// the problem, so the result page can be shown again. Cloze blanks are graded
// again with the pure grader; the next due date is not stored and is omitted.
func storedResult(a attempts.View, full problems.View) (attempts.Result, error) {
	g := attempts.GradeView{Correct: a.AutoCorrect, Score: a.Score, Rating: a.Rating, Scheduled: a.AutoCorrect != nil || a.Rating != nil}
	if g.Rating == nil && a.AutoCorrect != nil {
		def := grading.DefaultRating
		if !*a.AutoCorrect {
			def = grading.RatingAgain
		}
		g.Rating = &def
	}
	if full.Type == problems.TypeCloze {
		var p problems.ClozePayload
		var resp attempts.ClozeResponse
		if err := json.Unmarshal(full.Payload, &p); err != nil {
			return attempts.Result{}, fmt.Errorf("review: cloze payload: %w", err)
		}
		if err := json.Unmarshal(a.Response, &resp); err != nil {
			return attempts.Result{}, fmt.Errorf("review: cloze response: %w", err)
		}
		blanks := make([]grading.ClozeBlank, 0, len(p.Blanks))
		for _, b := range p.Blanks {
			blanks = append(blanks, grading.ClozeBlank{ID: b.ID, Accepted: b.Accepted, CaseSensitive: b.CaseSensitive, MaxEdits: b.MaxEdits})
		}
		g.Blanks = grading.GradeCloze(blanks, resp.Answers).Correct
	}
	return attempts.Result{Attempt: a, Grade: g, Answer: full.Payload, Explanation: full.Explanation}, nil
}

// reviewResponse builds the attempt response from the posted form, in the
// shape the problem's type takes.
func reviewResponse(pv problems.PromptView, r *http.Request) (json.RawMessage, error) {
	var v any
	switch pv.Type {
	case problems.TypeMCQ:
		sel := r.PostForm["selected"]
		if sel == nil {
			sel = []string{}
		}
		v = attempts.MCQResponse{Selected: sel}
	case problems.TypeCloze:
		ids, err := clozePromptIDs(pv.Payload)
		if err != nil {
			return nil, err
		}
		ans := make(map[string]string, len(ids))
		for id := range ids {
			ans[id] = r.PostFormValue("blank_" + id)
		}
		v = attempts.ClozeResponse{Answers: ans}
	default:
		v = attempts.OpenResponse{Text: r.PostFormValue("text")}
	}
	return json.Marshal(v)
}

func buildReviewResult(pv problems.PromptView, res attempts.Result) (*reviewResult, error) {
	out := &reviewResult{
		AttemptID: res.Attempt.ID.String(), Type: pv.Type,
		Explanation: RenderMarkdown(res.Explanation), Prompt: RenderMarkdown(pv.Prompt),
	}
	if c := res.Grade.Correct; c != nil {
		out.Verdict = map[bool]string{true: "right", false: "wrong"}[*c]
	}
	if res.Grade.Rating != nil {
		out.Applied = *res.Grade.Rating
	}
	if res.NextDue != nil {
		out.NextDue = res.NextDue.UTC().Format("2006-01-02 15:04 MST")
	}
	switch pv.Type {
	case problems.TypeMCQ:
		var full problems.MCQPayload
		var resp attempts.MCQResponse
		if err := json.Unmarshal(res.Answer, &full); err != nil {
			return nil, fmt.Errorf("review: mcq answer: %w", err)
		}
		if err := json.Unmarshal(res.Attempt.Response, &resp); err != nil {
			return nil, fmt.Errorf("review: mcq response: %w", err)
		}
		for _, c := range full.Choices {
			ch := reviewChoice{ID: c.ID, Text: c.Text}
			for _, s := range resp.Selected {
				ch.Picked = ch.Picked || s == c.ID
			}
			for _, s := range full.Correct {
				ch.Right = ch.Right || s == c.ID
			}
			out.Choices = append(out.Choices, ch)
		}
	case problems.TypeCloze:
		var full problems.ClozePayload
		var resp attempts.ClozeResponse
		if err := json.Unmarshal(res.Answer, &full); err != nil {
			return nil, fmt.Errorf("review: cloze answer: %w", err)
		}
		if err := json.Unmarshal(res.Attempt.Response, &resp); err != nil {
			return nil, fmt.Errorf("review: cloze response: %w", err)
		}
		known := map[string]bool{}
		for _, b := range full.Blanks {
			known[b.ID] = true
			out.Blanks = append(out.Blanks, reviewBlank{ID: b.ID, Accepted: strings.Join(b.Accepted, " / "), Right: res.Grade.Blanks[b.ID]})
		}
		out.Prompt = fillBlanks(out.Prompt, known, func(id string) string {
			cls := "wrong"
			if res.Grade.Blanks[id] {
				cls = "right"
			}
			given := html.EscapeString(resp.Answers[id])
			if given == "" {
				given = "(blank)"
			}
			return `<span class="blank-result ` + cls + `">` + given + `</span>`
		})
	default:
		var full problems.OpenPayload
		if err := json.Unmarshal(res.Answer, &full); err != nil {
			return nil, fmt.Errorf("review: open answer: %w", err)
		}
		out.Reference, out.Rubric = RenderMarkdown(full.ReferenceAnswer), full.Rubric
		out.NeedsRating = !res.Grade.Scheduled
	}
	// Rating buttons: open needs one of 1-4; a right auto-graded answer may
	// move Good (the default) to Hard or Easy; a wrong one is Again already.
	lo, hi := 0, 0
	switch {
	case res.Attempt.Rating != nil:
		// already rated: nothing left to choose
	case out.NeedsRating:
		lo, hi = 1, 4
	case res.Grade.Correct != nil && *res.Grade.Correct:
		lo, hi = 2, 4
	}
	for v := lo; lo > 0 && v <= hi; v++ {
		out.Ratings = append(out.Ratings, reviewRating{Value: v, Label: ratingLabels[v], Default: !out.NeedsRating && v == out.Applied})
	}
	return out, nil
}

// reviewRate sets the rating of an attempt and moves on.
func (w *Web) reviewRate(rw http.ResponseWriter, r *http.Request) {
	id, ok := reviewSessionID(rw, r)
	if !ok {
		return
	}
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	aid, err := domain.ParseID(r.PostFormValue("attempt_id"))
	rating, rerr := strconv.Atoi(r.PostFormValue("rating"))
	if err != nil || rerr != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	_, err = w.reviewPg.answers.Rate(r.Context(), domain.Actor{Kind: domain.ActorHuman, UserID: ws.UserID, ClientID: webClientID}, aid, rating)
	var pb *platform.Problem
	// An attempt that is already rated (a double submit) just moves on.
	if err != nil && !(errors.As(err, &pb) && pb.Status == http.StatusConflict && strings.Contains(pb.Detail, "already has a rating")) {
		w.failReview(rw, r, err)
		return
	}
	http.Redirect(rw, r, reviewPath(id), http.StatusSeeOther)
}

// failReview shows a client-facing problem as its status and detail, and
// anything else as an opaque 500.
func (w *Web) failReview(rw http.ResponseWriter, r *http.Request, err error) {
	var pb *platform.Problem
	if errors.As(err, &pb) && pb.Status >= 400 && pb.Status < 500 {
		if pb.Status == http.StatusNotFound {
			http.NotFound(rw, r)
			return
		}
		http.Error(rw, problemMessage(pb), pb.Status)
		return
	}
	if errors.Is(err, grading.ErrInvalidRating) || errors.Is(err, grading.ErrRatingRequired) {
		http.Error(rw, "that rating does not apply to this answer", http.StatusUnprocessableEntity)
		return
	}
	w.failProblemPage(rw, "review", err)
}

// problemMessage is a problem's detail followed by its field errors.
func problemMessage(pb *platform.Problem) string {
	msg := pb.Detail
	for _, f := range pb.Errors {
		msg += "; " + f.Field + ": " + f.Detail
	}
	return msg
}
