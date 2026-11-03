// SPDX-License-Identifier: Apache-2.0

package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// ClientID is the client_id seeded rows record in their actor.
const ClientID = "seed"

// ProblemsAPI is the part of the problems service the loader uses.
type ProblemsAPI interface {
	Create(ctx context.Context, actor domain.Actor, in problems.CreateInput) (problems.View, error)
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
	Edit(ctx context.Context, actor domain.Actor, id domain.ID, ifMatch string, in problems.EditInput) (problems.View, error)
	Publish(ctx context.Context, ownerID, id domain.ID, ifMatch string) (problems.View, error)
}

// TagsAPI is the part of the tags service the loader uses.
type TagsAPI interface {
	Create(ctx context.Context, ownerID domain.ID, in tags.CreateInput) (tags.Tag, error)
	Get(ctx context.Context, ownerID domain.ID, ref string) (tags.Tag, error)
	Update(ctx context.Context, ownerID domain.ID, ref, ifMatch string, patch tags.Patch) (tags.Tag, error)
	ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]tags.Tag, error)
	SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, refs []string) ([]tags.Tag, error)
}

// QuizzesAPI is the part of the quizzes service the loader uses.
type QuizzesAPI interface {
	Create(ctx context.Context, actor domain.Actor, in quizzes.CreateInput) (quizzes.Quiz, error)
	Get(ctx context.Context, ownerID, id domain.ID) (quizzes.Quiz, error)
	Update(ctx context.Context, ownerID, id domain.ID, ifMatch string, patch quizzes.Patch) (quizzes.Quiz, error)
	SetItems(ctx context.Context, ownerID, id domain.ID, ifMatch string, refs []string) (quizzes.Quiz, error)
}

// Services is everything one seeding run needs, bound to one transaction.
type Services struct {
	Problems ProblemsAPI
	Tags     TagsAPI
	Quizzes  QuizzesAPI
	Keys     KeyRepo
	Clock    domain.Clock
}

// Transactor runs fn with services that share one store transaction, so a
// file applies completely or not at all.
type Transactor interface {
	InTx(ctx context.Context, fn func(Services) error) error
}

// Counts tallies what a run did to one kind of item.
type Counts struct {
	Created   int
	Updated   int
	Unchanged int
	// Skipped items were left alone: deleted or retired since they were
	// seeded, or draft in the file while already live.
	Skipped int
}

// Report is the outcome of applying a file.
type Report struct {
	Tags     Counts
	Problems Counts
	Quizzes  Counts
}

// Loader applies seed files.
type Loader struct{ tx Transactor }

// NewLoader builds a Loader.
func NewLoader(tx Transactor) *Loader { return &Loader{tx: tx} }

// Apply upserts the file's tags, problems and quizzes for ownerID, in that
// order, in one transaction. An item whose key is already mapped is touched
// only if its content in the file changed since it was last applied; edits
// made through the API in between are otherwise left alone. Rows are recorded
// with a system actor acting for the owner.
func (l *Loader) Apply(ctx context.Context, ownerID domain.ID, f *File) (Report, error) {
	var rep Report
	if err := f.Validate(); err != nil {
		return rep, err
	}
	err := l.tx.InTx(ctx, func(sv Services) error {
		rep = Report{}
		a := applier{Services: sv, owner: ownerID,
			actor: domain.Actor{Kind: domain.ActorSystem, UserID: ownerID, ClientID: ClientID}}
		for i, t := range f.Tags {
			c, err := a.tag(ctx, t)
			if err != nil {
				return fmt.Errorf("seed: tags[%d] %q: %w", i, t.Key, err)
			}
			rep.Tags.add(c)
		}
		for i, p := range f.Problems {
			c, err := a.problem(ctx, p)
			if err != nil {
				return fmt.Errorf("seed: problems[%d] %q: %w", i, p.Key, err)
			}
			rep.Problems.add(c)
		}
		for i, q := range f.Quizzes {
			c, err := a.quiz(ctx, q)
			if err != nil {
				return fmt.Errorf("seed: quizzes[%d] %q: %w", i, q.Key, err)
			}
			rep.Quizzes.add(c)
		}
		return nil
	})
	return rep, err
}

type outcome int

const (
	created outcome = iota
	updated
	unchanged
	skipped
)

func (c *Counts) add(o outcome) {
	switch o {
	case created:
		c.Created++
	case updated:
		c.Updated++
	case unchanged:
		c.Unchanged++
	case skipped:
		c.Skipped++
	}
}

type applier struct {
	Services
	owner domain.ID
	actor domain.Actor
}

