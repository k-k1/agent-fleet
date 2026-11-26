package memorelay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeCP is a Control Plane standing in for the real one: it records each request and answers
// from its table, keyed by "METHOD path".
type fakeCP struct {
	mu      sync.Mutex
	calls   []string
	bodies  []string
	answers map[string]struct {
		status int
		body   string
	}
}

func (f *fakeCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.bodies = append(f.bodies, string(b))
	a, ok := f.answers[key]
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, "no token", http.StatusUnauthorized)
		return
	}
	if !ok {
		a.status, a.body = http.StatusOK, `{}`
	}
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

func (f *fakeCP) answer(key string, status int, body string) {
	if f.answers == nil {
		f.answers = map[string]struct {
			status int
			body   string
		}{}
	}
	f.answers[key] = struct {
		status int
		body   string
	}{status, body}
}

// newFakeCP points the bridge at a fake CP and gives the workspace a session to stamp.
func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &fakeCP{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	t.Setenv("AF_MEMO_TOKEN", "test-token")
	session.WriteMeta(session.Meta{Name: "slot01", Kind: session.KindClaude, Origin: session.OriginUser,
		CreatedAt: "2026-11-14T10:00:00+09:00"})
	session.WriteMeta(session.Meta{Name: "kid01", Kind: session.KindClaude, Origin: session.OriginSession,
		OriginSession: "slot01", CreatedAt: "2026-11-14T10:00:00+09:00"})
	return f
}

func serve(h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /memos/{id}", h)
	mux.HandleFunc("POST /memos", h)
	mux.HandleFunc("GET /memos", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	return m
}

// The producer is the calling session and its origin as its meta records them. A value for
// either inside the memo the model wrote is not read, whatever it says.
func TestCreateStampsTheCallingSessionAndIgnoresTheModelsProducer(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m1"}`)
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"kid01","memo":{"kind":"text","body":"fix the flake","category":"impl",`+
			`"producer":"forged","producerOrigin":"user","provenance":"member","state":"pending"}}`)
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"id":"m1"}` {
		t.Fatalf("create answered %d %q, want the CP's 201 as it came", rec.Code, rec.Body.String())
	}
	if len(cp.calls) != 1 || cp.calls[0] != "POST /internal/memos" {
		t.Fatalf("CP calls = %q", cp.calls)
	}
	got := decode(t, cp.bodies[0])
	want := map[string]any{"kind": "text", "body": "fix the flake", "refPath": "", "repo": "", "category": "impl",
		"producer": "kid01", "producerOrigin": session.OriginSession}
	if len(got) != len(want) {
		t.Errorf("CP body = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("CP body %s = %v, want %v (body %s)", k, got[k], v, cp.bodies[0])
		}
	}
}

// The operator assistant has no session: an empty from is stamped as the operator.
func TestCreateFromTheOperator(t *testing.T) {
	cp := newFakeCP(t)
	rec := serve(HandleCreate, http.MethodPost, "/memos", `{"memo":{"kind":"text","body":"x"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d %s", rec.Code, rec.Body.String())
	}
	got := decode(t, cp.bodies[0])
	if got["producer"] != ProducerOperator || got["producerOrigin"] != session.OriginOperator {
		t.Errorf("operator create stamped %v / %v", got["producer"], got["producerOrigin"])
	}
}

// A caller naming a session this workspace does not have is refused, and nothing is sent.
func TestCreateRefusesAnUnknownCaller(t *testing.T) {
	cp := newFakeCP(t)
	for _, from := range []string{"ghost01", "../x", "a b"} {
		rec := serve(HandleCreate, http.MethodPost, "/memos", `{"from":"`+from+`","memo":{"kind":"text","body":"x"}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_from") {
			t.Errorf("from=%q answered %d %s, want 400 bad_from", from, rec.Code, rec.Body.String())
		}
	}
	if len(cp.calls) != 0 {
		t.Errorf("a refused create reached the CP: %q", cp.calls)
	}
}

