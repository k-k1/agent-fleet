package mcpproj

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
}

func gitAddCommit(t *testing.T, dir string, files ...string) {
	t.Helper()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(append([]string{"add"}, files...)...)
	run("commit", "-q", "-m", "x")
}

func writeRepoFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInspectNovelLabMotivatingExample replays docs/56 §1's real-world case: the
// same server duplicated across .mcp.json and opencode.json with two different
// placeholder dialects. The read must surface both the dialect danger AND the
// divergence (the two files literally spell the args differently).
func TestInspectNovelLabMotivatingExample(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, ".mcp.json", `{"mcpServers":{"syosetu":{"type":"stdio","command":"uv",
	  "args":["run","--quiet","${HOME}/repos/narou-mcp-stdio/narou_mcp.py"],
	  "env":{"SYOSETU_MIN_INTERVAL":"0.7"}}}}`)
	writeRepoFile(t, dir, "opencode.json", `{"mcp":{"syosetu":{"type":"local",
	  "command":["uv","run","--quiet","{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py"],
	  "environment":{"SYOSETU_MIN_INTERVAL":"0.7"},"enabled":true}}}`)
	gitAddCommit(t, dir, ".mcp.json", "opencode.json")

	snap, err := Inspect(dir, "novel-lab")
	if err != nil {
		t.Fatal(err)
	}
	if snap.VCS != "git" || snap.Worktree {
		t.Fatalf("vcs/worktree: %+v", snap.VCS)
	}

	byPath := map[string]File{}
	for _, f := range snap.Files {
		byPath[f.Path] = f
	}
	mj := byPath[".mcp.json"]
	if !mj.Exists || !mj.Parsable || !mj.Tracked || len(mj.Servers) != 1 {
		t.Fatalf(".mcp.json: %+v", mj)
	}
	oc := byPath["opencode.json"]
	if !oc.Exists || !oc.Parsable || !oc.Tracked || len(oc.Servers) != 1 {
		t.Fatalf("opencode.json: %+v", oc)
	}
	// Files that don't exist here must still be listed (docs/57 憲章「無いものが
	// 消えると分からない」), just Exists=false.
	cx := byPath[".codex/config.toml"]
	if cx.Exists {
		t.Fatalf("codex file should not exist: %+v", cx)
	}

	hasCode := func(code string) bool {
		for _, w := range snap.Warnings {
			if w.Code == code {
				return true
			}
		}
		return false
	}
	// Each file here already uses ITS OWN kind's correct dialect (claude's ${VAR},
	// opencode's {env:VAR}), so no per-file dialect mismatch fires — the danger in
	// this real example is naively copying one file's spelling into the other
	// (P1's plan/apply), which this read-only P0 does not do. What P0 DOES catch is
	// the duplication itself:
	if !hasCode(CodeServerDiverged) {
		t.Errorf("expected a divergence warning (args literally differ): %+v", snap.Warnings)
	}

	// Every kind is represented even without a file (agy has none at all).
	kindSeen := map[string]KindInfo{}
	for _, k := range snap.Kinds {
		kindSeen[k.Kind] = k
	}
	if kindSeen[session.KindAgy].HasProjectScope {
		t.Errorf("agy must report no project scope")
	}
	if !kindSeen[session.KindKiro].Unverified {
		t.Errorf("kiro must be marked unverified")
	}
}

func TestInspectNameHijackWarning(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, ".cursor/mcp.json", `{"mcpServers":{"af":{"command":"/bin/echo"}}}`)

	snap, err := Inspect(dir, "r")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range snap.Warnings {
		if w.Code == CodeNameHijack && w.Server == "af" {
			found = true
			if w.Severity != "red" {
				t.Fatalf("hijack should be red: %+v", w)
			}
		}
	}
	if !found {
		t.Fatalf("expected a hijack warning: %+v", snap.Warnings)
	}
}

