// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Transactor runs fn atomically with transaction-bound repositories.
// store.AttemptsTx adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

// CardTransactor is a Transactor that also supplies the card updater bound to
// the attempt's own transaction, so the card is written atomically with the
// attempt. When a Service's Transactor implements it, the updater it supplies
// is used and the one set with WithCardUpdater is not.
type CardTransactor interface {
	Transactor
	InCardTx(ctx context.Context, fn func(Repo, CardUpdater) error) error
}

// AttemptRecorder is told once for every attempt that is stored by Answer.
// platform.Metrics implements it.
type AttemptRecorder interface{ RecordAttempt() }

// ProblemSource looks up the problem being answered. The problems service
// implements it.
type ProblemSource interface {
	ForAnswer(ctx context.Context, ownerID, id domain.ID) (problems.AnswerSubject, error)
}

// SessionChecker tells whether a review or quiz session belongs to the user.
// The review sessions service implements it. A session that does not exist and
// one owned by another user are both reported as false.
type SessionChecker interface {
	Exists(ctx context.Context, ownerID, id domain.ID) (bool, error)
}

// CardUpdater is called inside the attempt's transaction, after the attempt is
// stored (or its missing rating filled) and only when it is scheduled, with the
// rating that applies. It returns the card's next due time, or nil when it has
// none to report. The card projection (review.Projector) is the real
// implementation, bound to the transaction by a CardTransactor; it refolds the
// card from the stored attempts and so does not depend on the rating argument.
// NoopCardUpdater is the default.
type CardUpdater interface {
	Apply(ctx context.Context, a Attempt, rating int) (nextDue *time.Time, err error)
}

// NoopCardUpdater updates no card and reports no due date.
type NoopCardUpdater struct{}

// Apply implements CardUpdater.
func (NoopCardUpdater) Apply(context.Context, Attempt, int) (*time.Time, error) { return nil, nil }

// AnswerInput is a request to record an attempt.
type AnswerInput struct {
	ProblemID      domain.ID
	ProblemVersion int
	// Response is the raw JSON answer; its shape follows the problem's type.
	Response  json.RawMessage
	LatencyMS int
	// SessionID is empty for an answer outside a session.
	SessionID string
	// Rating is the rating the client chose (1-4), or nil.
	Rating *int
}

// GradeView is the grade as the API shows it.
type GradeView struct {
	// Correct is whether the answer was fully right; nil for open problems.
	Correct *bool `json:"correct"`
	// Score is 0-1; nil for an open attempt that has no rating yet.
	Score *float64 `json:"score"`
	// Rating is the rating that applies now; nil for an unrated open attempt.
	Rating *int `json:"rating"`
	// Scheduled is false while an open attempt waits for its rating.
	Scheduled bool `json:"scheduled"`
	// Blanks reports each cloze blank, by blank id.
	Blanks map[string]bool `json:"blanks,omitempty"`
}

// View is a stored attempt as the API shows it.
type View struct {
	ID             domain.ID          `json:"id"`
	ProblemID      domain.ID          `json:"problem_id"`
	ProblemVersion int                `json:"problem_version"`
	CardEpoch      int                `json:"card_epoch"`
	SessionID      *string            `json:"session_id"`
	Response       json.RawMessage    `json:"response"`
	AutoCorrect    *bool              `json:"auto_correct"`
	Score          *float64           `json:"score"`
	Rating         *int               `json:"rating"`
	LatencyMS      int                `json:"latency_ms"`
	AnsweredAt     time.Time          `json:"answered_at"`
	Client         problems.ActorView `json:"client"`
}

// Result is what answering returns: the stored attempt, its grade, the full
// answer, the explanation and the card's next due time.
type Result struct {
	Attempt     View            `json:"attempt"`
	Grade       GradeView       `json:"grade"`
	Answer      json.RawMessage `json:"answer"`
	Explanation string          `json:"explanation"`
	NextDue     *time.Time      `json:"next_due"`
}

// Service holds the rules of the answer flow.
type Service struct {
	tx       Transactor
	problems ProblemSource
	sessions SessionChecker
	cards    CardUpdater
	rec      AttemptRecorder
	reads    Repo
	tags     TagResolver
	clock    domain.Clock
	ids      *domain.IDGenerator
}

