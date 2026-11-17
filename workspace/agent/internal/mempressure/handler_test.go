package mempressure

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func serveWindows(t *testing.T, query string) (*httptest.ResponseRecorder, WindowsPage) {
	t.Helper()
	w := httptest.NewRecorder()
	HandleWindows(w, httptest.NewRequest(http.MethodGet, "/workspace/usage-windows"+query, nil))
	var p WindowsPage
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
	}
	return w, p
}

func TestHandleWindows(t *testing.T) {
	// No sampler (AF_MEM_PRESSURE=off): an empty page, not an error, and the caller's cursor back.
	defMu.Lock()
	Default = nil
	defMu.Unlock()
	if w, p := serveWindows(t, "?since=5"); w.Code != 200 || len(p.Windows) != 0 || p.Cursor != 5 {
		t.Fatalf("no sampler: %d %+v", w.Code, p)
	}
	{
		body := httptest.NewRecorder()
		HandleWindows(body, httptest.NewRequest(http.MethodGet, "/workspace/usage-windows", nil))
		if got := body.Body.String(); !strings.Contains(got, `"windows":[]`) {
			t.Fatalf("an empty page must carry [] and not null: %s", got)
		}
	}

	series := NewSeries(filepath.Join(t.TempDir(), "series.jsonl"), 0)
	appendJSON(t, series, map[string]any{"t": 60, "lid": "A", "ws": 9})
	appendJSON(t, series, map[string]any{"t": 120, "lid": "A", "ws": 10})
	defMu.Lock()
	Default = &Sampler{series: series}
	defMu.Unlock()
	t.Cleanup(func() { defMu.Lock(); Default = nil; defMu.Unlock() })

	if w, p := serveWindows(t, "?since=60"); w.Code != 200 || len(p.Windows) != 1 || p.Windows[0].T != 120 || p.Cursor != 120 {
		t.Fatalf("since=60: %d %+v", w.Code, p)
	}
	if w, p := serveWindows(t, "?limit=1"); w.Code != 200 || len(p.Windows) != 1 || !p.More || p.Cursor != 60 {
		t.Fatalf("limit=1: %d %+v", w.Code, p)
	}
	for _, q := range []string{"?since=x", "?since=-1", "?limit=0", "?limit=x"} {
		if w, _ := serveWindows(t, q); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, w.Code)
		}
	}
}