func (a applier) lookup(ctx context.Context, kind Kind, key string) (Entry, bool, error) {
	e, err := a.Keys.Get(ctx, a.owner, kind, key)
	if errors.Is(err, ErrNotFound) {
		return Entry{}, false, nil
	}
	return e, err == nil, err
}

func (a applier) put(ctx context.Context, kind Kind, key string, id domain.ID, dig string) error {
	return a.Keys.Put(ctx, a.owner, kind, key, Entry{EntityID: id, Digest: dig}, a.Clock.Now())
}

// missing reports whether err says the row is gone (never existed, deleted, or
// not the owner's).
func missing(err error) bool {
	var p *platform.Problem
	return errors.Is(err, problems.ErrNotFound) || errors.Is(err, tags.ErrNotFound) ||
		errors.Is(err, quizzes.ErrNotFound) || errors.As(err, &p) && p.Status == http.StatusNotFound
}

func (a applier) tag(ctx context.Context, t Tag) (outcome, error) {
	dig, err := digest(t)
	if err != nil {
		return 0, err
	}
	e, found, err := a.lookup(ctx, KindTag, t.Key)
	if err != nil {
		return 0, err
	}
	if !found {
		slug := t.Slug
		if slug == "" {
			slug = tags.Slugify(t.Name)
		}
		// A tag the owner already has under this slug is adopted, not
		// overwritten, and not duplicated.
		existing, err := a.Tags.Get(ctx, a.owner, slug)
		switch {
		case err == nil:
			if existing.Slug != slug {
				return 0, fmt.Errorf("slug %q is already an alias of tag %q", slug, existing.Slug)
			}
			return unchanged, a.put(ctx, KindTag, t.Key, existing.ID, dig)
		case !missing(err):
			return 0, err
		}
		nt, err := a.Tags.Create(ctx, a.owner, tags.CreateInput{Slug: slug, Name: t.Name, Description: t.Description})
		if err != nil {
			return 0, errors.New(fieldErr(err))
		}
		return created, a.put(ctx, KindTag, t.Key, nt.ID, dig)
	}
	if e.Digest == dig {
		return unchanged, nil
	}
	cur, err := a.Tags.Get(ctx, a.owner, e.EntityID.String())
	if err != nil && !missing(err) {
		return 0, err
	}
	if err != nil || cur.Status != tags.StatusActive {
		return skipped, nil
	}
	patch := tags.Patch{Name: &t.Name, Description: &t.Description}
	if t.Slug != "" {
		patch.Slug = &t.Slug
	}
	if _, err := a.Tags.Update(ctx, a.owner, cur.ID.String(), tags.ETag(cur), patch); err != nil {
		return 0, errors.New(fieldErr(err))
	}
	return updated, a.put(ctx, KindTag, t.Key, e.EntityID, dig)
}

// tagRefs maps tag keys to tag IDs; anything else is passed on as a tag
// reference (ID, slug or alias).
func (a applier) tagRefs(ctx context.Context, refs []string) ([]string, error) {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		e, found, err := a.lookup(ctx, KindTag, r)
		if err != nil {
			return nil, err
		}
		if found {
			r = e.EntityID.String()
		}
		out = append(out, r)
	}
	return out, nil
}

// addTags attaches refs to the problem, keeping tags it already has.
func (a applier) addTags(ctx context.Context, id domain.ID, refs []string) (bool, error) {
	if len(refs) == 0 {
		return false, nil
	}
	want, err := a.tagRefs(ctx, refs)
	if err != nil {
		return false, err
	}
	have, err := a.Tags.ProblemTags(ctx, a.owner, id)
	if err != nil {
		return false, err
	}
	all := make([]string, 0, len(have)+len(want))
	for _, t := range have {
		all = append(all, t.ID.String())
	}
	n := len(all)
	for _, w := range want {
		if t, err := a.Tags.Get(ctx, a.owner, w); err == nil {
			w = t.ID.String()
		}
		if !slices.Contains(all, w) {
			all = append(all, w)
		}
	}
	if len(all) == n {
		return false, nil
	}
	if _, err := a.Tags.SetProblemTags(ctx, a.owner, id, all); err != nil {
		return false, errors.New(fieldErr(err))
	}
	return true, nil
}

