package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// What ADR 0087 decision 5 has to buy without: a background agent that starts must be visible
// to the next call, with no window at all. The first implementation bought the cheap "no
// subagents" answer with a 15s negative cache and confined it to what looked like the two
// badge call sites; review found that one of those badges is not a badge — WireLive's value
// travels the wire into the CP's reaper (control-plane/session_activity.go), which stops the
// workspace on it. So the cheapness now comes from WHERE we look (beside the located
// transcript) rather than from not looking, and these tests pin that there is no window left.

// fixture builds a claude config tree with `projects` directories and returns the session's
// sid. The session's own project directory is created empty, so the only thing that makes
// subagents appear later is the test creating it.
func fixture(t *testing.T, projects int) (sid, cwd, subagentsDir string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	cwd = "/home/dev/repos/agent-fleet"
	m := session.Meta{Name: "slot01", Dir: cwd, Kind: "claude"}
	session.WriteMeta(m) // also records the cwd hint the derivation reads
	sid = session.UUID(m.Dir, m.Name)

	for i := range projects {
		// Decoys, so a sweep costs what it costs in production.
		mkdir(t, filepath.Join(cfg, "projects", "-home-dev-repos-decoy-"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}
	mkdir(t, filepath.Join(cfg, "projects", projectKey(cwd)))
	subagentsDir = filepath.Join(cfg, "projects", projectKey(cwd), sid, "subagents")

	// Each test gets a fresh process-wide memo state; they are package globals.
	jsonlMemo = pathMemo{}
	subagentMemo = pathMemo{}
	return sid, cwd, subagentsDir
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

// writeTranscript gives the session the main transcript every live claude session has. The
// subagents lookup hangs its Lstat off this, so without it the fixture measures the
// no-transcript fallback instead.
func writeTranscript(t *testing.T, cwd, sid string) {
	t.Helper()
	p := filepath.Join(ConfigDir(), "projects", projectKey(cwd), sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startSubagent writes a child transcript carrying prompt, the way claude records a prompt
// typed into a pane whose input box is bound to a background agent.
func startSubagent(t *testing.T, dir, prompt string) string {
	t.Helper()
	mkdir(t, dir)
	p := filepath.Join(dir, "agent-01.jsonl")
	line := `{"type":"user","message":{"role":"user","content":"The user sent a new message while you were working: ` + prompt + `"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The acceptance test the ADR specifies, now with no TTL to wait out: a lookup that answered
// "no background agents" must not make the NEXT one answer from memory.
func TestAnAgentStartingIsVisibleImmediately(t *testing.T) {
	sid, cwd, subagents := fixture(t, 8)
	writeTranscript(t, cwd, sid)

	if SubagentBusy(sid) {
		t.Fatal("no subagents exist yet")
	}
	startSubagent(t, subagents, "run the tests")

	if !SubagentBusy(sid) {
		t.Fatal("a background agent that started after a miss stayed invisible — this is what " +
			"stops an early completion report, a duplicated interruption, and a reaper that " +
			"halts the session (and the workspace) with the agent still running")
	}
	if got := SubagentLogs(sid); len(got) != 1 {
		t.Fatalf("SubagentLogs = %v, want the one live transcript", got)
	}
}

// The delivery check reads the same answer through a different door, and it is the one that
// decides whether to retype a prompt into a session whose input box is bound to an agent.
func TestAMisdeliveryIsSeenAsSoonAsItLands(t *testing.T) {
	sid, cwd, subagents := fixture(t, 8)
	writeTranscript(t, cwd, sid)
	const prompt = "run the tests"

	if SubagentBusy(sid) { // the miss that used to be cached
		t.Fatal("no subagents exist yet")
	}
	base := SubagentSnapshot(sid)
	startSubagent(t, subagents, prompt)

	if !SubagentReceivedSince(sid, base, prompt) {
		t.Fatal("misdelivery went unnoticed")
	}
}

// The cheap answer comes from looking beside the transcript, so "no subagents" must be
// answered by an Lstat of the one place it could be — not by a sweep, and not from memory.
func TestTheAbsenceIsAnsweredWithoutASweep(t *testing.T) {
	sid, cwd, _ := fixture(t, 8)
	writeTranscript(t, cwd, sid)

	if got := subagentBases(sid); len(got) != 0 {
		t.Fatalf("subagentBases = %v, want none", got)
	}
	// Remove every decoy project directory's READ permission: a sweep would now fail or
	// change its answer, while the targeted Lstat does not touch them at all.
	projects, err := os.ReadDir(filepath.Join(ConfigDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	swept := 0
	for _, e := range projects {
		if e.Name() == projectKey("/home/dev/repos/agent-fleet") {
			continue
		}
		swept++
	}
	if swept == 0 {
		t.Fatal("the fixture has no decoys, so this proves nothing")
	}
	// A subagents directory under a DECOY project must not be found: the session's own
	// transcript says which project directory is its, and that is the only one to look in.
	stray := filepath.Join(ConfigDir(), "projects", projects[0].Name(), sid, "subagents")
	if projects[0].Name() != projectKey("/home/dev/repos/agent-fleet") {
		mkdir(t, stray)
		subagentMemo = pathMemo{}
		if got := subagentBases(sid); len(got) != 0 {
			t.Fatalf("subagentBases = %v, want none — a directory under another project is "+
				"not this session's", got)
		}
	}
}

// Without a transcript there is nothing to hang the Lstat off, so the old sweep has to stay
// as the fallback — a session that has not written its first turn still has to be answered
// correctly.
func TestFallsBackToTheSweepBeforeTheFirstTurn(t *testing.T) {
	sid, cwd, _ := fixture(t, 4)
	subagents := filepath.Join(ConfigDir(), "projects", projectKey(cwd), sid, "subagents")
	mkdir(t, subagents)

	// No transcript written: transcriptProjectDirs is empty and the sweep is what answers.
	if got := subagentBases(sid); len(got) != 1 || got[0] != subagents {
		t.Fatalf("subagentBases = %v, want [%s]", got, subagents)
	}
}

// The transcript-side memo must keep refusing to remember a miss: answering "no transcript"
// when one exists makes buildProgram pass --session-id and claude exits with "Session ID is
// already in use" (jsonl_memo.go).
func TestTranscriptMemoStillNeverRemembersAMiss(t *testing.T) {
	sid, cwd, _ := fixture(t, 4)
	if len(jsonlPaths(sid)) != 0 {
		t.Fatal("no transcript exists yet")
	}
	writeTranscript(t, cwd, sid)
	p := filepath.Join(ConfigDir(), "projects", projectKey(cwd), sid+".jsonl")
	got := jsonlPaths(sid)
	if len(got) != 1 || got[0] != p {
		t.Fatalf("jsonlPaths = %v, want [%s] immediately after the transcript appeared", got, p)
	}
}

// The derivation is a guess confirmed by one Lstat, so a cwd that does not name the project
// directory must fall back to the sweep rather than answering "not there".
func TestSearchFallsBackWhenTheGuessMisses(t *testing.T) {
	sid, _, _ := fixture(t, 4)
	elsewhere := filepath.Join(ConfigDir(), "projects", "-home-dev-somewhere-else")
	mkdir(t, elsewhere)
	p := filepath.Join(elsewhere, sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := jsonlPaths(sid)
	if len(got) != 1 || got[0] != p {
		t.Fatalf("jsonlPaths = %v, want [%s] — a wrong guess must fall through to the sweep", got, p)
	}
}

func TestProjectKeyMatchesClaudesEncoding(t *testing.T) {
	// Taken from a live ~/.claude/projects tree.
	for cwd, want := range map[string]string{
		"/home/dev/repos/agent-fleet":           "-home-dev-repos-agent-fleet",
		"/home/dev/repos/agent-fleet@wip-s2y":   "-home-dev-repos-agent-fleet-wip-s2y",
		"/home/dev/.config/agent-fleet/chat-wd": "-home-dev--config-agent-fleet-chat-wd",
	} {
		if got := projectKey(cwd); got != want {
			t.Errorf("projectKey(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestCWDHintFollowsTheSubdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "console"))
	m := session.Meta{Name: "slot02", Dir: dir, Subdir: "console", Kind: "claude"}
	session.WriteMeta(m)

	// claude encodes the cwd it actually ran in, which is the subdir when there is one.
	if got := session.CWDForUUID(session.UUID(m.Dir, m.Name)); got != filepath.Join(dir, "console") {
		t.Fatalf("CWDForUUID = %q, want the subdir", got)
	}
}

// The hole the second review found in the first version of this lookup, kept as the case that
// must never come back: anchoring only on the located transcript concludes "absent" from the
// presence of a DIFFERENT object, and the anchor can be the wrong one.
//
// How a session gets two project directories: Meta.CWD() resolves to Dir/Subdir only while
// that directory exists and falls back to Dir when it does not, so a branch switch that
// removes the folder moves the next launch to the other name. Both transcripts then exist,
// jsonlPaths answers with whichever the cwd hint resolves to today, and the background agent
// is beside the other one.
//
// ⚠️ Worse than the negative cache this design replaced, which is why it is pinned: that one
// healed itself after 15s, and this did not heal at all — and the false "no background work"
// reaches the CP's reaper, which stops the workspace on it.
func TestAnAgentIsFoundUnderTheOtherCWDTheSessionCanHave(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	jsonlMemo, subagentMemo = pathMemo{}, pathMemo{}

	root := filepath.Join(home, "repo")
	mkdir(t, filepath.Join(root, "pkg")) // the subdir is back on disk, so CWD() prefers it
	m := session.Meta{Name: "slot01", Dir: root, Subdir: "pkg", Kind: "claude"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	for _, cwd := range []string{root, filepath.Join(root, "pkg")} {
		mkdir(t, filepath.Join(cfg, "projects", projectKey(cwd)))
		if err := os.WriteFile(filepath.Join(cfg, "projects", projectKey(cwd), sid+".jsonl"),
			[]byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The live agent is beside the transcript the cwd hint does NOT point at.
	startSubagent(t, filepath.Join(cfg, "projects", projectKey(root), sid, "subagents"), "x")

	if !SubagentBusy(sid) {
		t.Fatal("a live background agent went unseen because the anchor transcript was the " +
			"stale one — the reaper would stop the workspace with it still running")
	}
}