// A refusal of the CP keeps its status and code, so the tool can say what it was.
func TestRelayKeepsTheCPRefusal(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("PATCH /internal/memos/m1", http.StatusConflict, `{"error":{"code":"memo_locked","message":"locked"}}`)
	rec := serve(HandleUpdate, http.MethodPatch, "/memos/m1", `{"body":"x"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "memo_locked") {
		t.Errorf("update answered %d %s, want the CP's 409 memo_locked", rec.Code, rec.Body.String())
	}
}

// An update forwards the fields update_memo may change and nothing else, and leaves out the
// ones the caller did not pass (the CP leaves those as they are).
func TestUpdateForwardsOnlyThePatchFields(t *testing.T) {
	cp := newFakeCP(t)
	rec := serve(HandleUpdate, http.MethodPatch, "/memos/m%2F1",
		`{"id":"m/1","body":"corrected","position":2,"producer":"forged","state":"pending"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update answered %d %s", rec.Code, rec.Body.String())
	}
	if cp.calls[0] != "PATCH /internal/memos/m/1" {
		t.Errorf("CP call = %q", cp.calls[0])
	}
	if cp.bodies[0] != `{"body":"corrected","position":2}` {
		t.Errorf("CP body = %s", cp.bodies[0])
	}
}

// The list carries the memos and the categories (with queue) together. When only the categories
// cannot be read, the memos are still answered and the reason is said.
func TestListCarriesMemosAndCategories(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("GET /internal/memos", http.StatusOK, `[{"id":"m1","state":"held"}]`)
	cp.answer("GET /internal/memo-categories", http.StatusOK, `[{"id":"c1","name":"impl","queue":true}]`)
	rec := serve(HandleList, http.MethodGet, "/memos", "")
	want := `{"memos":[{"id":"m1","state":"held"}],"categories":[{"id":"c1","name":"impl","queue":true}]}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("list answered %d %s, want %s", rec.Code, rec.Body.String(), want)
	}

	cp.answer("GET /internal/memo-categories", http.StatusNotFound, `404 page not found`)
	rec = serve(HandleList, http.MethodGet, "/memos", "")
	got := decode(t, rec.Body.String())
	if got["categories"] != nil || !strings.Contains(got["categoriesError"].(string), "404") || got["memos"] == nil {
		t.Errorf("list without categories = %s", rec.Body.String())
	}
}

// Without the bridge the relay says so in the same words the tools always used, and calls
// nothing.
func TestRelayWithoutTheBridge(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_MEMO_TOKEN", "")
	for _, h := range []struct {
		h              http.HandlerFunc
		method, target string
	}{{HandleList, http.MethodGet, "/memos"}, {HandleCreate, http.MethodPost, "/memos"}, {HandleUpdate, http.MethodPatch, "/memos/m1"}} {
		rec := serve(h.h, h.method, h.target, `{"memo":{"kind":"text","body":"x"}}`)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), UnavailableMessage) {
			t.Errorf("%s %s answered %d %s", h.method, h.target, rec.Code, rec.Body.String())
		}
	}
}

// A Control Plane that does not answer the categories read in time still leaves the memos to
// answer with. The relay's own bound is what makes that happen, not the caller's.
func TestListAnswersMemosWhenCategoriesHang(t *testing.T) {
	newFakeCP(t)
	prev := CPTimeout
	CPTimeout = 200 * time.Millisecond
	t.Cleanup(func() { CPTimeout = prev })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/memo-categories" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, `[{"id":"m1"}]`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	start := time.Now()
	rec := serve(HandleList, http.MethodGet, "/memos", "")
	got := decode(t, rec.Body.String())
	if rec.Code != http.StatusOK || got["memos"] == nil || got["categoriesError"] == nil {
		t.Errorf("list = %d %s, want the memos and a categoriesError", rec.Code, rec.Body.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("list took %v with a 200ms CP bound", d)
	}
}

// A reference on a create reaches the Control Plane as the object the model wrote, and a create
// without one sends no reference key at all.
func TestCreateForwardsTheReference(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m1"}`)
	ref := `{"provider":"github","key":"acme/app#12","url":"https://github.com/acme/app/issues/12"}`
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"kid01","memo":{"kind":"text","body":"","category":"impl","reference":`+ref+`,"extra":"dropped"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create answered %d", rec.Code)
	}
	got := decode(t, cp.bodies[0])
	if want := decode(t, ref); !reflect.DeepEqual(got["reference"], map[string]any(want)) {
		t.Errorf("CP got reference %v, want %v (body %s)", got["reference"], want, cp.bodies[0])
	}
	if _, ok := got["extra"]; ok {
		t.Errorf("an unknown field was forwarded: %s", cp.bodies[0])
	}

	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x"}}`)
	if strings.Contains(cp.bodies[1], "reference") {
		t.Errorf("a create without a reference sent one: %s", cp.bodies[1])
	}
	// A JSON null is no reference either.
	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x","reference":null}}`)
	if strings.Contains(cp.bodies[2], "reference") {
		t.Errorf("a null reference was forwarded: %s", cp.bodies[2])
	}
}

