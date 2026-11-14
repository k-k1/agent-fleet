package memorelay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