func (a applier) problem(ctx context.Context, p Problem) (outcome, error) {
	dig, err := digest(p)
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(p.Payload)
	if err != nil {
		return 0, err
	}
	if p.Payload == nil {
		payload = json.RawMessage(`{}`)
	}
	want := problems.StatusLive
	if p.Status == "draft" {
		want = problems.StatusDraft
	}
	e, found, err := a.lookup(ctx, KindProblem, p.Key)
	if err != nil {
		return 0, err
	}
	if !found {
		v, err := a.Problems.Create(ctx, a.actor, problems.CreateInput{Type: p.Type, Prompt: p.Prompt,
			Payload: payload, Explanation: p.Explanation, Source: p.Source, Status: want})
		if err != nil {
			return 0, errors.New(fieldErr(err))
		}
		if _, err := a.addTags(ctx, v.ID, p.Tags); err != nil {
			return 0, err
		}
		return created, a.put(ctx, KindProblem, p.Key, v.ID, dig)
	}
	if e.Digest == dig {
		return unchanged, nil
	}
	cur, err := a.Problems.Get(ctx, a.owner, e.EntityID)
	if missing(err) {
		return skipped, nil
	}
	if err != nil {
		return 0, err
	}

	out := unchanged
	if !sameContent(cur, p, payload) {
		kind := problems.ChangeMinor
		if p.Change == "major" {
			kind = problems.ChangeMajor
		}
		cur, err = a.Problems.Edit(ctx, a.actor, cur.ID, cur.ETag(), problems.EditInput{ChangeKind: kind,
			Type: &p.Type, Prompt: &p.Prompt, Payload: payload, Explanation: &p.Explanation, Source: &p.Source})
		if err != nil {
			return 0, errors.New(fieldErr(err))
		}
		out = updated
	}
	// Draft becomes live; nothing moves a live problem back (see
	// problems.CanTransition), and an archived one stays archived.
	if want == problems.StatusLive && cur.Status == problems.StatusDraft {
		if cur, err = a.Problems.Publish(ctx, a.owner, cur.ID, cur.ETag()); err != nil {
			return 0, errors.New(fieldErr(err))
		}
		out = updated
	}
	if changed, err := a.addTags(ctx, cur.ID, p.Tags); err != nil {
		return 0, err
	} else if changed {
		out = updated
	}
	return out, a.put(ctx, KindProblem, p.Key, cur.ID, dig)
}

func sameContent(cur problems.View, p Problem, payload json.RawMessage) bool {
	if cur.Type != p.Type || cur.Prompt != p.Prompt || cur.Explanation != p.Explanation || cur.Source != p.Source {
		return false
	}
	var a, b any
	if json.Unmarshal(cur.Payload, &a) != nil || json.Unmarshal(problems.CanonicalPayload(p.Type, payload), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func (a applier) quiz(ctx context.Context, q Quiz) (outcome, error) {
	dig, err := digest(q)
	if err != nil {
		return 0, err
	}
	status := quizzes.StatusLive
	if q.Status == "draft" {
		status = quizzes.StatusDraft
	}
	// Items are problem keys. A problem deleted since it was seeded drops out
	// of the quiz instead of failing the run.
	var items []string
	for _, k := range q.Problems {
		pe, found, err := a.lookup(ctx, KindProblem, k)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, fmt.Errorf("problems: %q is not a seeded problem key", k)
		}
		if _, err := a.Problems.Get(ctx, a.owner, pe.EntityID); err != nil {
			if missing(err) {
				continue
			}
			return 0, err
		}
		items = append(items, pe.EntityID.String())
	}

	e, found, err := a.lookup(ctx, KindQuiz, q.Key)
	if err != nil {
		return 0, err
	}
	if !found {
		nq, err := a.Quizzes.Create(ctx, a.actor, quizzes.CreateInput{Title: q.Title, Description: q.Description,
			Status: status, ProblemIDs: items})
		if err != nil {
			return 0, errors.New(fieldErr(err))
		}
		return created, a.put(ctx, KindQuiz, q.Key, nq.ID, dig)
	}
	if e.Digest == dig {
		return unchanged, nil
	}
	cur, err := a.Quizzes.Get(ctx, a.owner, e.EntityID)
	if missing(err) {
		return skipped, nil
	}
	if err != nil {
		return 0, err
	}
	st := string(status)
	if cur, err = a.Quizzes.Update(ctx, a.owner, cur.ID, quizzes.ETag(cur),
		quizzes.Patch{Title: &q.Title, Description: &q.Description, Status: &st}); err != nil {
		return 0, errors.New(fieldErr(err))
	}
	if !sameItems(cur.Items, items) {
		if cur, err = a.Quizzes.SetItems(ctx, a.owner, cur.ID, quizzes.ETag(cur), items); err != nil {
			return 0, errors.New(fieldErr(err))
		}
	}
	return updated, a.put(ctx, KindQuiz, q.Key, cur.ID, dig)
}

func sameItems(have []domain.ID, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i].String() != want[i] {
			return false
		}
	}
	return true
}
