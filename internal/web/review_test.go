// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/web"
)

func (e todayEnv) create(t *testing.T, typ, prompt, payload string) problems.View {
	t.Helper()
	v, err := e.problems.Create(context.Background(), domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "c"}, problems.CreateInput{
		Type: typ, Prompt: prompt, Payload: []byte(payload), Status: problems.StatusLive, Explanation: "Because *reasons*."})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e todayEnv) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// keyboard replays what review.js does for a key press against the page the
// browser is showing: it finds the element whose data-key matches and
// activates it (check a choice, submit the form, follow the link). Typing is
// done with Type before the key that submits.
type keyboard struct {
	t      *testing.T
	e      todayEnv
	path   string
	body   string
	fields url.Values // what the browser would submit for the shown form
	// latencyMS is what the script would write into latency_ms on submit.
	latencyMS string
}

func (e todayEnv) open(t *testing.T, path string) *keyboard {
	t.Helper()
	k := &keyboard{t: t, e: e, latencyMS: "2500"}
	k.load(e.do("GET", path, nil), path)
	return k
}

var (
	hiddenRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)
	tagRe    = regexp.MustCompile(`<(input|button|a)\b[^>]*\bdata-key="([^"]+)"[^>]*>`)
	attrRe   = regexp.MustCompile(`([a-z-]+)="([^"]*)"`)
	actionRe = regexp.MustCompile(`<form method="post" action="(/review/[^"]+)"`)
)

