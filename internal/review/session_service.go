// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// ReviewPlanner builds review sessions. *Builder implements it.
type ReviewPlanner interface {
	Build(ctx context.Context, userID, sessionID domain.ID, now time.Time) (Plan, error)
	AppendDue(ctx context.Context, userID domain.ID, outstanding []domain.ID, now time.Time) ([]Item, error)
}

// QuizPlanner builds quiz sessions. *QuizBuilder implements it.
type QuizPlanner interface {
	Build(ctx context.Context, userID, sessionID, quizID domain.ID, shuffle bool) (Plan, error)
}

// SessionAttempts reads the answers given in a session. attempts.Repo
// implements it.
type SessionAttempts interface {
	List(ctx context.Context, ownerID domain.ID, f attempts.ListFilter) ([]attempts.Attempt, error)
}

// SessionScope is everything the session service reads and writes, bound to
// one transaction.
type SessionScope struct {
	Sessions SessionRepo
	Attempts SessionAttempts
	Review   ReviewPlanner
	Quiz     QuizPlanner
	Problems ProblemSource
}

// SessionTransactor runs fn atomically over a transaction-bound SessionScope.
// The store adapts a store.Store to it (store.Cards.SessionTx).
type SessionTransactor interface {
	InTx(ctx context.Context, fn func(SessionScope) error) error
}

// PromptSource renders a problem as a client shows it while answering. The
// problems service implements it.
type PromptSource interface {
	GetPrompt(ctx context.Context, ownerID, id domain.ID) (problems.PromptView, error)
}

// SessionRecorder is told once for every session handed to a client.
// platform.Metrics implements it.
type SessionRecorder interface{ RecordSessionServed() }

// SessionService holds the rules of review and quiz sessions: building and
// freezing the list, serving it one item at a time, and completing it.
type SessionService struct {
	tx      SessionTransactor
	prompts PromptSource
	rec     SessionRecorder
	clock   domain.Clock
	ids     *domain.IDGenerator
}

// NewSessionService builds a SessionService. rec may be nil.
func NewSessionService(tx SessionTransactor, prompts PromptSource, rec SessionRecorder, clock domain.Clock, ids *domain.IDGenerator) *SessionService {
	return &SessionService{tx: tx, prompts: prompts, rec: rec, clock: clock, ids: ids}
}

// appendRetries bounds how often Next rereads a session whose list another
// request extended at the same moment.
const appendRetries = 3

// CreateSessionInput is a request to start a session.
type CreateSessionInput struct {
	Kind SessionKind
	// QuizID is the quiz to serve; required for a quiz, forbidden for a review.
	QuizID string
	// Shuffle orders a quiz by the session ID rather than as authored; it is
	// refused for a review session.
	Shuffle bool
}

// SessionItemView is one entry of a session's frozen list.
type SessionItemView struct {
	Position  int       `json:"position"`
	ProblemID domain.ID `json:"problem_id"`
	CardEpoch int       `json:"card_epoch"`
	Kind      ItemKind  `json:"kind"`
	Answered  bool      `json:"answered"`
}

// SessionView is a session as the API shows it.
type SessionView struct {
	ID          domain.ID         `json:"id"`
	Kind        SessionKind       `json:"kind"`
	QuizID      *domain.ID        `json:"quiz_id"`
	Items       []SessionItemView `json:"items"`
	Answered    int               `json:"answered"`
	CreatedAt   time.Time         `json:"created_at"`
	CompletedAt *time.Time        `json:"completed_at"`
}

// NextItemView is the item Next serves.
type NextItemView struct {
	Position int                 `json:"position"`
	Kind     ItemKind            `json:"kind"`
	Problem  problems.PromptView `json:"problem"`
}

// NextView is what Next returns: the item to show (nil when nothing is left)
// and how many unanswered, servable items remain, that one included.
type NextView struct {
	SessionID domain.ID     `json:"session_id"`
	Remaining int           `json:"remaining"`
	Item      *NextItemView `json:"item"`
}

