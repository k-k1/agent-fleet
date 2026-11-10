package wtoutput

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

func httpHome(t *testing.T) (home, wt string) {
	t.Helper()
	home, _ = filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	main := filepath.Join(home, "repos", "app")
	wt = filepath.Join(home, "repos", "app@wip")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, main, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(main, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, main, "add", "-A")
	run(t, main, "commit", "-q", "-m", "init")
	run(t, main, "worktree", "add", "-q", "-b", "wip", wt)
	if err := os.MkdirAll(filepath.Join(wt, "node_modules", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "node_modules", "p", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, wt
}

func TestHandlers(t *testing.T) {
	_, wt := httpHome(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	gd := filepath.Join(filepath.Dir(wt), "app", ".git", "worktrees", "app@wip")
	for _, p := range []string{filepath.Join(gd, "HEAD"), filepath.Join(gd, "index"), filepath.Join(wt, ".git")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	do := func(method, target string, h http.HandlerFunc, name string) (int, Report) {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		r := httptest.NewRequest(method, target+sep+"worktree="+url.QueryEscape(name), nil)
		w := httptest.NewRecorder()
		h(w, r)
		var rep Report
		_ = json.Unmarshal(w.Body.Bytes(), &rep)
		return w.Code, rep
	}

	// Survey lists the worktree (and only it: the parent clone is not a candidate).
	r := httptest.NewRequest("GET", "/cleanup/worktree-output", nil)
	w := httptest.NewRecorder()
	HandleSurvey(w, r)
	var sv surveyAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &sv); err != nil || len(sv.Worktrees) != 1 || sv.Worktrees[0].Status != "candidate" {
		t.Fatalf("survey = %d %s", w.Code, w.Body.String())
	}

	if code, _ := do("POST", "/x?dormant_days=0", HandleApply, "app@wip"); code != http.StatusBadRequest {
		t.Errorf("dormant_days=0 → %d", code)
	}
	if code, _ := do("POST", "/x", HandleApply, "../etc"); code != http.StatusBadRequest {
		t.Errorf("path-shaped name → %d", code)
	}
	if code, rep := do("POST", "/x", HandleApply, "app"); code != http.StatusConflict || rep.Reason != ReasonNotWorktree {
		t.Errorf("parent clone → %d %+v", code, rep)
	}
	if code, rep := do("POST", "/x?dry_run=1", HandleApply, "app@wip"); code != http.StatusOK || rep.Status != "candidate" || len(rep.Removed) != 0 {
		t.Fatalf("dry run → %d %+v", code, rep)
	}
	if _, err := os.Stat(filepath.Join(wt, "node_modules")); err != nil {
		t.Fatal("dry run deleted node_modules")
	}
	if code, rep := do("POST", "/x", HandleApply, "app@wip"); code != http.StatusOK || len(rep.Removed) != 1 {
		t.Fatalf("apply → %d %+v", code, rep)
	}
	if _, err := os.Lstat(filepath.Join(wt, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules still there")
	}
}

func TestLastActivityInDirReadsStoppedAndCreated(t *testing.T) {
	metas := []session.Meta{
		{Dir: "/r/app@wip", CreatedAt: "2026-10-01T00:00:00Z", StoppedAt: "2026-10-03T00:00:00Z"},
		{Dir: "/r/app@wip/sub", CreatedAt: "2026-10-05T00:00:00Z"},
		{Dir: "/r/app@wip-other", CreatedAt: "2026-11-01T00:00:00Z"},
	}
	got := sessionx.LastActivityInDir(metas, "/r/app@wip")
	if want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v (a sibling whose name starts the same must not count)", got, want)
	}
	if !sessionx.LastActivityInDir(nil, "/r/x").IsZero() {
		t.Error("no sessions must read as zero")
	}
}
