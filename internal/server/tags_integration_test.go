// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

type tagJSON struct {
	ID         string   `json:"id"`
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	MergedInto *string  `json:"merged_into"`
	Aliases    []string `json:"aliases"`
}

type tagPageJSON struct {
	Items      []tagJSON `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

// ownerScopeIT is the environment TestOwnerScope runs against, for creators
// that must seed data directly because no endpoint creates it yet.
var ownerScopeIT *itEnv

var tagSeq atomic.Int64

func uniqueSlug(prefix string) string { return fmt.Sprintf("%s-%d", prefix, tagSeq.Add(1)) }

// seedProblem stores a live problem owned by the test user with this subject.
func seedProblem(t testing.TB, e *itEnv, ownerSub, prompt string) domain.ID {
	t.Helper()
	uid, _ := e.userFor(t, ownerSub)
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatalf("user %q not provisioned: %v", ownerSub, err)
	}
	ids := domain.NewIDGenerator(domain.SystemClock{})
	act := domain.Actor{Kind: domain.ActorHuman, UserID: owner}
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := ids.New()
	p := problems.Problem{ID: id, OwnerID: owner, Status: problems.StatusLive, CurrentVersion: 1,
		CreatedBy: act, CreatedAt: now, UpdatedAt: now}
	v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: prompt,
		Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: now}
	if err := e.st.Problems().Create(context.Background(), owner, p, v); err != nil {
		t.Fatal(err)
	}
	return id
}

func init() {
	createTag := func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
		slug := uniqueSlug("secret-topic")
		r := c.Do("POST", "/tags", map[string]string{"slug": slug, "name": "Secret Topic Name"})
		if r.Code != http.StatusCreated {
			t.Fatalf("create tag = %d (%s)", r.Code, r.Body)
		}
		var tg tagJSON
		if err := json.Unmarshal(r.Body, &tg); err != nil {
			t.Fatal(err)
		}
		return ownerscope.Created{
			Params: ownerscope.Params{"tag": tg.ID},
			// The ID is in the request path, so the problem body echoes it; the
			// slug and name are what must not leak.
			Secrets: []string{slug, "Secret Topic Name"},
			Header:  http.Header{"If-Match": {r.Header.Get("ETag")}},
		}
	}
	ownerScope.Resource(ownerscope.Resource{
		Name:   "tags",
		Create: createTag,
		Ops: []ownerscope.Op{
			{Pattern: "GET /tags/{tag}"},
			{Pattern: "PATCH /tags/{tag}", Body: func(ownerscope.Created) any { return map[string]string{"name": "Changed"} }},
			{Pattern: "GET /tags/{tag}/problems"},
			{Pattern: "GET /tags", List: true},
			{Pattern: "POST /tags", List: true, WantOwner: http.StatusCreated, WantOther: http.StatusCreated,
				Body: func(ownerscope.Created) any {
					return map[string]string{"slug": uniqueSlug("posted"), "name": "Posted"}
				}},
		},
		Intact: func(t testing.TB, c *ownerscope.Client, created ownerscope.Created) {
			r := c.Do("GET", "/tags/"+created.Params["tag"], nil)
			var tg tagJSON
			if err := json.Unmarshal(r.Body, &tg); err != nil || tg.Name != "Secret Topic Name" {
				t.Errorf("tag changed by another user: %d %s", r.Code, r.Body)
			}
		},
	})

	ownerScope.Resource(ownerscope.Resource{
		Name: "problem tags",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			tg := createTag(t, c)
			slug := tg.Secrets[0]
			pid := seedProblem(t, ownerScopeIT, "owner-a-sub", "Secret prompt text")
			return ownerscope.Created{
				Params:  ownerscope.Params{"id": pid.String()},
				Secrets: []string{slug, "Secret prompt text"},
			}
		},
		Ops: []ownerscope.Op{{
			Pattern: "PUT /problems/{id}/tags",
			Body:    func(c ownerscope.Created) any { return map[string]any{"tags": []string{c.Secrets[0]}} },
		}},
	})
}

type tagsEnv struct {
	e    *itEnv
	a, b *ownerscope.Client
}

func newTagsEnv(t *testing.T) tagsEnv {
	t.Helper()
	e := newITEnv(t)
	env := e.ownerScopeEnv()
	te := tagsEnv{e: e,
		a: &ownerscope.Client{T: t, Env: env, User: ownerscope.UserA},
		b: &ownerscope.Client{T: t, Env: env, User: ownerscope.UserB}}
	// First requests provision both users.
	te.a.Do("GET", "/me", nil)
	te.b.Do("GET", "/me", nil)
	return te
}

func decode[T any](t *testing.T, r ownerscope.Response, wantCode int) T {
	t.Helper()
	if r.Code != wantCode {
		t.Fatalf("status = %d, want %d (%s)", r.Code, wantCode, r.Body)
	}
	var v T
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("body %q: %v", r.Body, err)
	}
	return v
}

func wantProblem(t *testing.T, r ownerscope.Response, code int, typeSuffix string) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("status = %d, want %d (%s)", r.Code, code, r.Body)
	}
	var p struct {
		Type   string `json:"type"`
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatal(err)
	}
	if want := "urn:quizzotic:problem:" + typeSuffix; p.Type != want {
		t.Fatalf("problem type = %q, want %q", p.Type, want)
	}
}

func (te tagsEnv) create(t *testing.T, c *ownerscope.Client, slug, name string) tagJSON {
	t.Helper()
	body := map[string]string{"name": name}
	if slug != "" {
		body["slug"] = slug
	}
	return decode[tagJSON](t, c.Do("POST", "/tags", body), http.StatusCreated)
}

func (te tagsEnv) get(t *testing.T, c *ownerscope.Client, ref string) (tagJSON, string) {
	t.Helper()
	r := c.Do("GET", "/tags/"+ref, nil)
	return decode[tagJSON](t, r, http.StatusOK), r.Header.Get("ETag")
}

func (te tagsEnv) rename(t *testing.T, c *ownerscope.Client, ref, slug string) ownerscope.Response {
	t.Helper()
	_, etag := te.get(t, c, ref)
	return c.DoWith("PATCH", "/tags/"+ref, map[string]string{"slug": slug}, http.Header{"If-Match": {etag}})
}

func TestTagsResolveByIDSlugAndAlias(t *testing.T) {
	te := newTagsEnv(t)
	created := te.create(t, te.a, "golang", "Go")
	if created.Status != "active" || created.MergedInto != nil || len(created.Aliases) != 0 {
		t.Fatalf("created = %+v", created)
	}

	// Rename: the old slug becomes an alias.
	renamed := decode[tagJSON](t, te.rename(t, te.a, "golang", "go"), http.StatusOK)
	if renamed.ID != created.ID || renamed.Slug != "go" || !slices.Equal(renamed.Aliases, []string{"golang"}) {
		t.Fatalf("renamed = %+v", renamed)
	}

	for _, ref := range []string{created.ID, "go", "golang", "GOLANG"} {
		got, _ := te.get(t, te.a, ref)
		if got.ID != created.ID || got.Slug != "go" {
			t.Errorf("GET /tags/%s = %+v, want tag %s with slug go", ref, got, created.ID)
		}
	}
	if r := te.a.Do("GET", "/tags/rust", nil); r.Code != http.StatusNotFound {
		t.Errorf("unknown ref = %d, want 404", r.Code)
	}

	// Every {tag} position accepts ID, slug and alias.
	pid := seedProblem(t, te.e, "owner-a-sub", "What is a goroutine?")
	for _, ref := range []string{created.ID, "go", "golang"} {
		decode[struct {
			Items []tagJSON `json:"items"`
		}](t, te.a.Do("PUT", "/problems/"+pid.String()+"/tags", map[string]any{"tags": []string{}}), http.StatusOK)
		r := te.a.Do("PUT", "/problems/"+pid.String()+"/tags", map[string]any{"tags": []string{ref}})
		set := decode[struct {
			Items []tagJSON `json:"items"`
		}](t, r, http.StatusOK)
		if len(set.Items) != 1 || set.Items[0].ID != created.ID {
			t.Errorf("PUT tags [%s] = %+v", ref, set.Items)
		}
		list := decode[struct {
			Items []struct {
				ID     string `json:"id"`
				Prompt string `json:"prompt"`
			} `json:"items"`
		}](t, te.a.Do("GET", "/tags/"+ref+"/problems", nil), http.StatusOK)
		if len(list.Items) != 1 || list.Items[0].ID != pid.String() || list.Items[0].Prompt != "What is a goroutine?" {
			t.Errorf("GET /tags/%s/problems = %+v", ref, list.Items)
		}
	}

	// PATCH accepts any reference too.
	_, etag := te.get(t, te.a, "golang")
	r := te.a.DoWith("PATCH", "/tags/golang", map[string]string{"name": "Go language"}, http.Header{"If-Match": {etag}})
	if got := decode[tagJSON](t, r, http.StatusOK); got.Name != "Go language" || got.Slug != "go" {
		t.Errorf("patch by alias = %+v", got)
	}

	// Renaming back to the alias takes it over; "go" becomes the alias.
	back := decode[tagJSON](t, te.rename(t, te.a, "go", "golang"), http.StatusOK)
	if back.Slug != "golang" || !slices.Equal(back.Aliases, []string{"go"}) {
		t.Errorf("rename back = %+v", back)
	}
}

func TestTagsEnforceSlugUniquenessPerOwner(t *testing.T) {
	te := newTagsEnv(t)
	te.create(t, te.a, "go", "Go")

	dup := te.a.Do("POST", "/tags", map[string]string{"slug": "go", "name": "Another Go"})
	wantProblem(t, dup, http.StatusConflict, "conflict")

	// Another owner may use the same slug; the two tags are distinct.
	bTag := te.create(t, te.b, "go", "Go for B")
	aTag, _ := te.get(t, te.a, "go")
	if aTag.ID == bTag.ID || aTag.Name != "Go" {
		t.Fatalf("a=%+v b=%+v", aTag, bTag)
	}

	// An alias holds its slug: nobody of that owner may take it.
	decode[tagJSON](t, te.rename(t, te.a, "go", "golang"), http.StatusOK)
	wantProblem(t, te.a.Do("POST", "/tags", map[string]string{"slug": "go", "name": "Takes the alias"}),
		http.StatusConflict, "conflict")
	other := te.create(t, te.a, "rust", "Rust")
	wantProblem(t, te.rename(t, te.a, other.ID, "go"), http.StatusConflict, "conflict")
	wantProblem(t, te.rename(t, te.a, other.ID, "golang"), http.StatusConflict, "conflict")
	if got, _ := te.get(t, te.a, "rust"); got.ID != other.ID || len(got.Aliases) != 0 {
		t.Errorf("failed rename changed the tag: %+v", got)
	}

	// B's own alias space is independent: B's "go" tag is untouched.
	if got, _ := te.get(t, te.b, "go"); got.ID != bTag.ID || len(got.Aliases) != 0 {
		t.Errorf("b's tag = %+v", got)
	}
	if r := te.b.Do("GET", "/tags/golang", nil); r.Code != http.StatusNotFound {
		t.Errorf("b resolves a's alias: %d", r.Code)
	}

	// Each owner lists only their own tags.
	for c, want := range map[*ownerscope.Client][]string{te.a: {"golang", "rust"}, te.b: {"go"}} {
		page := decode[tagPageJSON](t, c.Do("GET", "/tags", nil), http.StatusOK)
		var got []string
		for _, it := range page.Items {
			got = append(got, it.Slug)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("list = %v, want %v", got, want)
		}
	}
}

func TestTagsValidationAndPreconditions(t *testing.T) {
	te := newTagsEnv(t)

	derived := te.create(t, te.a, "", "Distributed Systems (Basics)!")
	if derived.Slug != "distributed-systems-basics" {
		t.Errorf("derived slug = %q", derived.Slug)
	}
	for name, body := range map[string]map[string]string{
		"bad slug":    {"slug": "Not A Slug", "name": "x"},
		"empty name":  {"name": " "},
		"id as slug":  {"slug": "0190a000-0000-7000-8000-000000000000", "name": "x"},
		"no slug src": {"name": "!!!"},
	} {
		if r := te.a.Do("POST", "/tags", body); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d (%s)", name, r.Code, r.Body)
		}
	}

	_, etag := te.get(t, te.a, derived.ID)
	wantProblem(t, te.a.Do("PATCH", "/tags/"+derived.ID, map[string]string{"name": "n"}), http.StatusPreconditionRequired, "precondition-required")
	wantProblem(t, te.a.DoWith("PATCH", "/tags/"+derived.ID, map[string]string{"name": "n"}, http.Header{"If-Match": {`"stale"`}}),
		http.StatusPreconditionFailed, "precondition-failed")
	ok := te.a.DoWith("PATCH", "/tags/"+derived.ID, map[string]string{"name": "n"}, http.Header{"If-Match": {etag}})
	decode[tagJSON](t, ok, http.StatusOK)
	// The old ETag is stale after a change.
	wantProblem(t, te.a.DoWith("PATCH", "/tags/"+derived.ID, map[string]string{"name": "m"}, http.Header{"If-Match": {etag}}),
		http.StatusPreconditionFailed, "precondition-failed")

	// Pagination.
	for _, s := range []string{"p1", "p2", "p3"} {
		te.create(t, te.a, s, s)
	}
	var seen int
	cursor := ""
	for {
		path := "/tags?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := decode[tagPageJSON](t, te.a.Do("GET", path, nil), http.StatusOK)
		seen += len(page.Items)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if seen != 4 {
		t.Errorf("paged through %d tags, want 4", seen)
	}
	wantProblem(t, te.a.Do("GET", "/tags?cursor=junk", nil), http.StatusBadRequest, "bad-request")
	if r := te.a.Do("GET", "/tags?status=bogus", nil); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("bogus status = %d", r.Code)
	}
}

func TestSetProblemTagsRules(t *testing.T) {
	te := newTagsEnv(t)
	x := te.create(t, te.a, "x", "X")
	y := te.create(t, te.a, "y", "Y")
	bTag := te.create(t, te.b, "bee", "Bee")
	pid := seedProblem(t, te.e, "owner-a-sub", "p")
	path := "/problems/" + pid.String() + "/tags"
	type items struct {
		Items []tagJSON `json:"items"`
	}

	// Duplicates collapse; result is ordered by slug.
	got := decode[items](t, te.a.Do("PUT", path, map[string]any{"tags": []string{"y", y.ID, "x"}}), http.StatusOK)
	if len(got.Items) != 2 || got.Items[0].ID != x.ID || got.Items[1].ID != y.ID {
		t.Fatalf("items = %+v", got.Items)
	}

	// An unknown or foreign tag is a 422 naming the entry, and changes nothing.
	r := te.a.Do("PUT", path, map[string]any{"tags": []string{"x", "nope", bTag.ID}})
	wantProblem(t, r, http.StatusUnprocessableEntity, "validation")
	var p struct {
		Errors []struct{ Field string } `json:"errors"`
	}
	_ = json.Unmarshal(r.Body, &p)
	if len(p.Errors) != 2 || p.Errors[0].Field != "tags[1]" || p.Errors[1].Field != "tags[2]" {
		t.Errorf("errors = %+v", p.Errors)
	}
	got = decode[items](t, te.a.Do("PUT", path, map[string]any{"tags": []string{"x", "y"}}), http.StatusOK)
	if len(got.Items) != 2 {
		t.Errorf("tags changed by failed request: %+v", got.Items)
	}

	// B cannot tag A's problem, and A's tag is unknown to B.
	if r := te.b.Do("PUT", path, map[string]any{"tags": []string{"bee"}}); r.Code != http.StatusNotFound {
		t.Errorf("b tags a's problem = %d, want 404", r.Code)
	}
	bp := seedProblem(t, te.e, "owner-b-sub", "bp")
	r = te.b.Do("PUT", "/problems/"+bp.String()+"/tags", map[string]any{"tags": []string{"x"}})
	wantProblem(t, r, http.StatusUnprocessableEntity, "validation")

	// Missing and malformed bodies, unknown and non-ID problems.
	if r := te.a.Do("PUT", path, map[string]any{}); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing tags = %d", r.Code)
	}
	if r := te.a.Do("PUT", "/problems/"+domain.NewIDGenerator(domain.SystemClock{}).New().String()+"/tags", map[string]any{"tags": []string{}}); r.Code != http.StatusNotFound {
		t.Errorf("unknown problem = %d", r.Code)
	}
	if r := te.a.Do("PUT", "/problems/not-an-id/tags", map[string]any{"tags": []string{}}); r.Code != http.StatusNotFound {
		t.Errorf("malformed problem id = %d", r.Code)
	}

	// A soft-deleted problem drops out of the tag's problem list.
	uid, _ := te.e.userFor(t, "owner-a-sub")
	owner, _ := domain.ParseID(uid)
	if err := te.e.st.Problems().SoftDelete(context.Background(), owner, pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	list := decode[struct {
		Items []any `json:"items"`
	}](t, te.a.Do("GET", "/tags/x/problems", nil), http.StatusOK)
	if len(list.Items) != 0 {
		t.Errorf("deleted problem listed: %+v", list.Items)
	}
}

func TestTagScopes(t *testing.T) {
	e := newITEnv(t)
	// quizzotic-agent is allowed problems:read and problems:write only.
	tok := e.idp.access("quizzotic-agent", "agent-sub")
	if r := e.do(t, "GET", "/api/v1/tags", bearer(tok), ""); r.code != http.StatusForbidden {
		t.Errorf("GET /tags without tags:read = %d, want 403", r.code)
	}
	if r := e.do(t, "POST", "/api/v1/tags", bearer(tok), ""); r.code != http.StatusForbidden {
		t.Errorf("POST /tags without tags:write = %d, want 403", r.code)
	}
	if r := e.do(t, "GET", "/api/v1/tags", nil, ""); r.code != http.StatusUnauthorized {
		t.Errorf("GET /tags anonymous = %d, want 401", r.code)
	}
}
