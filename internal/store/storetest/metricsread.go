// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// runMetricsReads covers the inputs the metrics endpoints read beyond the
// projection itself: tag labels and the studied cards with their problems.
func runMetricsReads(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	other := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	otherActor := domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}
	repo := f.s.Mastery()

	tagX, tagY := f.newTag(f.user, "x"), f.newTag(f.user, "y")
	retired := f.newTag(f.user, "retired")
	if err := f.s.Tags().Retire(f.ctx, f.user, retired, t0); err != nil {
		t.Fatal(err)
	}
	p1 := f.newProblem(pkinds[0], problems.StatusLive)
	p2 := f.newProblem(pkinds[1], problems.StatusLive)
	unstudied := f.newProblem(pkinds[0], problems.StatusLive)
	gone := f.newProblem(pkinds[0], problems.StatusLive)
	f.tag(f.user, p1.ID, tagX)
	f.tag(f.user, p2.ID, tagX, tagY)
	f.tag(f.user, unstudied.ID, tagY)
	theirs, err := f.probs.Create(f.ctx, otherActor, problems.CreateInput{Type: pkinds[0].typ, Prompt: pkinds[0].prompt,
		Payload: []byte(pkinds[0].payload), Status: problems.StatusLive})
	if err != nil {
		t.Fatal(err)
	}
	_ = theirs

	f.clock.t = t0.Add(time.Hour)
	for _, p := range []problems.View{p1, p2, gone} {
		k := pkinds[0]
		if p.ID == p2.ID {
			k = pkinds[1]
		}
		if _, err := f.answer(p.ID, k.right, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.remove(gone.ID)

	t.Run("tag labels", func(t *testing.T) {
		got, err := repo.TagLabels(f.ctx, f.user)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("TagLabels = %+v, want the two active tags (not the retired one)", got)
		}
		for _, l := range got {
			switch l.ID {
			case tagX:
				if l.Slug != "x" || l.Name != "x" {
					t.Errorf("label of x = %+v", l)
				}
			case tagY:
				if l.Slug != "y" || l.Name != "y" {
					t.Errorf("label of y = %+v", l)
				}
			default:
				t.Errorf("unexpected label %+v", l)
			}
		}
		if got[0].ID.String() >= got[1].ID.String() {
			t.Errorf("TagLabels not ordered by tag ID: %+v", got)
		}
		if l, err := repo.TagLabels(f.ctx, other.ID); err != nil || len(l) != 0 {
			t.Errorf("another owner's TagLabels = %+v, %v; want none", l, err)
		}
	})

	t.Run("studied cards", func(t *testing.T) {
		got, err := repo.StudiedCards(f.ctx, f.user)
		if err != nil {
			t.Fatal(err)
		}
		// The unanswered and the deleted problems are excluded.
		if len(got) != 2 {
			t.Fatalf("StudiedCards = %d, want p1 and p2 only", len(got))
		}
		for _, pc := range got {
			want, k := p1, pkinds[0]
			if pc.ProblemID == p2.ID {
				want, k = p2, pkinds[1]
			} else if pc.ProblemID != p1.ID {
				t.Fatalf("unexpected problem %v", pc.ProblemID)
			}
			c, ok := f.card(want.ID)
			if !ok {
				t.Fatalf("no card for %v", want.ID)
			}
			if pc.Prompt != k.prompt || pc.Type != string(k.typ) {
				t.Errorf("problem %v: type %q prompt %q, want %q %q", want.ID, pc.Type, pc.Prompt, k.typ, k.prompt)
			}
			if pc.Card.Reps != c.Reps || pc.Card.Stability != c.Stability || !pc.Card.Due.Equal(c.Due) ||
				!pc.Card.LastReview.Equal(c.LastReview) || pc.Card.State != c.State || pc.Card.Epoch != c.Epoch ||
				pc.Card.UserID != f.user || pc.Card.ProblemID != want.ID {
				t.Errorf("card = %+v, want %+v", pc.Card, c)
			}
		}
		if got[0].ProblemID.String() >= got[1].ProblemID.String() {
			t.Errorf("StudiedCards not ordered by problem ID")
		}
		if l, err := repo.StudiedCards(f.ctx, other.ID); err != nil || len(l) != 0 {
			t.Errorf("another owner's StudiedCards = %d, %v; want none", len(l), err)
		}

		// A major edit starts a new epoch whose card has no review yet, and an
		// archived problem has no live card: neither is studied any more.
		f.edit(p1.ID, problems.ChangeMajor)
		f.archive(p2.ID)
		got, err = repo.StudiedCards(f.ctx, f.user)
		if err != nil || len(got) != 0 {
			t.Errorf("StudiedCards after a major edit and an archive = %d, %v; want none", len(got), err)
		}
	})
}