// TestInspectSecretNeverOnWire is the independent assertion docs/56 §13 calls for:
// the raw secret-looking value must not appear ANYWHERE in the marshaled response,
// masked or not — checked on the JSON bytes, not just the typed field.
func TestInspectSecretNeverOnWire(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	const secret = "af-test-fixture-9f8e7d6c5b4a3210x"
	writeRepoFile(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"http","url":"https://example.com/mcp",
	  "headers":{"Authorization":"Bearer `+secret+`"}}}}`)
	gitAddCommit(t, dir, ".mcp.json")

	snap, err := Inspect(dir, "r")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("secret leaked into the wire response: %s", b)
	}

	var srv Server
	for _, f := range snap.Files {
		for _, s := range f.Servers {
			if s.Name == "srv" {
				srv = s
			}
		}
	}
	if srv.Headers["Authorization"] != "***" {
		t.Fatalf("expected masked header, got %q", srv.Headers["Authorization"])
	}

	found := false
	for _, w := range snap.Warnings {
		if w.Code == CodeSecretTracked && w.Key == "Authorization" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a tracked-secret warning: %+v", snap.Warnings)
	}
}

// TestInspectUnparsableFileUntouched: an unreadable file is reported, not rewritten
// or skipped-silently, and other files are still processed (docs/57 憲章3).
func TestInspectUnparsableFileUntouched(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	const bad = "{not json"
	writeRepoFile(t, dir, ".mcp.json", bad)
	writeRepoFile(t, dir, "opencode.json", `{"mcp":{"ok":{"type":"local","command":["/bin/echo"]}}}`)

	snap, err := Inspect(dir, "r")
	if err != nil {
		t.Fatal(err)
	}
	var mj, oc File
	for _, f := range snap.Files {
		switch f.Path {
		case ".mcp.json":
			mj = f
		case "opencode.json":
			oc = f
		}
	}
	if !mj.Exists || mj.Parsable || mj.Note == "" {
		t.Fatalf(".mcp.json should be exists=true parsable=false with a note: %+v", mj)
	}
	if !oc.Exists || !oc.Parsable || len(oc.Servers) != 1 {
		t.Fatalf("opencode.json should still parse: %+v", oc)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != bad {
		t.Fatalf("unparsable file must never be rewritten: %q", after)
	}

	hasUnreadable := false
	for _, w := range snap.Warnings {
		if w.Code == CodeFileUnreadable && w.File == ".mcp.json" {
			hasUnreadable = true
		}
	}
	if !hasUnreadable {
		t.Fatalf("expected a file-unreadable warning: %+v", snap.Warnings)
	}
}

// TestInspectNoVCSMarksUncertainNotFalse: outside git entirely, secret-looking
// values must still warn (as "cannot determine", not silently "safe") — docs/56
// §7.2's VCS-not-git row, docs/57 憲章6.
func TestInspectNoVCSMarksUncertain(t *testing.T) {
	dir := t.TempDir() // no git init
	const secret = "af-test-fixture-9f8e7d6c5b4a3210x"
	writeRepoFile(t, dir, ".mcp.json", `{"mcpServers":{"srv":{"type":"http","url":"https://example.com/mcp",
	  "headers":{"Authorization":"Bearer `+secret+`"}}}}`)

	snap, err := Inspect(dir, "r")
	if err != nil {
		t.Fatal(err)
	}
	if snap.VCS != "none" {
		t.Fatalf("expected vcs=none, got %q", snap.VCS)
	}
	for _, f := range snap.Files {
		if f.Path == ".mcp.json" && (!f.TrackedUncertain || f.Tracked) {
			t.Fatalf(".mcp.json tracked state: %+v", f)
		}
	}
	found := false
	for _, w := range snap.Warnings {
		if w.Code == CodeSecretVCSUncertain {
			found = true
		}
		if w.Code == CodeSecretTracked {
			t.Fatalf("must not claim definitively tracked outside git: %+v", w)
		}
	}
	if !found {
		t.Fatalf("expected a vcs-uncertain secret warning: %+v", snap.Warnings)
	}
}

func TestInspectWorktreeNoted(t *testing.T) {
	parent := t.TempDir()
	gitInit(t, parent)
	writeRepoFile(t, parent, "a.txt", "x")
	gitAddCommit(t, parent, "a.txt")

	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "-C", parent, "worktree", "add", "-q", "-b", "side", wt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, out)
	}

	snap, err := Inspect(wt, "r")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Worktree {
		t.Fatalf("expected Worktree=true for a linked worktree")
	}
}