// NewService builds a Service. The card updater defaults to NoopCardUpdater;
// pass WithCardUpdater to replace it.
func NewService(tx Transactor, src ProblemSource, clock domain.Clock, ids *domain.IDGenerator, opts ...Option) *Service {
	s := &Service{tx: tx, problems: src, cards: NoopCardUpdater{}, clock: clock, ids: ids}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option tunes a Service.
type Option func(*Service)

// WithAttemptRecorder sets who is told about each stored attempt.
func WithAttemptRecorder(r AttemptRecorder) Option { return func(s *Service) { s.rec = r } }

// WithSessions sets the check that a session_id names one of the actor's own
// sessions. Without it a session_id is only validated as an ID.
func WithSessions(c SessionChecker) Option { return func(s *Service) { s.sessions = c } }

// inTx runs fn in a transaction with the card updater for it.
func (s *Service) inTx(ctx context.Context, fn func(Repo, CardUpdater) error) error {
	if ct, ok := s.tx.(CardTransactor); ok {
		return ct.InCardTx(ctx, fn)
	}
	return s.tx.InTx(ctx, func(r Repo) error { return fn(r, s.cards) })
}

// WithCardUpdater sets the hook that updates the card inside the attempt's
// transaction.
func WithCardUpdater(c CardUpdater) Option { return func(s *Service) { s.cards = c } }

// Answer grades in, stores the attempt and updates the card in one
// transaction. Only a human may answer. Failures that are the client's to fix
// are *platform.Problem; an invalid rating is reported as an error wrapping
// grading.ErrInvalidRating (the handler renders it).
func (s *Service) Answer(ctx context.Context, actor domain.Actor, in AnswerInput) (Result, error) {
	if actor.Kind != domain.ActorHuman || actor.UserID.IsNil() {
		return Result{}, platform.Forbidden("only a human can answer problems")
	}
	var sessionID domain.ID
	if fields := validateInput(in, &sessionID); len(fields) > 0 {
		return Result{}, platform.Validation("the attempt is invalid", fields...)
	}

	// An unknown session and another user's look the same: a 422 on session_id.
	// The spec has no rule on completed sessions, so none is added here.
	if s.sessions != nil && !sessionID.IsNil() {
		ok, err := s.sessions.Exists(ctx, actor.UserID, sessionID)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, platform.Validation("the attempt is invalid",
				platform.FieldError{Field: "session_id", Detail: "no such session"})
		}
	}

	subj, err := s.problems.ForAnswer(ctx, actor.UserID, in.ProblemID)
	if err != nil {
		return Result{}, err
	}
	if subj.Status != problems.StatusLive {
		return Result{}, platform.Conflict(fmt.Sprintf("the problem is %s, not live", subj.Status))
	}
	if in.ProblemVersion != subj.Version {
		p := platform.NewProblem(platform.TypeStaleProblemVersion, http.StatusConflict,
			fmt.Sprintf("problem_version %d is not current; the current version is %d", in.ProblemVersion, subj.Version))
		return Result{}, p
	}

	g, err := gradeResponse(subj, in.Response)
	if err != nil {
		return Result{}, err
	}
	rating, scheduled, score, err := resolveRating(subj.Type, g, in.Rating)
	if err != nil {
		return Result{}, err
	}

	a := Attempt{
		ID: s.ids.New(), UserID: actor.UserID, ProblemID: subj.ID, ProblemVersion: subj.Version,
		CardEpoch: subj.CardEpoch, SessionID: sessionID, Response: g.response,
		Score: score, Rating: in.Rating, LatencyMS: in.LatencyMS, AnsweredAt: s.clock.Now(), Client: actor,
	}
	if g.auto {
		c := g.correct
		a.AutoCorrect = &c
	}

	var nextDue *time.Time
	err = s.inTx(ctx, func(r Repo, cards CardUpdater) error {
		if err := r.Create(ctx, actor.UserID, a); err != nil {
			return err
		}
		if !scheduled {
			return nil
		}
		nextDue, err = cards.Apply(ctx, a, rating)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	if s.rec != nil {
		s.rec.RecordAttempt()
	}

	gv := GradeView{Score: score, Scheduled: scheduled, Blanks: g.blanks}
	if g.auto {
		c := g.correct
		gv.Correct = &c
	}
	if scheduled {
		gv.Rating = &rating
	}
	return Result{Attempt: newView(a), Grade: gv, Answer: subj.Payload, Explanation: subj.Explanation, NextDue: nextDue}, nil
}

// resolveRating applies the shared rating rules. For an auto-graded problem
// the rating that applies is always known; an open problem without a chosen
// rating is stored unscheduled and unscored until it is rated.
func resolveRating(typ string, g graded, chosen *int) (rating int, scheduled bool, score *float64, err error) {
	if chosen != nil && (*chosen < grading.RatingAgain || *chosen > grading.RatingEasy) {
		return 0, false, nil, platform.Validation("the attempt is invalid",
			platform.FieldError{Field: "rating", Detail: "must be between 1 and 4"})
	}
	if !g.auto && chosen == nil {
		return 0, false, nil, nil
	}
	rating, err = grading.ResolveRating(typ, g.correct, chosen)
	if err != nil {
		return 0, false, nil, err
	}
	sc := g.score
	if !g.auto {
		if sc, err = grading.OpenScore(rating); err != nil {
			return 0, false, nil, err
		}
	}
	return rating, true, &sc, nil
}

func validateInput(in AnswerInput, session *domain.ID) []platform.FieldError {
	var fields []platform.FieldError
	if in.ProblemID.IsNil() {
		fields = append(fields, platform.FieldError{Field: "problem_id", Detail: "required"})
	}
	if in.ProblemVersion < 1 {
		fields = append(fields, platform.FieldError{Field: "problem_version", Detail: "must be at least 1"})
	}
	if in.LatencyMS < 0 {
		fields = append(fields, platform.FieldError{Field: "latency_ms", Detail: "must not be negative"})
	}
	if len(in.Response) == 0 || string(in.Response) == "null" {
		fields = append(fields, platform.FieldError{Field: "response", Detail: "required"})
	}
	if in.SessionID != "" {
		id, err := domain.ParseID(in.SessionID)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "session_id", Detail: "not a valid ID"})
		}
		*session = id
	}
	return fields
}

