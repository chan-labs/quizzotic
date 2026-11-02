// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// ---- approvals queue ----

type opEffectRow struct {
	Name, Slug, Status string
	Before, After      int
	Created            bool
}

type opCard struct {
	ID, Kind, Summary, By, Created string
	Status                         string
	Highlight                      bool
	Effects                        []opEffectRow
	Affected                       int
	HasPreview                     bool
	PreviewError                   string
	Decided                        string
	Reverted                       string
}

type opsData struct {
	Pending, Applied []opCard
	Notice, Error    string
}

func actorLabel(a domain.Actor) string {
	if a.Kind == domain.ActorHuman {
		return "you"
	}
	if a.ClientID != "" {
		return string(a.Kind) + " " + a.ClientID
	}
	return string(a.Kind)
}

// tagNames maps every tag ID (active, merged and retired) to "Name (slug)".
func (w *Web) tagNames(ctx context.Context, owner domain.ID) (map[string]string, error) {
	names := map[string]string{}
	for _, st := range []tags.Status{tags.StatusActive, tags.StatusMerged, tags.StatusRetired} {
		ts, err := w.allTagsByStatus(ctx, owner, st)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			names[t.ID.String()] = t.Name
		}
	}
	return names, nil
}

func (w *Web) allOps(ctx context.Context, owner domain.ID, st tags.OpStatus) ([]tags.Operation, error) {
	var out []tags.Operation
	cursor := ""
	for {
		ops, next, err := w.tagops.ListOperations(ctx, owner, st, platform.Page{Cursor: cursor, Limit: platform.MaxLimit})
		if err != nil {
			return nil, err
		}
		out = append(out, ops...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// summarize words what an operation does, from its normalized payload.
func summarize(op tags.Operation, names map[string]string) string {
	name := func(ref string) string {
		if n, ok := names[ref]; ok {
			return n
		}
		return ref
	}
	list := func(refs []string) string {
		out := make([]string, len(refs))
		for i, r := range refs {
			out[i] = name(r)
		}
		return strings.Join(out, ", ")
	}
	switch op.Kind {
	case tags.OpRename:
		var in tags.RenameInput
		if json.Unmarshal(op.Payload, &in) == nil {
			var to []string
			if in.Name != nil {
				to = append(to, "name "+*in.Name)
			}
			if in.Slug != nil {
				to = append(to, "slug "+*in.Slug)
			}
			return fmt.Sprintf("Rename %s: %s", name(in.Tag), strings.Join(to, ", "))
		}
	case tags.OpMerge:
		var in tags.MergeInput
		if json.Unmarshal(op.Payload, &in) == nil {
			return fmt.Sprintf("Merge %s into %s", list(in.Sources), name(in.Target))
		}
	case tags.OpSplit:
		var in tags.SplitInput
		if json.Unmarshal(op.Payload, &in) == nil {
			var tg []string
			for _, t := range in.Targets {
				switch {
				case t.New != nil:
					tg = append(tg, t.New.Name+" (new)")
				default:
					tg = append(tg, name(t.Ref))
				}
			}
			s := fmt.Sprintf("Split %s into %s; %d problems assigned", name(in.Source), strings.Join(tg, ", "), len(in.Assignments))
			if in.RetireSource {
				s += "; source retired"
			}
			return s
		}
	}
	return string(op.Kind) + " operation"
}

func (w *Web) opCard(op tags.Operation, names map[string]string) opCard {
	c := opCard{ID: op.ID.String(), Kind: string(op.Kind), Summary: summarize(op, names), Status: string(op.Status),
		By: actorLabel(op.ProposedBy), Created: op.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")}
	if !op.AppliedAt.IsZero() {
		c.Decided = op.AppliedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	if !op.RevertedAt.IsZero() {
		c.Reverted = op.RevertedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	return c
}

func (w *Web) opsView(ctx context.Context, owner domain.ID, highlight string) (opsData, error) {
	names, err := w.tagNames(ctx, owner)
	if err != nil {
		return opsData{}, err
	}
	var d opsData
	proposed, err := w.allOps(ctx, owner, tags.OpProposed)
	if err != nil {
		return d, err
	}
	for _, op := range proposed {
		c := w.opCard(op, names)
		c.Highlight = c.ID == highlight
		det, err := w.tagops.GetOperation(ctx, owner, op.ID)
		if err != nil {
			return d, err
		}
		c.PreviewError = det.PreviewError
		if p := det.Preview; p != nil {
			c.HasPreview, c.Affected = true, len(p.AffectedProblems)
			for _, t := range p.Tags {
				c.Effects = append(c.Effects, opEffectRow{Name: t.Name, Slug: t.Slug, Status: string(t.Status),
					Before: t.ProblemsBefore, After: t.ProblemsAfter, Created: t.Created})
			}
		}
		d.Pending = append(d.Pending, c)
	}
	applied, err := w.allOps(ctx, owner, tags.OpApplied)
	if err != nil {
		return d, err
	}
	slices.Reverse(applied) // newest first
	for _, op := range applied {
		d.Applied = append(d.Applied, w.opCard(op, names))
	}
	return d, nil
}

func (w *Web) renderOps(rw http.ResponseWriter, r *http.Request, status int, owner domain.ID, highlight, notice, errMsg string) {
	d, err := w.opsView(r.Context(), owner, highlight)
	if err != nil {
		w.fail(rw, err)
		return
	}
	d.Notice, d.Error = notice, errMsg
	w.renderStatus(rw, r, status, "tag_ops", page{Title: "Tag operations", Data: d})
}

func (w *Web) opsPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	q := r.URL.Query()
	notice := ""
	switch {
	case q.Get("proposed") != "":
		notice = "Operation proposed. Review its preview below, then apply or reject it."
	case q.Get("done") != "":
		notice = map[string]string{"apply": "Operation applied.", "reject": "Operation rejected.", "revert": "Operation reverted."}[q.Get("done")]
	}
	w.renderOps(rw, r, http.StatusOK, ws.UserID, q.Get("proposed"), notice, "")
}

// opDecide handles Apply, Reject and Revert of one operation.
func (w *Web) opDecide(verb string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		actor, ok := w.tagOpsPost(rw, r)
		if !ok {
			return
		}
		id, err := domain.ParseID(r.PathValue("id"))
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		switch verb {
		case "apply":
			_, err = w.tagops.ApplyOperation(r.Context(), actor.UserID, actor, id)
		case "reject":
			_, err = w.tagops.RejectOperation(r.Context(), actor.UserID, actor, id)
		default:
			_, err = w.tagops.RevertOperation(r.Context(), actor.UserID, actor, id)
		}
		if err != nil {
			st, msg, ok := tagOpError(err)
			if !ok {
				w.fail(rw, err)
				return
			}
			w.renderOps(rw, r, st, actor.UserID, "", "", fmt.Sprintf("Could not %s: %s", verb, msg))
			return
		}
		http.Redirect(rw, r, "/tag-operations?done="+url.QueryEscape(verb), http.StatusSeeOther)
	}
}
