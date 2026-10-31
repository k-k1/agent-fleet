package usageall

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func stub(t *testing.T, m map[string]http.HandlerFunc) {
	t.Helper()
	old := sources
	sources = m
	t.Cleanup(func() { sources = old })
}

func call(t *testing.T, target string) map[string]map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	Handle(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Kinds map[string]map[string]any `json:"kinds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out.Kinds
}

func TestEveryKindIsCollected(t *testing.T) {
	stub(t, map[string]http.HandlerFunc{
		"claude": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":true,"authed":true}`)) },
		"muse":   func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":false,"authed":true}`)) },
		"broken": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
	})
	k := call(t, "/usage/all")
	if k["claude"]["ok"] != true || k["muse"]["authed"] != true {
		t.Errorf("kinds = %v", k)
	}
	if _, ok := k["broken"]; ok {
		t.Error("a kind that answered 500 was reported; the Console must read it as no new reading")
	}
}

func TestRefreshReachesOnlyTheNamedKind(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	h := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got[kind] = r.URL.Query().Get("refresh")
			mu.Unlock()
			w.Write([]byte(`{}`))
		}
	}
	stub(t, map[string]http.HandlerFunc{"agy": h("agy"), "copilot": h("copilot")})
	call(t, "/usage/all?refresh=1&kind=copilot")
	if got["copilot"] != "1" || got["agy"] != "" {
		t.Errorf("refresh forwarded as %v, want copilot only", got)
	}
	call(t, "/usage/all?kind=copilot")
	if got["copilot"] != "" {
		t.Error("kind without refresh forced a refresh")
	}
}