// RatingWindow is how long after an attempt its missing rating may still be
// set. After it, the default rating stands.
const RatingWindow = 15 * time.Minute

// Rate sets the missing rating of the actor's attempt, once, within
// RatingWindow of the attempt. It applies the same rating rules as Answer,
// fills the score of an open attempt, and runs the card hook in the same
// transaction. An attempt that is already rated, or whose window has closed,
// is a 409; a rating that does not fit the outcome is a 422 on the rating
// field (rendered by the handler from the grading error).
func (s *Service) Rate(ctx context.Context, actor domain.Actor, id domain.ID, rating int) (Result, error) {
	if actor.Kind != domain.ActorHuman || actor.UserID.IsNil() {
		return Result{}, platform.Forbidden("only a human can rate attempts")
	}
	if rating < grading.RatingAgain || rating > grading.RatingEasy {
		return Result{}, platform.Validation("the rating is invalid",
			platform.FieldError{Field: "rating", Detail: "must be between 1 and 4"})
	}

	// Look the attempt up first to find its problem; the rules run again,
	// authoritatively, inside the transaction below.
	var first Attempt
	if err := s.tx.InTx(ctx, func(r Repo) (err error) { first, err = r.Get(ctx, actor.UserID, id); return err }); err != nil {
		return Result{}, notFoundAttempt(err)
	}
	subj, err := s.problems.ForAnswer(ctx, actor.UserID, first.ProblemID)
	if err != nil {
		return Result{}, err
	}

	var (
		a       Attempt
		score   *float64
		nextDue *time.Time
		applied int
	)
	err = s.inTx(ctx, func(r Repo, cards CardUpdater) error {
		var err error
		if a, err = r.Get(ctx, actor.UserID, id); err != nil {
			return err
		}
		if a.Rating != nil {
			return platform.Conflict("the attempt already has a rating")
		}
		if s.clock.Now().Sub(a.AnsweredAt) > RatingWindow {
			return platform.Conflict("the rating window has closed; it is open for 15 minutes after the attempt")
		}
		// An attempt with no auto_correct is an open one: its rating is required
		// and gives its score. Any other type follows the mcq/cloze rules.
		typ, correct := problems.TypeMCQ, false
		if a.AutoCorrect == nil {
			typ = problems.TypeOpen
		} else {
			correct = *a.AutoCorrect
		}
		if applied, err = grading.ResolveRating(typ, correct, &rating); err != nil {
			return err
		}
		if a.AutoCorrect == nil {
			sc, err := grading.OpenScore(applied)
			if err != nil {
				return err
			}
			score = &sc
		}
		if err := r.SetRating(ctx, actor.UserID, id, rating, score); err != nil {
			return err
		}
		a.Rating = &rating
		if score != nil {
			a.Score = score
		}
		nextDue, err = cards.Apply(ctx, a, applied)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyRated) {
			return Result{}, platform.Conflict("the attempt already has a rating")
		}
		return Result{}, notFoundAttempt(err)
	}

	gv := GradeView{Score: a.Score, Rating: &applied, Scheduled: true}
	if a.AutoCorrect != nil {
		c := *a.AutoCorrect
		gv.Correct = &c
	}
	return Result{Attempt: newView(a), Grade: gv, Answer: subj.Payload, Explanation: subj.Explanation, NextDue: nextDue}, nil
}

// notFoundAttempt turns the repo's ErrNotFound into a 404 problem.
func notFoundAttempt(err error) error {
	if errors.Is(err, ErrNotFound) {
		return platform.NotFound("no such attempt")
	}
	return err
}

func newView(a Attempt) View {
	v := View{ID: a.ID, ProblemID: a.ProblemID, ProblemVersion: a.ProblemVersion, CardEpoch: a.CardEpoch,
		Response: a.Response, AutoCorrect: a.AutoCorrect, Score: a.Score, Rating: a.Rating,
		LatencyMS: a.LatencyMS, AnsweredAt: a.AnsweredAt,
		Client: problems.ActorView{Kind: a.Client.Kind, ClientID: a.Client.ClientID}}
	if !a.Client.UserID.IsNil() {
		v.Client.UserID = a.Client.UserID.String()
	}
	if !a.SessionID.IsNil() {
		s := a.SessionID.String()
		v.SessionID = &s
	}
	return v
}
