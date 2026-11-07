// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Transactor runs fn atomically with transaction-bound repositories.
// store.ProblemsTx adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

// TagResolver resolves a tag reference for list filtering. The tags service
// implements it; problems cannot import tags (tags imports problems).
type TagResolver interface {
	// ResolveTagFilter returns the ID of the owner's tag that ref (ID, slug or
	// alias) names, following a merge. found is false for an unknown tag.
	ResolveTagFilter(ctx context.Context, ownerID domain.ID, ref string) (id domain.ID, found bool, err error)
}

// Views accepted by Service.Get callers.
const (
	ViewFull   = "full"
	ViewPrompt = "prompt"
)

// ActorView is an Actor as the API shows it.
type ActorView struct {
	Kind     domain.ActorKind `json:"kind"`
	UserID   string           `json:"user_id"`
	ClientID string           `json:"client_id"`
}

func actorView(a domain.Actor) ActorView {
	v := ActorView{Kind: a.Kind, ClientID: a.ClientID}
	if !a.UserID.IsNil() {
		v.UserID = a.UserID.String()
	}
	return v
}

// View is a problem's current version with its answers (the "full" view).
type View struct {
	ID          domain.ID       `json:"id"`
	Status      Status          `json:"status"`
	Version     int             `json:"version"`
	Type        string          `json:"type"`
	Prompt      string          `json:"prompt"`
	Payload     json.RawMessage `json:"payload"`
	Explanation string          `json:"explanation"`
	Source      string          `json:"source"`
	CreatedBy   ActorView       `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ETag is a strong validator that changes whenever the problem's current
// version or its handle (status) changes.
func (v View) ETag() string {
	return fmt.Sprintf(`"%d-%d"`, v.Version, v.UpdatedAt.UnixMilli())
}

// PromptView is what a client shows while answering: no correct answers,
// reference answer, rubric or explanation.
type PromptView struct {
	ID      domain.ID       `json:"id"`
	Status  Status          `json:"status"`
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Prompt  string          `json:"prompt"`
	Payload json.RawMessage `json:"payload"`
}

type mcqPromptPayload struct {
	Choices  []Choice `json:"choices"`
	Multiple bool     `json:"multiple"`
	Shuffle  bool     `json:"shuffle"`
}

type clozePromptPayload struct {
	Blanks []clozePromptBlank `json:"blanks"`
}

type clozePromptBlank struct {
	ID string `json:"id"`
}

// CreateInput is the body of a create request.
type CreateInput struct {
	Type        string
	Prompt      string
	Payload     json.RawMessage
	Explanation string
	Source      string
	// Status is draft or live; empty means draft.
	Status Status
}

// ListQuery is a validated-on-use list request. The zero value lists the
// first page of every problem.
type ListQuery struct {
	Status        string
	Type          string
	CreatedByKind string
	Q             string
	Tag           string
	Cursor        string
	Limit         int // <= 0: platform.DefaultLimit
}

// Service holds the rules for problems.
type Service struct {
	repo    Repo
	tx      Transactor
	clock   domain.Clock
	ids     *domain.IDGenerator
	shuffle func(n int, swap func(i, j int))
	tags    TagResolver
	batch   BatchTransactor
	onMajor MajorEditHandler
}

// Option tunes a Service.
type Option func(*Service)

// WithShuffle replaces the shuffle used for MCQ prompt views (tests).
func WithShuffle(f func(n int, swap func(i, j int))) Option {
	return func(s *Service) { s.shuffle = f }
}

// WithTagResolver enables the tag filter of List. Without it a request that
// sets a tag is answered 422.
func WithTagResolver(r TagResolver) Option { return func(s *Service) { s.tags = r } }

// NewService builds a Service. repo serves reads; tx runs writes atomically.
func NewService(repo Repo, tx Transactor, clock domain.Clock, ids *domain.IDGenerator, opts ...Option) *Service {
	s := &Service{repo: repo, tx: tx, clock: clock, ids: ids, shuffle: rand.Shuffle, onMajor: noopMajorEdit{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// NewReader builds a Service meant for reads (Get, GetPrompt, List) over repo.
// The store uses it to hand another feature a problems service bound to that
// feature's own transaction; its write methods are not usable.
func NewReader(repo Repo) *Service {
	return NewService(repo, nil, domain.SystemClock{}, domain.NewIDGenerator(domain.SystemClock{}))
}

// Create validates in and stores a new problem with its first version, owned
// by actor's user. Validation failures are a *platform.Problem listing every
// invalid field.
func (s *Service) Create(ctx context.Context, actor domain.Actor, in CreateInput) (View, error) {
	if err := checkCreateActor(actor); err != nil {
		return View{}, err
	}
	if fields := validateCreate(in); len(fields) > 0 {
		return View{}, platform.Validation("the problem is invalid", fields...)
	}
	p, v, err := s.newProblem(actor, in)
	if err != nil {
		return View{}, err
	}
	err = s.tx.InTx(ctx, func(r Repo) error { return r.Create(ctx, actor.UserID, p, v) })
	if err != nil {
		return View{}, err
	}
	return newView(p, v), nil
}

func checkCreateActor(actor domain.Actor) error {
	// A system actor (seeding) is accepted when it acts for an owner.
	if err := actor.Validate(); err != nil || actor.UserID.IsNil() {
		return errors.New("problems: create needs an actor with a user")
	}
	return nil
}

// validateCreate lists every invalid field of in, rooted at the problem body.
func validateCreate(in CreateInput) []platform.FieldError {
	var fields []platform.FieldError
	if st := in.Status; st != "" && st != StatusDraft && st != StatusLive {
		fields = append(fields, platform.FieldError{Field: "status", Detail: "must be draft or live"})
	}
	if strings.TrimSpace(in.Prompt) == "" {
		fields = append(fields, platform.FieldError{Field: "prompt", Detail: "required"})
	}
	return append(fields, ValidatePayload(in.Type, in.Prompt, in.Payload)...)
}

// newProblem builds the rows of a validated create.
func (s *Service) newProblem(actor domain.Actor, in CreateInput) (Problem, Version, error) {
	status := in.Status
	if status == "" {
		status = StatusDraft
	}
	var payload bytes.Buffer
	if err := json.Compact(&payload, in.Payload); err != nil {
		return Problem{}, Version{}, platform.BadRequest("payload is not valid JSON")
	}
	now := s.clock.Now()
	id := s.ids.New()
	p := Problem{ID: id, OwnerID: actor.UserID, Status: status, CurrentVersion: 1,
		CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	v := Version{ProblemID: id, Version: 1, Type: in.Type, Prompt: in.Prompt, Payload: CanonicalPayload(in.Type, payload.Bytes()),
		Explanation: in.Explanation, Source: in.Source, ChangeKind: ChangeMajor, CreatedBy: actor, CreatedAt: now}
	return p, v, nil
}

// Get returns the owner's problem in the full view.
func (s *Service) Get(ctx context.Context, ownerID, id domain.ID) (View, error) {
	p, v, err := s.current(ctx, ownerID, id)
	if err != nil {
		return View{}, err
	}
	return newView(p, v), nil
}

// GetPrompt returns the owner's problem in the prompt view. MCQ choices are
// shuffled afresh on every call when the payload says shuffle.
func (s *Service) GetPrompt(ctx context.Context, ownerID, id domain.ID) (PromptView, error) {
	p, v, err := s.current(ctx, ownerID, id)
	if err != nil {
		return PromptView{}, err
	}
	payload, err := s.promptPayload(v)
	if err != nil {
		return PromptView{}, err
	}
	return PromptView{ID: p.ID, Status: p.Status, Version: p.CurrentVersion, Type: v.Type,
		Prompt: v.Prompt, Payload: payload}, nil
}

func (s *Service) current(ctx context.Context, ownerID, id domain.ID) (Problem, Version, error) {
	p, err := s.repo.Get(ctx, ownerID, id)
	if err != nil {
		return Problem{}, Version{}, notFound(err)
	}
	v, err := s.repo.GetVersion(ctx, ownerID, id, p.CurrentVersion)
	if err != nil {
		return Problem{}, Version{}, notFound(err)
	}
	return p, v, nil
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return platform.NotFound("no such problem")
	}
	return err
}

func (s *Service) promptPayload(v Version) (json.RawMessage, error) {
	switch v.Type {
	case TypeMCQ:
		var p MCQPayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return nil, fmt.Errorf("problems: stored mcq payload: %w", err)
		}
		out := mcqPromptPayload{Choices: append([]Choice{}, p.Choices...), Multiple: p.Multiple, Shuffle: p.Shuffle}
		if p.Shuffle {
			s.shuffle(len(out.Choices), func(i, j int) { out.Choices[i], out.Choices[j] = out.Choices[j], out.Choices[i] })
		}
		return json.Marshal(out)
	case TypeCloze:
		var p ClozePayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return nil, fmt.Errorf("problems: stored cloze payload: %w", err)
		}
		out := clozePromptPayload{Blanks: make([]clozePromptBlank, 0, len(p.Blanks))}
		for _, b := range p.Blanks {
			out.Blanks = append(out.Blanks, clozePromptBlank{ID: b.ID})
		}
		return json.Marshal(out)
	case TypeOpen:
		return json.RawMessage(`{}`), nil
	}
	return nil, fmt.Errorf("problems: stored problem has unknown type %q", v.Type)
}

// List returns one page of the owner's problems, oldest first.
func (s *Service) List(ctx context.Context, ownerID domain.ID, q ListQuery) (platform.PageResult[View], error) {
	var fields []platform.FieldError
	f := ListFilter{Query: strings.TrimSpace(q.Q)}
	if q.Status != "" {
		f.Status = Status(q.Status)
		if !f.Status.Valid() {
			fields = append(fields, platform.FieldError{Field: "status", Detail: "must be one of draft, live, archived"})
		}
	}
	if q.Type != "" {
		f.Type = q.Type
		if q.Type != TypeMCQ && q.Type != TypeCloze && q.Type != TypeOpen {
			fields = append(fields, platform.FieldError{Field: "type", Detail: "must be one of mcq, cloze, open"})
		}
	}
	if q.CreatedByKind != "" {
		f.CreatedByKind = domain.ActorKind(q.CreatedByKind)
		if !f.CreatedByKind.Valid() {
			fields = append(fields, platform.FieldError{Field: "created_by_kind", Detail: "must be one of human, agent, system"})
		}
	}
	if q.Tag != "" {
		if s.tags == nil {
			fields = append(fields, platform.FieldError{Field: "tag", Detail: "filtering by tag is not enabled"})
		} else {
			id, found, err := s.tags.ResolveTagFilter(ctx, ownerID, q.Tag)
			switch {
			case err != nil:
				return platform.PageResult[View]{}, err
			case !found:
				fields = append(fields, platform.FieldError{Field: "tag", Detail: "no such tag"})
			default:
				f.TagID = id
			}
		}
	}
	if q.Cursor != "" {
		after, err := decodeCursor(q.Cursor)
		if err != nil {
			fields = append(fields, platform.FieldError{Field: "cursor", Detail: "invalid cursor"})
		}
		f.After = after
	}
	if len(fields) > 0 {
		return platform.PageResult[View]{}, platform.Validation("invalid list parameters", fields...)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = platform.DefaultLimit
	}
	f.Limit = limit + 1 // one extra row says whether another page follows
	rows, err := s.repo.ListCurrent(ctx, ownerID, f)
	if err != nil {
		return platform.PageResult[View]{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(rows[limit-1].Problem.ID)
	}
	items := make([]View, 0, len(rows))
	for _, c := range rows {
		items = append(items, newView(c.Problem, c.Version))
	}
	return platform.NewPageResult(items, next), nil
}

func newView(p Problem, v Version) View {
	return View{ID: p.ID, Status: p.Status, Version: p.CurrentVersion, Type: v.Type, Prompt: v.Prompt,
		Payload: CanonicalPayload(v.Type, v.Payload), Explanation: v.Explanation, Source: v.Source, CreatedBy: actorView(p.CreatedBy),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func encodeCursor(id domain.ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func decodeCursor(c string) (domain.ID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return domain.Nil, err
	}
	return domain.ParseID(string(b))
}