func (k *keyboard) load(rec *httptest.ResponseRecorder, path string) {
	k.t.Helper()
	if rec.Code != http.StatusOK {
		k.t.Fatalf("GET %s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	k.path, k.body = path, rec.Body.String()
	k.fields = url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(k.body, -1) {
		k.fields.Set(m[1], html.UnescapeString(m[2]))
	}
}

// Type fills a named text field, as typing in it would.
func (k *keyboard) Type(name, text string) { k.fields.Set(name, text) }

// Has reports whether some element answers to key.
func (k *keyboard) Has(key string) bool {
	for _, m := range tagRe.FindAllStringSubmatch(k.body, -1) {
		if m[2] == key {
			return true
		}
	}
	return false
}

func (k *keyboard) Press(key string) {
	k.t.Helper()
	for _, m := range tagRe.FindAllStringSubmatch(k.body, -1) {
		if m[2] != key {
			continue
		}
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[0], -1) {
			attrs[a[1]] = html.UnescapeString(a[2])
		}
		switch {
		case m[1] == "a":
			k.load(k.e.do("GET", attrs["href"], nil), attrs["href"])
		case m[1] == "input": // a choice: click toggles a checkbox, selects a radio
			if attrs["type"] == "checkbox" {
				if has(k.fields["selected"], attrs["value"]) {
					k.fields["selected"] = without(k.fields["selected"], attrs["value"])
				} else {
					k.fields.Add("selected", attrs["value"])
				}
			} else {
				k.fields.Set("selected", attrs["value"])
			}
		default: // a submit button
			k.submit(attrs)
		}
		return
	}
	k.t.Fatalf("no element answers to key %q on:\n%s", key, k.body)
}

func (k *keyboard) submit(button map[string]string) {
	k.t.Helper()
	action := actionRe.FindStringSubmatch(k.body)
	if action == nil {
		k.t.Fatalf("no form to submit:\n%s", k.body)
	}
	form := url.Values{}
	for n, v := range k.fields {
		form[n] = v
	}
	if form.Has("latency_ms") {
		form.Set("latency_ms", k.latencyMS)
	}
	if button["name"] != "" {
		form.Set(button["name"], button["value"])
	}
	rec := k.e.do("POST", action[1], form)
	if rec.Code == http.StatusSeeOther {
		loc := rec.Header().Get("Location")
		k.load(k.e.do("GET", loc, nil), loc)
		return
	}
	k.load(rec, action[1])
}

func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func without(s []string, v string) []string {
	var out []string
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func sessionPath(t *testing.T, e todayEnv) string {
	t.Helper()
	rec := e.post(testCSRF)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d", rec.Code)
	}
	return rec.Header().Get("Location")
}

const (
	mcqPayload   = `{"choices":[{"id":"a","text":"Wrong one"},{"id":"b","text":"Right one"},{"id":"c","text":"Also wrong"}],"correct":["b"]}`
	multiPayload = `{"choices":[{"id":"a","text":"Yes 1"},{"id":"b","text":"No"},{"id":"c","text":"Yes 2"}],"correct":["a","c"],"multiple":true}`
	clozePayload = `{"blanks":[{"id":"city","accepted":["Paris"]},{"id":"river","accepted":["Seine"]}]}`
	openPayload  = `{"reference_answer":"The **reference** answer.","rubric":["point one"]}`
)

// The Done-when: a mixed-type session is finished using only key presses (and
// typing into cloze blanks). Each press is replayed through the same markup
// the script reads, then the form it would submit is posted.
func TestReviewSessionFinishedWithKeysOnly(t *testing.T) {
	e := newTodayEnv(t)
	e.create(t, problems.TypeMCQ, "Pick the right one", mcqPayload)
	e.create(t, problems.TypeMCQ, "Pick both yeses", multiPayload)
	e.create(t, problems.TypeCloze, "{{city}} sits on the {{river}}.", clozePayload)
	e.create(t, problems.TypeOpen, "Explain it", openPayload)

	path := sessionPath(t, e)
	k := e.open(t, path)
	seen := map[string]int{}
	for i := 0; i < 10 && !strings.Contains(k.body, "review-done"); i++ {
		switch {
		case strings.Contains(k.body, `data-review-phase="prompt"`):
			typ := regexp.MustCompile(`data-type="(\w+)"`).FindStringSubmatch(k.body)[1]
			seen[typ]++
			switch typ {
			case "mcq":
				if strings.Contains(k.body, "Pick both yeses") {
					k.Press("1")
					k.Press("3")
				} else {
					k.Press("2")
				}
				k.Press("Enter")
			case "cloze":
				k.Type("blank_city", "paris")
				k.Type("blank_river", "Seine")
				k.Press("Enter")
			case "open":
				if k.Has("Enter") {
					t.Fatal("an open problem must reveal with Space, not submit with Enter")
				}
				k.Press("Space")
			}
		case strings.Contains(k.body, `data-review-phase="result"`):
			switch {
			case strings.Contains(k.body, "Reference answer"):
				if !strings.Contains(k.body, "<strong>reference</strong>") || !strings.Contains(k.body, "point one") {
					t.Errorf("open result lacks the reference:\n%s", k.body)
				}
				if k.Has("Enter") {
					t.Error("an unrated open attempt must not continue on Enter")
				}
				k.Press("4") // Easy
			case strings.Contains(k.body, `class="verdict right"`):
				if !strings.Contains(k.body, "<em>reasons</em>") {
					t.Error("result lacks the explanation")
				}
				k.Press("Enter") // keep the default rating
			default:
				t.Fatalf("unexpected result page:\n%s", k.body)
			}
		default:
			t.Fatalf("unexpected page:\n%s", k.body)
		}
	}
	if !strings.Contains(k.body, "4 answered") {
		t.Fatalf("session did not finish with 4 answers:\n%s", k.body)
	}
	if seen["mcq"] != 2 || seen["cloze"] != 1 || seen["open"] != 1 {
		t.Errorf("prompt types seen = %v", seen)
	}

	// Every answer is an attempt in the session, with the latency the script
	// measured and a rating in force.
	res, err := e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 4 {
		t.Fatalf("attempts = %d, want 4", len(res.Items))
	}
	sid := strings.TrimPrefix(path, "/review/")
	for _, a := range res.Items {
		if a.SessionID == nil || *a.SessionID != sid {
			t.Errorf("attempt %s session = %v, want %s", a.ID, a.SessionID, sid)
		}
		if a.LatencyMS != 2500 {
			t.Errorf("attempt %s latency = %d, want 2500", a.ID, a.LatencyMS)
		}
	}
	// The open attempt was rated Easy (4) by key; the others carry the default.
	for _, a := range res.Items {
		isOpen := a.AutoCorrect == nil
		switch {
		case isOpen && (a.Rating == nil || *a.Rating != 4):
			t.Errorf("open attempt rating = %v, want 4", a.Rating)
		case !isOpen && a.Rating != nil:
			t.Errorf("auto-graded attempt got explicit rating %d", *a.Rating)
		}
	}
	// Finishing completed the session.
	id, _ := domain.ParseID(sid)
	if s, err := e.sessions.Get(context.Background(), e.owner, id); err != nil || s.CompletedAt == nil {
		t.Errorf("session completed = %v, err %v", s.CompletedAt, err)
	}
}

func TestReviewWrongAnswerOffersNoRatingAndRateKeyAdjustsRight(t *testing.T) {
	e := newTodayEnv(t)
	e.create(t, problems.TypeMCQ, "Only one", mcqPayload)
	k := e.open(t, sessionPath(t, e))
	k.Press("1") // wrong
	k.Press("Enter")
	if !strings.Contains(k.body, `class="verdict wrong"`) {
		t.Fatalf("not marked wrong:\n%s", k.body)
	}
	for _, key := range []string{"1", "2", "3", "4"} {
		if k.Has(key) {
			t.Errorf("wrong answer offers rating key %s", key)
		}
	}
	if !k.Has("Enter") {
		t.Error("cannot continue by keyboard")
	}

	e2 := newTodayEnv(t)
	e2.create(t, problems.TypeMCQ, "Only one", mcqPayload)
	k = e2.open(t, sessionPath(t, e2))
	k.Press("2")
	k.Press("Enter")
	if k.Has("1") || !k.Has("2") || !k.Has("3") || !k.Has("4") {
		t.Errorf("right answer should offer exactly keys 2-4:\n%s", k.body)
	}
	k.Press("2") // Hard
	res, _ := e2.answers.List(context.Background(), e2.owner, attempts.ListQuery{Limit: 5})
	if len(res.Items) != 1 || res.Items[0].Rating == nil || *res.Items[0].Rating != 2 {
		t.Errorf("attempts = %+v, want one rated 2", res.Items)
	}
}

func TestReviewPromptMarkupExposesKeysAndLatency(t *testing.T) {
	e := newTodayEnv(t)
	e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	body := e.do("GET", sessionPath(t, e), nil).Body.String()
	for _, want := range []string{
		`data-latency`, `name="latency_ms"`, `src="/static/review.js"`,
		`data-key="1"`, `data-key="2"`, `data-key="3"`, `data-key="Enter"`,
		`name="csrf_token" value="` + testCSRF + `"`, `action="/review/`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt page missing %q", want)
		}
	}
	// CSP is script-src 'self': no inline script or handler may be needed.
	if regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]`).MatchString(body) || regexp.MustCompile(`\son[a-z]+=`).MatchString(body) {
		t.Errorf("page has inline script:\n%s", body)
	}
	// Correct answers never reach the prompt.
	if strings.Contains(body, `"correct"`) || strings.Contains(body, "reasons") {
		t.Error("prompt page leaks the answer")
	}

	script := e.do("GET", "/static/review.js", nil)
	if script.Code != http.StatusOK {
		t.Fatalf("script status = %d", script.Code)
	}
	for _, want := range []string{"data-key", "latency_ms", "performance.now", `"Space"`, `"Enter"`} {
		if !strings.Contains(script.Body.String(), want) {
			t.Errorf("review.js missing %q", want)
		}
	}
}

func TestReviewOpenProblemUsesSpaceToReveal(t *testing.T) {
	e := newTodayEnv(t)
	e.create(t, problems.TypeOpen, "Explain", openPayload)
	k := e.open(t, sessionPath(t, e))
	if !k.Has("Space") || strings.Contains(k.body, "reference") {
		t.Errorf("open prompt should offer Space and hide the reference:\n%s", k.body)
	}
	k.Press("Space")
	if !strings.Contains(k.body, "Reference answer") {
		t.Fatalf("Space did not reveal the reference:\n%s", k.body)
	}
	for _, key := range []string{"1", "2", "3", "4"} {
		if !k.Has(key) {
			t.Errorf("open result lacks rating key %s", key)
		}
	}
	// Until it is rated the card is unscheduled.
	res, _ := e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 5})
	if len(res.Items) != 1 || res.Items[0].Rating != nil {
		t.Errorf("attempt before rating = %+v", res.Items)
	}
	k.Press("1")
	res, _ = e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 5})
	if res.Items[0].Rating == nil || *res.Items[0].Rating != 1 {
		t.Errorf("rating after key 1 = %v", res.Items[0].Rating)
	}
}

func TestReviewWithoutScriptSubmitsPlainForm(t *testing.T) {
	// No key presses, no latency: the plain form still works and latency is 0.
	e := newTodayEnv(t)
	p := e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	path := sessionPath(t, e)
	rec := e.do("POST", path+"/answer", url.Values{"csrf_token": {testCSRF}, "problem_id": {p.ID.String()},
		"problem_version": {"1"}, "selected": {"b"}})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, path+"/result/") {
		t.Fatalf("status %d, Location %q", rec.Code, loc)
	}
	if body := e.do("GET", loc, nil).Body.String(); !strings.Contains(body, `class="verdict right"`) {
		t.Fatalf("result page:\n%s", body)
	}
	res, _ := e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 5})
	if len(res.Items) != 1 || res.Items[0].LatencyMS != 0 {
		t.Errorf("attempts = %+v", res.Items)
	}
}

func (e todayEnv) attemptCount(t *testing.T) int {
	t.Helper()
	res, err := e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Items)
}

// Refreshing or replaying the answer must never record a second attempt.
func TestReviewReplayedAnswerAndRefreshedResultRecordOneAttempt(t *testing.T) {
	for _, typ := range []string{problems.TypeMCQ, problems.TypeCloze, problems.TypeOpen} {
		e := newTodayEnv(t)
		var p problems.View
		form := url.Values{"csrf_token": {testCSRF}, "problem_version": {"1"}}
		switch typ {
		case problems.TypeMCQ:
			p = e.create(t, typ, "Which?", mcqPayload)
			form.Set("selected", "b")
		case problems.TypeCloze:
			p = e.create(t, typ, "{{city}} on the {{river}}.", clozePayload)
			form.Set("blank_city", "Paris")
			form.Set("blank_river", "Lyon")
		default:
			p = e.create(t, typ, "Explain", openPayload)
			form.Set("text", "mine")
		}
		form.Set("problem_id", p.ID.String())
		path := sessionPath(t, e)

		first := e.do("POST", path+"/answer", form)
		loc := first.Header().Get("Location")
		if first.Code != http.StatusSeeOther || !strings.HasPrefix(loc, path+"/result/") {
			t.Fatalf("%s: answer = %d %q", typ, first.Code, loc)
		}
		// Replays (refresh of the POST, double submit) record nothing and move on.
		for range 2 {
			again := e.do("POST", path+"/answer", form)
			if again.Code != http.StatusSeeOther || again.Header().Get("Location") != path {
				t.Errorf("%s: replay = %d %q, want 303 to %s", typ, again.Code, again.Header().Get("Location"), path)
			}
		}
		// Refreshing the result URL re-reads it and writes nothing.
		var bodies []string
		for range 3 {
			rec := e.do("GET", loc, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: result = %d", typ, rec.Code)
			}
			bodies = append(bodies, rec.Body.String())
		}
		if bodies[0] != bodies[2] {
			t.Errorf("%s: result page changed between refreshes", typ)
		}
		if n := e.attemptCount(t); n != 1 {
			t.Errorf("%s: attempts = %d, want exactly 1", typ, n)
		}
		switch typ {
		case problems.TypeCloze:
			if !strings.Contains(bodies[0], `blank-result right">Paris`) || !strings.Contains(bodies[0], `blank-result wrong">Lyon`) {
				t.Errorf("cloze result lost its grading:\n%s", bodies[0])
			}
		case problems.TypeOpen:
			if !strings.Contains(bodies[0], "<strong>reference</strong>") {
				t.Error("open result lost the reference")
			}
		}
	}
}

func TestReviewResultIsScopedToSessionAndOwner(t *testing.T) {
	e := newTodayEnv(t)
	p := e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	path := sessionPath(t, e)
	loc := e.do("POST", path+"/answer", url.Values{"csrf_token": {testCSRF}, "problem_id": {p.ID.String()},
		"problem_version": {"1"}, "selected": {"b"}}).Header().Get("Location")
	// The same attempt under another session is not found.
	otherSess := sessionPath(t, e)
	if rec := e.do("GET", otherSess+"/result/"+strings.TrimPrefix(loc, path+"/result/"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("attempt under another session = %d, want 404", rec.Code)
	}
	if rec := e.do("GET", path+"/result/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed attempt id = %d", rec.Code)
	}
	if rec := get(e.mux, loc, false); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous result = %d, want login redirect", rec.Code)
	}
	// Rating from the result page still returns to /review/{id}.
	att := strings.TrimPrefix(loc, path+"/result/")
	rate := e.do("POST", path+"/rate", url.Values{"csrf_token": {testCSRF}, "attempt_id": {att}, "rating": {"4"}})
	if rate.Code != http.StatusSeeOther || rate.Header().Get("Location") != path {
		t.Errorf("rate = %d %q", rate.Code, rate.Header().Get("Location"))
	}
	if body := e.do("GET", loc, nil).Body.String(); strings.Contains(body, `id="rate-form"`) {
		t.Error("a rated attempt still offers rating buttons")
	}
}

// A session belonging to another user is a 404, for the page, the result and the posts.
func TestReviewIsNotVisibleToAnotherUser(t *testing.T) {
	e := newQuizEnv(t)
	theirs, err := e.sessions.Create(context.Background(), e.other, review.CreateSessionInput{Kind: review.SessionReview})
	if err != nil {
		t.Fatal(err)
	}
	p := e.problem("Mine")
	base := "/review/" + theirs.ID.String()
	form := url.Values{"problem_id": {p.String()}, "problem_version": {"1"}, "selected": {"x"}, "attempt_id": {p.String()}, "rating": {"3"}}
	for _, c := range []struct {
		method, path string
		form         url.Values
	}{
		{"GET", base, nil}, {"GET", base + "/result/" + p.String(), nil},
		{"POST", base + "/answer", form}, {"POST", base + "/rate", form},
	} {
		if rec := e.do(c.method, c.path, c.form, true); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

func TestReviewNothingPickedAsksAgain(t *testing.T) {
	e := newTodayEnv(t)
	p := e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	path := sessionPath(t, e)
	rec := e.do("POST", path+"/answer", url.Values{"csrf_token": {testCSRF}, "problem_id": {p.ID.String()}, "problem_version": {"1"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "select at least one") ||
		!strings.Contains(rec.Body.String(), `data-review-phase="prompt"`) ||
		!strings.Contains(rec.Body.String(), `id="remaining">1<`) {
		t.Errorf("status %d:\n%s", rec.Code, rec.Body.String())
	}
}

func TestReviewPostsRequireCSRFSessionAndOwnership(t *testing.T) {
	e := newTodayEnv(t)
	p := e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	path := sessionPath(t, e)
	form := func(tok string) url.Values {
		return url.Values{"csrf_token": {tok}, "problem_id": {p.ID.String()}, "problem_version": {"1"}, "selected": {"b"},
			"attempt_id": {p.ID.String()}, "rating": {"3"}}
	}
	for _, target := range []string{path + "/answer", path + "/rate"} {
		for _, tok := range []string{"", "wrong"} {
			if rec := e.do("POST", target, form(tok)); rec.Code != http.StatusForbidden {
				t.Errorf("POST %s token %q: %d, want 403", target, tok, rec.Code)
			}
		}
		anon := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form(testCSRF).Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		e.mux.ServeHTTP(anon, req)
		if anon.Code != http.StatusSeeOther || !strings.HasPrefix(anon.Header().Get("Location"), "/auth/login") {
			t.Errorf("anonymous POST %s: %d %q", target, anon.Code, anon.Header().Get("Location"))
		}
	}
	anon := httptest.NewRecorder()
	e.mux.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, path, nil))
	if anon.Code != http.StatusSeeOther {
		t.Errorf("anonymous GET: %d", anon.Code)
	}
	// Unknown and malformed sessions are 404; a problem outside the session is refused.
	other := "/review/" + domain.NewIDGenerator(e.clock).New().String()
	if rec := e.do("GET", other, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: %d", rec.Code)
	}
	if rec := e.do("GET", "/review/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d", rec.Code)
	}
	stranger := e.create(t, problems.TypeMCQ, "Not in session", mcqPayload)
	f := form(testCSRF)
	f.Set("problem_id", stranger.ID.String())
	if rec := e.do("POST", path+"/answer", f); rec.Code != http.StatusBadRequest {
		t.Errorf("problem outside session: %d, want 400", rec.Code)
	}
}

func TestReviewRenderingIsInert(t *testing.T) {
	e := newTodayEnv(t)
	e.create(t, problems.TypeMCQ, "<script>alert(1)</script> [x](javascript:alert(1))",
		`{"choices":[{"id":"a","text":"<img src=x onerror=alert(1)>"},{"id":"b","text":"ok"}],"correct":["b"]}`)
	body := e.do("GET", sessionPath(t, e), nil).Body.String()
	for _, bad := range []string{"<script>alert", "javascript:alert", "<img src=x"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
}

func TestReviewFinishedSessionShowsDone(t *testing.T) {
	e := newTodayEnv(t)
	path := sessionPath(t, e) // nothing to review
	rec := e.do("GET", path, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "complete") || !strings.Contains(rec.Body.String(), "0 answered") {
		t.Errorf("empty session: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", path, nil); rec.Code != http.StatusOK {
		t.Errorf("completed session reopened: %d", rec.Code)
	}
}

// Quiz sessions land on the same page and are labelled as quizzes.
type quizStub struct{ completed bool }

func (q *quizStub) view() review.SessionView {
	v := review.SessionView{ID: domain.NewIDGenerator(domain.NewFakeClock(time.Now())).New(), Kind: review.SessionQuiz}
	if q.completed {
		now := time.Now()
		v.CompletedAt, v.Answered = &now, 3
	}
	return v
}
func (q *quizStub) Get(context.Context, domain.ID, domain.ID) (review.SessionView, error) {
	return q.view(), nil
}
func (q *quizStub) Next(context.Context, domain.ID, domain.ID) (review.NextView, error) {
	return review.NextView{}, nil
}
func (q *quizStub) Complete(context.Context, domain.ID, domain.ID) (review.SessionView, error) {
	q.completed = true
	return q.view(), nil
}

func TestReviewPageHandlesQuizSessions(t *testing.T) {
	site, err := web.New(web.Config{Gate: fakeGate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseReview(&quizStub{}, nil, nil)
	mux := http.NewServeMux()
	site.Register(mux)
	id := domain.NewIDGenerator(domain.NewFakeClock(time.Now())).New()
	body := get(mux, "/review/"+id.String(), true).Body.String()
	if !strings.Contains(body, "Quiz complete") || !strings.Contains(body, "3 answered") {
		t.Errorf("quiz done page:\n%s", body)
	}
}
