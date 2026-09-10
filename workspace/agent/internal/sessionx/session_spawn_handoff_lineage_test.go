package sessionx

// The lineage a HANDOFF PROPOSAL leaves behind (ADR 0073 decision 1, amendment 2026-09-10):
// a session the user launched from another session's proposal keeps origin=user and gains
// origin_session.
//
// These go through the REAL create and fork handlers. A unit test of the predicate cannot see
// the two failures this feature is actually made of — a lineage nothing writes, and a lineage
// that quietly costs the launched session its ability to spawn.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// handoffLaunchBody is the create the Console sends for a launch seeded by a proposal:
// no origin field (so it resolves to user) plus the pair that names the proposal.
func handoffLaunchBody(dir, from, proposalID string) map[string]any {
	return map[string]any{
		"kind": "claude", "dir": dir, "worktree": false,
		"origin_session": from, "origin_proposal": proposalID,
	}
}

// createSession POSTs a create and returns the created session, failing the test otherwise.
func (e *spawnEnv) createOK(body map[string]any) session.Session {
	e.t.Helper()
	code, raw := e.create(body)
	if code != http.StatusCreated {
		e.t.Fatalf("create = %d %s, want 201", code, raw)
	}
	var s session.Session
	if err := json.Unmarshal(raw, &s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// plantConversation gives a session a claude conversation log with a real turn in it, which is
// what ForkSource demands before it will hand out a fork source.
func (e *spawnEnv) plantConversation(m session.Meta) {
	e.t.Helper()
	dir := filepath.Join(e.home, ".claude", "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl"), []byte(line), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// fork forks name through the real handler and returns the successor.
func (e *spawnEnv) fork(name string) session.Session {
	e.t.Helper()
	code, raw := roundtrip(e.t, e.srv, "POST", "/sessions/"+name+"/fork", map[string]any{})
	if code != http.StatusCreated {
		e.t.Fatalf("fork %s = %d %s, want 201", name, code, raw)
	}
	var s session.Session
	if err := json.Unmarshal(raw, &s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// The route test the unit tests cannot stand in for: a proposal, a Console launch that names it,
// and the provenance that lands on disk. "The function exists but nothing calls it" is invisible
// from below, and the whole point of this change is a value the create path has to write.
func TestCreateFromHandoffProposalRecordsLineage(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "proposer", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginUser, CreatedAt: "2026-09-10T10:00:00+09:00"})
	p, err := AddHandoffProposal("proposer", "carry on with the migration", "Migration part 2")
	if err != nil {
		t.Fatal(err)
	}

	// Watch the delivery too: a proposal launch is a person's launch, so it must not pick up
	// the spawn envelope (which tells the agent a SESSION gave it this instruction).
	got := make(chan string, 4)
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(_, prompt string) { got <- prompt }
	t.Cleanup(func() { deliverInitialPromptFn = orig })

	body := handoffLaunchBody(repo, "proposer", p.ID)
	body["initial_prompt"] = "carry on with the migration"
	created := env.createOK(body)

	if created.Origin != session.OriginUser {
		t.Fatalf("wire origin = %q, want %q — a person opened this session and the accounting axis must not move",
			created.Origin, session.OriginUser)
	}
	if created.OriginSession != "proposer" {
		t.Fatalf("wire originSession = %q, want %q", created.OriginSession, "proposer")
	}
	m, ok := session.ReadMeta(created.Name)
	if !ok || m.Origin != session.OriginUser || m.OriginSession != "proposer" {
		t.Fatalf("meta origin = %q/%q (ok=%v), want user/proposer", m.Origin, m.OriginSession, ok)
	}
	// It is not a child: no slot spent, and nothing for the proposing session to steer.
	if n := countChildren("proposer"); n != 0 {
		t.Fatalf("children of the proposing session = %d, want 0 (a proposal launch is not a spawn)", n)
	}
	if d := <-got; strings.HasPrefix(d, "[agent-fleet:spawn ") {
		t.Fatalf("delivered task carries the spawn envelope: %q", d)
	}
}

// The lineage is PROVED, not believed. Everywhere else origin_session comes from the MCP
// server's own $AF_SESSION_NAME; this pair arrives from a browser, so a create that cannot
// point at a real proposal on the named session's file records nothing.
func TestCreateHandoffLineageRefusesForgery(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"proposer", "stranger"} {
		env.fixture(session.Meta{Name: n, Kind: session.KindClaude, Dir: repo,
			Origin: session.OriginUser, CreatedAt: "2026-09-10T10:00:00+09:00"})
	}
	// Only `stranger` really proposed anything.
	strangers, err := AddHandoffProposal("stranger", "someone else's work", "Elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(string, string) {}
	t.Cleanup(func() { deliverInitialPromptFn = orig })

	for _, tc := range []struct {
		name       string
		from, prop string
	}{
		{"an id nobody proposed", "proposer", "hp_deadbeefdeadbeef"},
		{"another session's proposal id", "proposer", strangers.ID},
		{"a session with no proposal file at all", "ghost", "hp_deadbeefdeadbeef"},
		{"a name that is not a session name", "../../etc", strangers.ID},
		{"a lineage claimed with no proposal", "stranger", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := env.createOK(handoffLaunchBody(repo, tc.from, tc.prop))
			if created.OriginSession != "" {
				t.Fatalf("originSession = %q, want empty — an unproven lineage must not be recorded", created.OriginSession)
			}
			if m, ok := session.ReadMeta(created.Name); !ok || m.OriginSession != "" {
				t.Fatalf("meta originSession = %q (ok=%v), want empty", m.OriginSession, ok)
			}
		})
	}
}

// The regression this change is most likely to cause: origin_session now has a producer that is
// NOT an unattended chain, so a depth rule reading the bare field would take spawning away from
// the session a person launched from a proposal — and from its fork.
//
// The reverse has to stay fixed in the same test, or "nobody may spawn" and "the right ones may"
// look identical: a real child, and the fork of a real child, are still refused.
func TestSpawnDepthAcrossHandoffLineage(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(string, string) {}
	t.Cleanup(func() { deliverInitialPromptFn = orig })
	// The working-copy guard has its own test and would otherwise refuse every spawn below for
	// an unrelated reason (each one aims at the same fixture repo).
	aliveOrig := sessionAliveFn
	sessionAliveFn = func(session.Meta) bool { return false }
	t.Cleanup(func() { sessionAliveFn = aliveOrig })

	env.fixture(session.Meta{Name: "proposer", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginUser, CreatedAt: "2026-09-10T10:00:00+09:00"})
	p, err := AddHandoffProposal("proposer", "carry on", "Part 2")
	if err != nil {
		t.Fatal(err)
	}
	// The session a person launched from that proposal: origin=user WITH a lineage.
	launched := env.createOK(handoffLaunchBody(repo, "proposer", p.ID))
	if launched.OriginSession != "proposer" {
		t.Fatalf("precondition: launched session has no lineage (%q)", launched.OriginSession)
	}
	env.plantConversation(session.Meta{Name: launched.Name, Dir: repo})
	launchedFork := env.fork(launched.Name)
	// The fork must not INHERIT the lineage — origin=handoff plus a lineage reads as an
	// unattended chain, and that is how the fork would lose a capability its source has.
	if m, ok := session.ReadMeta(launchedFork.Name); !ok || m.OriginSession != "" {
		t.Fatalf("fork of a proposal launch inherited lineage %q (ok=%v); its origin is handoff, so that reads as unattended",
			m.OriginSession, ok)
	}

	// A real child, and its fork, for the other direction.
	env.fixture(session.Meta{Name: "parent1", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginUser, CreatedAt: "2026-09-10T10:00:00+09:00"})
	env.fixture(session.Meta{Name: "kid", Kind: session.KindClaude, Dir: repo,
		Origin: session.OriginSession, OriginSession: "parent1", CreatedAt: "2026-09-10T10:00:00+09:00"})
	env.plantConversation(session.Meta{Name: "kid", Dir: repo})
	kidFork := env.fork("kid")
	if m, ok := session.ReadMeta(kidFork.Name); !ok || m.OriginSession != "parent1" {
		t.Fatalf("fork of a child dropped the lineage: %q (ok=%v)", m.OriginSession, ok)
	}

	for _, tc := range []struct {
		name, caller string
		refused      bool
	}{
		{"a session launched from a proposal may still spawn", launched.Name, false},
		{"and so may its fork", launchedFork.Name, false},
		{"a child may not", "kid", true},
		{"nor may a child's fork", kidFork.Name, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := env.create(spawnBody(map[string]any{
				"dir": repo, "worktree": false, "origin_session": tc.caller,
			}))
			if tc.refused {
				if code != http.StatusConflict || !strings.Contains(string(raw), "spawn_depth") {
					t.Fatalf("= %d %s, want 409 spawn_depth", code, raw)
				}
				return
			}
			if code != http.StatusCreated {
				t.Fatalf("= %d %s, want 201", code, raw)
			}
		})
	}
}
