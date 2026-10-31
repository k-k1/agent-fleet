package usageall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func stub(t *testing.T, m map[string]http.HandlerFunc) {
	t.Helper()
	old := sources
	sources = m
	t.Cleanup(func() { sources = old })
}

func call(t *testing.T, target string) map[string]map[string]any {
	t.Helper()
	k, _ := callFull(t, target)
	return k
}

func callFull(t *testing.T, target string) (map[string]map[string]any, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	Handle(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Kinds   map[string]map[string]any `json:"kinds"`
		Pending []string                  `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out.Kinds, out.Pending
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

// One panicking handler must cost its own kind, not the Agent: outside the request goroutine
// net/http's recovery does not reach it.
func TestAPanickingHandlerIsLeftOut(t *testing.T) {
	stub(t, map[string]http.HandlerFunc{
		"claude": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":true}`)) },
		"agy":    func(http.ResponseWriter, *http.Request) { panic("parse drift") },
	})
	k, pending := callFull(t, "/usage/all")
	if k["claude"]["ok"] != true || len(pending) != 0 {
		t.Fatalf("kinds %v pending %v", k, pending)
	}
	if _, ok := k["agy"]; ok {
		t.Error("the panicking kind was reported")
	}
}

// A cold slow kind must not hold the fast chips blank: past the budget the answer goes out
// with the slow kind pending, and that kind keeps running so its cache fills.
func TestSlowKindIsPendingAndKeepsRunning(t *testing.T) {
	prev := budget
	budget = 50 * time.Millisecond
	t.Cleanup(func() { budget = prev })
	release := make(chan struct{})
	finished := make(chan error, 1)
	stub(t, map[string]http.HandlerFunc{
		"claude": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":true}`)) },
		"agy": func(w http.ResponseWriter, r *http.Request) {
			<-release
			finished <- r.Context().Err()
			w.Write([]byte(`{"ok":true}`))
		},
	})
	// net/http cancels a request's context once its handler returns; do the same here.
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	start := time.Now()
	Handle(rec, httptest.NewRequest(http.MethodGet, "/usage/all", nil).WithContext(ctx))
	cancel()
	if time.Since(start) > 2*time.Second {
		t.Fatal("the answer waited for the slow kind")
	}
	var out struct {
		Kinds   map[string]map[string]any `json:"kinds"`
		Pending []string                  `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kinds["claude"]["ok"] != true || !reflect.DeepEqual(out.Pending, []string{"agy"}) {
		t.Fatalf("answer %s, want claude now and agy pending", rec.Body.String())
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("the late handler's request was cancelled (%v): its fetch would fail and never fill the cache", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the slow kind did not keep running")
	}
}

// The reload button waits for its own kind however long it takes.
func TestRefreshedKindIsAlwaysWaitedFor(t *testing.T) {
	prev := budget
	budget = 20 * time.Millisecond
	t.Cleanup(func() { budget = prev })
	stub(t, map[string]http.HandlerFunc{
		"agy": func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.Write([]byte(`{"ok":true}`))
		},
	})
	k, pending := callFull(t, "/usage/all?refresh=1&kind=agy")
	if k["agy"]["ok"] != true || len(pending) != 0 {
		t.Fatalf("kinds %v pending %v, want agy answered", k, pending)
	}
}