// Create builds and freezes a session for the user and counts it as served.
//
// A review session is built at the current time from the user's due cards and
// new problems; a quiz session from the quiz's live problems. A plan with
// nothing in it is a valid, empty session: having nothing due is a normal
// outcome, not an error. Quiz answers are ordinary attempts, so they count
// toward the daily review and new-card limits (which are measured against the
// attempt log) exactly like review answers.
func (s *SessionService) Create(ctx context.Context, userID domain.ID, in CreateSessionInput) (SessionView, error) {
	var quizID domain.ID
	var fields []platform.FieldError
	switch in.Kind {
	case SessionReview:
		if in.QuizID != "" {
			fields = append(fields, platform.FieldError{Field: "quiz_id", Detail: "not allowed for a review session"})
		}
		if in.Shuffle {
			fields = append(fields, platform.FieldError{Field: "shuffle", Detail: "only a quiz session can be shuffled"})
		}
	case SessionQuiz:
		id, err := domain.ParseID(in.QuizID)
		switch {
		case in.QuizID == "":
			fields = append(fields, platform.FieldError{Field: "quiz_id", Detail: "required for a quiz session"})
		case err != nil:
			fields = append(fields, platform.FieldError{Field: "quiz_id", Detail: "not a valid ID"})
		default:
			quizID = id
		}
	default:
		fields = append(fields, platform.FieldError{Field: "kind", Detail: "must be review or quiz"})
	}
	if len(fields) > 0 {
		return SessionView{}, platform.Validation("the session is invalid", fields...)
	}

	id, now := s.ids.New(), s.clock.Now()
	var out Session
	err := s.tx.InTx(ctx, func(sc SessionScope) error {
		var plan Plan
		var err error
		if in.Kind == SessionReview {
			plan, err = sc.Review.Build(ctx, userID, id, now)
		} else {
			plan, err = sc.Quiz.Build(ctx, userID, id, quizID, in.Shuffle)
		}
		if err != nil {
			return err
		}
		out = Session{ID: id, UserID: userID, Kind: in.Kind, QuizID: quizID, Items: plan.Items, CreatedAt: now}
		return sc.Sessions.Create(ctx, userID, out)
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return SessionView{}, platform.NotFound("no such quiz")
	case errors.Is(err, ErrQuizNotLive):
		return SessionView{}, platform.Validation("the session is invalid",
			platform.FieldError{Field: "quiz_id", Detail: "the quiz is not live; publish it first"})
	case err != nil:
		return SessionView{}, err
	}
	if s.rec != nil {
		s.rec.RecordSessionServed()
	}
	return newSessionView(out, make([]bool, len(out.Items))), nil
}

// Get returns the user's session with each item marked answered or not.
func (s *SessionService) Get(ctx context.Context, userID, id domain.ID) (SessionView, error) {
	var v SessionView
	err := s.tx.InTx(ctx, func(sc SessionScope) error {
		sess, answered, err := loadSession(ctx, sc, userID, id)
		if err != nil {
			return err
		}
		v = newSessionView(sess, answered)
		return nil
	})
	return v, sessionError(err)
}

// Complete marks the user's session completed. Completing a completed session
// is not an error and keeps its first completion time.
func (s *SessionService) Complete(ctx context.Context, userID, id domain.ID) (SessionView, error) {
	var v SessionView
	err := s.tx.InTx(ctx, func(sc SessionScope) error {
		if err := sc.Sessions.Complete(ctx, userID, id, s.clock.Now()); err != nil {
			return err
		}
		sess, answered, err := loadSession(ctx, sc, userID, id)
		if err != nil {
			return err
		}
		v = newSessionView(sess, answered)
		return nil
	})
	return v, sessionError(err)
}

// Next serves the first unanswered item whose problem is still live.
//
// For a review session it first appends the learning-step cards that have
// come due since the session was built (Builder.AppendDue, given the problems
// still waiting in the list), so a card answered Again returns once its step
// has elapsed. A completed session serves nothing: that is a conflict.
func (s *SessionService) Next(ctx context.Context, userID, id domain.ID) (NextView, error) {
	var (
		candidates []int // positions of unanswered, live items, in order
		sess       Session
	)
	for try := 0; ; try++ {
		candidates = candidates[:0]
		err := s.tx.InTx(ctx, func(sc SessionScope) error {
			var answered []bool
			var err error
			sess, answered, err = loadSession(ctx, sc, userID, id)
			if err != nil {
				return err
			}
			if sess.CompletedAt != nil {
				return errSessionCompleted
			}
			if sess.Kind == SessionReview {
				var waiting []domain.ID
				for i, it := range sess.Items {
					if !answered[i] {
						waiting = append(waiting, it.ProblemID)
					}
				}
				extra, err := sc.Review.AppendDue(ctx, userID, waiting, s.clock.Now())
				if err != nil {
					return err
				}
				if len(extra) > 0 {
					if err := sc.Sessions.AppendItems(ctx, userID, id, len(sess.Items), extra); err != nil {
						return err
					}
					sess.Items = append(sess.Items, extra...)
					answered = append(answered, make([]bool, len(extra))...)
				}
			}
			live, err := sc.Problems.LiveEpochs(ctx, userID)
			if err != nil {
				return err
			}
			isLive := make(map[domain.ID]bool, len(live))
			for _, pe := range live {
				isLive[pe.ID] = pe.Live
			}
			for i, it := range sess.Items {
				if !answered[i] && isLive[it.ProblemID] {
					candidates = append(candidates, i)
				}
			}
			return nil
		})
		if errors.Is(err, ErrSessionChanged) && try+1 < appendRetries {
			continue // another request extended the list; reread and recompute
		}
		if errors.Is(err, ErrSessionChanged) {
			return NextView{}, platform.Conflict("the session is changing; try again")
		}
		if err != nil {
			return NextView{}, sessionError(err)
		}
		break
	}

	// Prompts come from the problems service, outside the transaction. A
	// problem retired in between is skipped like one retired before.
	view := NextView{SessionID: id, Remaining: len(candidates)}
	for n, pos := range candidates {
		it := sess.Items[pos]
		pv, err := s.prompts.GetPrompt(ctx, userID, it.ProblemID)
		var pb *platform.Problem
		switch {
		case errors.As(err, &pb) && pb.Status == 404:
			view.Remaining = len(candidates) - n - 1
			continue
		case err != nil:
			return NextView{}, err
		case pv.Status != problems.StatusLive:
			view.Remaining = len(candidates) - n - 1
			continue
		}
		view.Item = &NextItemView{Position: pos, Kind: it.Kind, Problem: pv}
		return view, nil
	}
	view.Remaining = 0
	return view, nil
}

var errSessionCompleted = platform.Conflict("the session is completed")

// sessionError maps repository errors to API problems.
func sessionError(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return platform.NotFound("no such session")
	}
	return err
}