// An update's reference keeps the Control Plane's three meanings on the wire: absent leaves it,
// {} clears it, a full object sets it.
func TestUpdateReferenceThreeWays(t *testing.T) {
	cp := newFakeCP(t)
	for _, c := range []struct{ name, in, want string }{
		{"absent", `{"body":"b"}`, `{"body":"b"}`},
		{"null is absent", `{"body":"b","reference":null}`, `{"body":"b"}`},
		{"empty object clears", `{"reference":{}}`, `{"reference":{}}`},
		{"full object sets", `{"reference":{"provider":"jira","key":"PROJ-7","url":"https://j.example/browse/PROJ-7"}}`,
			`{"reference":{"provider":"jira","key":"PROJ-7","url":"https://j.example/browse/PROJ-7"}}`},
		{"partial object is the Control Plane's to refuse", `{"reference":{"provider":"jira"}}`, `{"reference":{"provider":"jira"}}`},
	} {
		cp.bodies = nil
		rec := serve(HandleUpdate, http.MethodPatch, "/memos/m1", c.in)
		if rec.Code != http.StatusOK || len(cp.bodies) != 1 {
			t.Fatalf("%s: answered %d, %d CP calls", c.name, rec.Code, len(cp.bodies))
		}
		if cp.bodies[0] != c.want {
			t.Errorf("%s: CP got %s, want %s", c.name, cp.bodies[0], c.want)
		}
	}
}

// The Control Plane's answer, with existing and the state, goes back to the caller untouched.
func TestCreateAnswersExistingAsItCame(t *testing.T) {
	cp := newFakeCP(t)
	ans := `{"id":"m1","state":"held","existing":true,"reference":{"provider":"github","key":"acme/app#12","url":"https://github.com/acme/app/issues/12"}}`
	cp.answer("POST /internal/memos", http.StatusOK, ans)
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"kid01","memo":{"kind":"text","reference":{"provider":"github","key":"acme/app#12","url":"https://github.com/acme/app/issues/12"}}}`)
	if rec.Code != http.StatusOK || rec.Body.String() != ans {
		t.Fatalf("answered %d %q, want the Control Plane's answer as it came", rec.Code, rec.Body.String())
	}
}

// The prerequisites ride a create as the model wrote them, and an update keeps three meanings:
// absent leaves them, [] clears them, a list replaces them.
func TestAfterIsForwarded(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m2"}`)
	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x","category":"impl","after":["m1","acme/app#12"]}}`)
	if got := decode(t, cp.bodies[0]); !reflect.DeepEqual(got["after"], []any{"m1", "acme/app#12"}) {
		t.Fatalf("CP got after %v (body %s)", got["after"], cp.bodies[0])
	}
	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x"}}`)
	if strings.Contains(cp.bodies[1], "after") {
		t.Errorf("a create without prerequisites sent some: %s", cp.bodies[1])
	}
	for _, c := range []struct{ name, in, want string }{
		{"absent", `{"body":"b"}`, `{"body":"b"}`},
		{"empty list clears", `{"after":[]}`, `{"after":[]}`},
		{"list replaces", `{"after":["m1"]}`, `{"after":["m1"]}`},
	} {
		cp.bodies = nil
		rec := serve(HandleUpdate, http.MethodPatch, "/memos/m2", c.in)
		if rec.Code != http.StatusOK || len(cp.bodies) != 1 || cp.bodies[0] != c.want {
			t.Errorf("%s: %d, CP got %v, want %s", c.name, rec.Code, cp.bodies, c.want)
		}
	}
}

