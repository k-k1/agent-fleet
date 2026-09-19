package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// planFileEnv isolates HOME (the status store) and CLAUDE_CONFIG_DIR (the plans
// directory), writes a claude session with a pending plan, and returns it with its sid.
func planFileEnv(t *testing.T, plan string) (session.Meta, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	m := session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	if plan != "" {
		status.WritePendingPlan(sid, plan)
	}
	return m, sid
}

func planFileServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/{name}/plan-file", HandleSessionPlanFile)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writePlanInPlansDir creates <CLAUDE_CONFIG_DIR>/plans/<slug>.md, as claude's own Write
// tool would.
func writePlanInPlansDir(t *testing.T, slug, body string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

type planFileResp struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

func TestPlanFileReturnsTheRecordedClaudeFile(t *testing.T) {
	m, sid := planFileEnv(t, "# Plan\n\ndo the thing\n")
	path := writePlanInPlansDir(t, "eager-twirling-frost", "# Plan\n\ndo the thing\n")
	status.WritePlanFile(sid, path)

	var got planFileResp
	do(t, planFileServer(t), "GET", "/sessions/"+m.Name+"/plan-file", nil, 200, &got)
	if got.Source != "claude" || got.Path != path {
		t.Fatalf("got %+v, want the recorded path with source=claude (%s)", got, path)
	}
}

// A record whose file is gone (the config dir moved, the file was cleaned up) must fall
// back rather than hand out a path nothing can read.
func TestPlanFileFallsBackWhenTheRecordedFileIsGone(t *testing.T) {
	m, sid := planFileEnv(t, "# Plan\n\nbody\n")
	path := writePlanInPlansDir(t, "gone", "# Plan\n\nbody\n")
	status.WritePlanFile(sid, path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var got planFileResp
	do(t, planFileServer(t), "GET", "/sessions/"+m.Name+"/plan-file", nil, 200, &got)
	if got.Source != "snapshot" {
		t.Fatalf("got %+v, want source=snapshot", got)
	}
}

// No record at all (an older claude that wrote the plan file itself, so the hook never
// saw a path): the snapshot carries the pending text verbatim.
func TestPlanFileSnapshotsWhenNothingWasRecorded(t *testing.T) {
	const plan = "# Plan\n\n何をするか\n"
	m, _ := planFileEnv(t, plan)

	var got planFileResp
	do(t, planFileServer(t), "GET", "/sessions/"+m.Name+"/plan-file", nil, 200, &got)
	if got.Source != "snapshot" {
		t.Fatalf("got %+v, want source=snapshot", got)
	}
	b, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read snapshot %s: %v", got.Path, err)
	}
	if string(b) != plan {
		t.Fatalf("snapshot = %q, want the pending plan %q", b, plan)
	}
}

// Asking twice for the same plan must settle on ONE path: the reviewer's first prompt has
// already been sent with it.
func TestPlanFileSnapshotPathIsStableForTheSamePlan(t *testing.T) {
	m, _ := planFileEnv(t, "# Plan\n\nsame\n")
	srv := planFileServer(t)
	var first, second planFileResp
	do(t, srv, "GET", "/sessions/"+m.Name+"/plan-file", nil, 200, &first)
	do(t, srv, "GET", "/sessions/"+m.Name+"/plan-file", nil, 200, &second)
	if first.Path != second.Path {
		t.Fatalf("snapshot path moved: %s -> %s", first.Path, second.Path)
	}
}

func TestPlanFileRefusesWithNoPendingPlan(t *testing.T) {
	m, _ := planFileEnv(t, "")
	if code := httpStatus(t, planFileServer(t), "GET", "/sessions/"+m.Name+"/plan-file", nil); code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
}

// planFileOf is what stops the Write/Edit hook from recording every edit as "the plan".
// Both directions are asserted: a check that only ever accepts and a check that never
// accepts look the same from the recording side.
func TestPlanFileOfAcceptsOnlyPlansDirMarkdown(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	plans := filepath.Join(cfg, "plans")

	if got := planFileOf(filepath.Join(plans, "brave-wandering-owl.md")); got == "" {
		t.Fatal("a plans/*.md path was rejected — nothing would ever be recorded")
	}
	for _, p := range []string{
		"/home/dev/repos/app/docs/plan.md",          // an ordinary edit in the repo
		filepath.Join(cfg, "plansible", "x.md"),     // sibling directory sharing the prefix
		filepath.Join(plans, "nested", "deeper.md"), // not directly in plans/
		filepath.Join(plans, "notes.txt"),           // not markdown
		"plans/relative.md",                         // relative
		"",
	} {
		if got := planFileOf(p); got != "" {
			t.Fatalf("planFileOf(%q) = %q, want it rejected", p, got)
		}
	}
}

// The recording side, through the real hook entry point: a plan file is remembered and an
// ordinary edit in the working copy is not.
func TestStatusHookRecordsOnlyThePlanFile(t *testing.T) {
	m, sid := planFileEnv(t, "# Plan\n\nbody\n")
	plan := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "plans", "quiet-rolling-pebble.md")

	feedStatusHook(t, "permtool", `{"session_id":"`+sid+`","tool_name":"Write","tool_input":{"file_path":"`+filepath.Join(m.Dir, "README.md")+`"}}`)
	if got, ok := status.ReadPlanFile(sid); ok {
		t.Fatalf("an edit inside the working copy was recorded as the plan file: %s", got)
	}
	feedStatusHook(t, "permtool", `{"session_id":"`+sid+`","tool_name":"Write","tool_input":{"file_path":"`+plan+`"}}`)
	if got, _ := status.ReadPlanFile(sid); got != plan {
		t.Fatalf("plan file = %q, want %q", got, plan)
	}
}