// loadSession reads the session and works out which items were answered.
func loadSession(ctx context.Context, sc SessionScope, userID, id domain.ID) (Session, []bool, error) {
	sess, err := sc.Sessions.Get(ctx, userID, id)
	if err != nil {
		return Session{}, nil, err
	}
	log, err := sc.Attempts.List(ctx, userID, attempts.ListFilter{SessionID: id})
	if err != nil {
		return Session{}, nil, err
	}
	return sess, answeredItems(sess.Items, log), nil
}

// answeredItems marks which items of a session's list were answered. The
// attempt log is the source of truth: each attempt with the session's ID
// answers one item for its problem, the earliest still-unanswered one when the
// list holds the problem more than once (a learning card served again).
func answeredItems(items []Item, log []attempts.Attempt) []bool {
	count := make(map[domain.ID]int, len(log))
	for _, a := range log {
		count[a.ProblemID]++
	}
	out := make([]bool, len(items))
	for i, it := range items {
		if count[it.ProblemID] > 0 {
			count[it.ProblemID]--
			out[i] = true
		}
	}
	return out
}

func newSessionView(s Session, answered []bool) SessionView {
	v := SessionView{ID: s.ID, Kind: s.Kind, Items: make([]SessionItemView, len(s.Items)),
		CreatedAt: s.CreatedAt, CompletedAt: s.CompletedAt}
	if !s.QuizID.IsNil() {
		q := s.QuizID
		v.QuizID = &q
	}
	for i, it := range s.Items {
		v.Items[i] = SessionItemView{Position: i, ProblemID: it.ProblemID, CardEpoch: it.Epoch, Kind: it.Kind, Answered: answered[i]}
		if answered[i] {
			v.Answered++
		}
	}
	return v
}