// A continuation carries the delivery from the session's own meta. A queueDelivery the model
// wrote inside the memo is dropped by the decode, and the claim from a session no queue
// started is refused here rather than sent on as a plain create.
func TestContinuationStampsTheDeliveryFromMetaAndDropsTheModelsOwn(t *testing.T) {
	cp := newFakeCP(t)
	session.WriteMeta(session.Meta{Name: "run01", Kind: session.KindClaude, Origin: session.OriginQueue,
		QueueDelivery: "dlv-real", CreatedAt: "2026-11-14T10:00:00+09:00"})
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m1"}`)
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"run01","memo":{"kind":"text","body":"part 2","category":"impl","continuation":true,"queueDelivery":"dlv-forged"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("answered %d %q", rec.Code, rec.Body.String())
	}
	got := decode(t, cp.bodies[0])
	if got["queueDelivery"] != "dlv-real" || got["continuation"] != true {
		t.Errorf("CP body = %s, want the delivery from meta and the claim", cp.bodies[0])
	}

	before := len(cp.calls)
	rec = serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"slot01","memo":{"kind":"text","body":"x","category":"impl","continuation":true,"queueDelivery":"dlv-forged"}}`)
	if rec.Code != http.StatusBadRequest || len(cp.calls) != before {
		t.Errorf("a continuation from a session no queue started answered %d and made %d CP calls, want 400 and none", rec.Code, len(cp.calls)-before)
	}

	rec = serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"slot01","memo":{"kind":"text","body":"x","category":"impl","queueDelivery":"dlv-forged"}}`)
	if got := decode(t, cp.bodies[len(cp.bodies)-1]); got["queueDelivery"] != nil {
		t.Errorf("a plain create carried queueDelivery %v", got["queueDelivery"])
	}
}

// runNext rides on a create to the CP; an update drops it, so a session never changes the mark on
// a message already waiting (#5021).
func TestRunNextOnCreateOnly(t *testing.T) {
	cp := newFakeCP(t)
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m2"}`)
	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x","category":"impl","runNext":true}}`)
	if got := decode(t, cp.bodies[0]); got["runNext"] != true {
		t.Fatalf("CP got runNext %v (body %s)", got["runNext"], cp.bodies[0])
	}
	serve(HandleCreate, http.MethodPost, "/memos", `{"from":"kid01","memo":{"kind":"text","body":"x"}}`)
	if strings.Contains(cp.bodies[1], "runNext") {
		t.Errorf("a create without the mark sent one: %s", cp.bodies[1])
	}
	cp.bodies = nil
	rec := serve(HandleUpdate, http.MethodPatch, "/memos/m2", `{"body":"b","runNext":true}`)
	if rec.Code != http.StatusOK || len(cp.bodies) != 1 || cp.bodies[0] != `{"body":"b"}` {
		t.Fatalf("update: %d, CP got %v", rec.Code, cp.bodies)
	}
}

func TestMovedQueueRootRelaysItsLaunchIdentity(t *testing.T) {
	cp := newFakeCP(t)
	session.WriteMeta(session.Meta{Name: "launch-old", Kind: session.KindClaude, Origin: session.OriginQueue,
		QueueDelivery: "moved-dlv", MovedTo: "continued"})
	session.WriteMeta(session.Meta{Name: "continued", Kind: session.KindCodex, Origin: session.OriginUser})
	cp.answer("POST /internal/memos", http.StatusCreated, `{"id":"m1"}`)
	cp.answer("POST /internal/memo-deliveries/moved-dlv/defer", http.StatusOK, `{}`)
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":"continued","memo":{"kind":"text","body":"part 2","continuation":true,"queueLaunch":"forged"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("continuation: %d %s", rec.Code, rec.Body.String())
	}
	got := decode(t, cp.bodies[0])
	if got["queueDelivery"] != "moved-dlv" || got["queueLaunch"] != "launch-old" ||
		got["producer"] != "continued" || got["producerOrigin"] != "queue" {
		t.Fatalf("wrong stamped identity: %s", cp.bodies[0])
	}
	rec = serveDefer(`{"from":"continued","reason":"waiting"}`)
	if rec.Code != http.StatusOK || decode(t, cp.bodies[1])["session"] != "launch-old" {
		t.Fatalf("defer: %d %s, relay bodies %v", rec.Code, rec.Body.String(), cp.bodies)
	}
	rec = serveDefer(`{"from":"launch-old","reason":"waiting"}`)
	if rec.Code != http.StatusConflict || len(cp.bodies) != 2 {
		t.Fatalf("replaced source sent a request: %d %s, relay bodies %v", rec.Code, rec.Body.String(), cp.bodies)
	}
}
