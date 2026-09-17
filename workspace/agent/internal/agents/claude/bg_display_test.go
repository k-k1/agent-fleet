package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The acceptance test ADR 0087 decision 5 asks for: prime the display path's negative cache
// with a miss, then create the child transcript INSIDE its TTL, and check that every
// decision still sees it. A negative cache that leaked into those three would mean a
// duplicated interruption fired at a background agent, a completion reported while one is
// still working, or a session killed with one still running.

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
	subagentAbsent = absenceMemo{}
	return sid, cwd, subagentsDir
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
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

func TestDisplayCacheDoesNotHideAMisdelivery(t *testing.T) {
	sid, _, subagents := fixture(t, 8)
	const prompt = "run the tests"

	// The badge misses and caches the absence…
	if SubagentBusyDisplay(sid) {
		t.Fatal("no subagents exist yet")
	}
	// …the delivery baseline is taken, the prompt is typed, and claude puts it into the
	// background agent's transcript instead of the session's own.
	base := SubagentSnapshot(sid)
	startSubagent(t, subagents, prompt)

	if SubagentBusyDisplay(sid) {
		t.Fatal("the badge is expected to still be stale here — the test below is what matters")
	}
	// (1) Delivery confirmation must see it, or confirmPromptDelivery retypes the prompt and
	// fires the same interruption into the agent a second time.
	if !SubagentReceivedSince(sid, base, prompt) {
		t.Fatal("misdelivery went unnoticed while the display cache was warm")
	}
}

func TestDisplayCacheDoesNotHideARunningAgentFromTheDecisions(t *testing.T) {
	sid, _, subagents := fixture(t, 8)

	if SubagentBusyDisplay(sid) {
		t.Fatal("no subagents exist yet")
	}
	startSubagent(t, subagents, "anything")

	// (2) and (3): completion (chatx.collectReportSignals) and the armed stop
	// (chatx.stop_after_turn, through the same signals) both read SubagentBusy. It searches,
	// so it sees the agent that appeared a moment ago.
	if !SubagentBusy(sid) {
		t.Fatal("SubagentBusy missed a live background agent — a report would be sent early " +
			"and an armed stop would kill the session with the agent still running")
	}
	if len(SubagentLogs(sid)) != 1 {
		t.Fatalf("SubagentLogs = %v, want the one live transcript", SubagentLogs(sid))
	}
	// The badge catches up once the TTL is behind it. Rather than sleeping 15s, age the
	// entry: the point is that the cache expires, not how the clock is read.
	subagentAbsent.note(ConfigDir()+"\x00"+sid, true)
	subagentAbsent.seen[ConfigDir()+"\x00"+sid] = time.Now().Add(-subagentAbsentTTL - time.Second)
	if !SubagentBusyDisplay(sid) {
		t.Fatal("the badge never caught up after the TTL")
	}
}

// Note what this does NOT say: agents appearing does not clear the cache by itself. While the
// entry is fresh, subagentBasesDisplay returns early and never looks, so the absence is
// dropped only by the first lookup AFTER the TTL — which is the whole cost of the cache, and
// is what TestDisplayCacheDoesNotHideARunningAgentFromTheDecisions pins.
func TestDisplayCacheIsDroppedByTheFirstLookupThatFindsAgents(t *testing.T) {
	sid, _, subagents := fixture(t, 4)
	key := ConfigDir() + "\x00" + sid

	SubagentBusyDisplay(sid) // miss → remembered
	if !subagentAbsent.fresh(key) {
		t.Fatal("a miss was not remembered — the display path would sweep on every poll")
	}
	startSubagent(t, subagents, "x")

	// Age the entry past the TTL, so the next display lookup actually searches.
	subagentAbsent.seen[key] = time.Now().Add(-subagentAbsentTTL - time.Second)
	if !SubagentBusyDisplay(sid) {
		t.Fatal("the lookup after the TTL did not find the agents")
	}
	if subagentAbsent.fresh(key) {
		t.Fatal("the absence outlived the directory it described")
	}
}

// The transcript-side memo must keep refusing to remember a miss: answering "no transcript"
// when one exists makes buildProgram pass --session-id and claude exits with "Session ID is
// already in use" (jsonl_memo.go). The display cache must not have spread to it.
func TestTranscriptMemoStillNeverRemembersAMiss(t *testing.T) {
	sid, cwd, _ := fixture(t, 4)
	if len(jsonlPaths(sid)) != 0 {
		t.Fatal("no transcript exists yet")
	}
	p := filepath.Join(ConfigDir(), "projects", projectKey(cwd), sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
