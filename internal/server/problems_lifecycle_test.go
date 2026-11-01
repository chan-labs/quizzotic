// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

func init() {
	// Each lifecycle op is its own resource: Create is called once per op and
	// publish needs a draft, archive a live problem.
	lifecycle := func(name, pattern, status string, want int) ownerscope.Resource {
		return ownerscope.Resource{
			Name: name,
			Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
				prompt := "Lifecycle secret prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
				body := openBody(prompt)
				body["status"] = status
				r := c.Do("POST", "/problems", body)
				if r.Code != http.StatusCreated {
					t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
				}
				var v problems.View
				if err := json.Unmarshal(r.Body, &v); err != nil {
					t.Fatal(err)
				}
				return ownerscope.Created{
					Params:  ownerscope.Params{"id": v.ID.String()},
					Secrets: []string{prompt, "secret reference answer"},
					Header:  http.Header{"If-Match": {r.Header.Get("ETag")}},
				}
			},
			Ops: []ownerscope.Op{{Pattern: pattern, WantOwner: want}},
			Intact: func(t testing.TB, c *ownerscope.Client, created ownerscope.Created) {
				r := c.Do("GET", "/problems/"+created.Params["id"], nil)
				var v problems.View
				if err := json.Unmarshal(r.Body, &v); err != nil || r.Code != http.StatusOK || string(v.Status) != status {
					t.Errorf("problem changed by another user: %d %s", r.Code, r.Body)
				}
			},
		}
	}
	ownerScope.Resource(lifecycle("problem publish", "POST /problems/{id}/publish", "draft", http.StatusOK))
	ownerScope.Resource(lifecycle("problem archive", "POST /problems/{id}/archive", "live", http.StatusOK))
	ownerScope.Resource(lifecycle("problem delete", "DELETE /problems/{id}", "draft", http.StatusNoContent))
}

func TestProblemLifecycleHTTP(t *testing.T) {
	e := newProbEnv(t)
	v := e.create(t, e.human, openBody("Lifecycle?"))
	path := "/problems/" + v.ID.String()
	hdr := func(etag string) map[string]string { return map[string]string{"If-Match": etag} }

	if r := e.req(t, "POST", path+"/publish", e.human, nil, nil); r.code != http.StatusPreconditionRequired {
		t.Errorf("publish without If-Match = %d", r.code)
	}
	if r := e.req(t, "POST", path+"/publish", e.human, nil, hdr(`"stale"`)); r.code != http.StatusPreconditionFailed {
		t.Errorf("publish stale = %d", r.code)
	}
	if r := e.req(t, "POST", path+"/archive", e.human, nil, hdr("*")); r.code != http.StatusConflict {
		t.Errorf("archive draft = %d, want 409", r.code)
	}
	r := e.req(t, "POST", path+"/publish", e.human, nil, hdr(v.ETag()))
	if r.code != http.StatusOK {
		t.Fatalf("publish = %d (%s)", r.code, r.body)
	}
	live := decodeReply[problems.View](t, r)
	if live.Status != problems.StatusLive || r.header.Get("ETag") != live.ETag() || live.ETag() == v.ETag() {
		t.Errorf("published view %+v etag %q", live, r.header.Get("ETag"))
	}
	if r := e.req(t, "POST", path+"/publish", e.human, nil, hdr(live.ETag())); r.code != http.StatusConflict {
		t.Errorf("publish live = %d, want 409", r.code)
	}
	if r := e.req(t, "POST", path+"/archive", e.human, nil, hdr(live.ETag())); r.code != http.StatusOK {
		t.Errorf("archive = %d (%s)", r.code, r.body)
	}
	if r := e.req(t, "DELETE", path, e.human, nil, nil); r.code != http.StatusPreconditionRequired {
		t.Errorf("delete without If-Match = %d", r.code)
	}
	if r := e.req(t, "DELETE", path, e.human, nil, hdr("*")); r.code != http.StatusNoContent {
		t.Errorf("delete = %d", r.code)
	}
	if r := e.req(t, "GET", path, e.human, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("get after delete = %d", r.code)
	}
	if r := e.req(t, "DELETE", path, e.human, nil, hdr("*")); r.code != http.StatusNotFound {
		t.Errorf("second delete = %d", r.code)
	}
}
