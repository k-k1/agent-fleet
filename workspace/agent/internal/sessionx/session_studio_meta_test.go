package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const studioID = "0b9d1f2e-7c4a-4e1b-9a3d-5f6e7a8b9c0d"

// The studio binding and the initial-prompt state ride the meta from the create, reach the
// wire, and follow ADR 0100 decision 2's rules: fork drops the studio, recreate keeps it, a
// session starting a session may not bind one.
func TestCreateCarriesStudioAndInitialPromptState(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := deliverInitialPromptFn
	deliverInitialPromptFn = func(string, string) {} // never settles: the state stays pending
	t.Cleanup(func() { deliverInitialPromptFn = orig })

	created := env.createOK(map[string]any{"kind": "claude", "dir": repo, "studio": studioID, "initial_prompt": "persona"})
	if created.Studio != studioID || created.InitialPromptState != session.InitialPromptPending {
		t.Fatalf("wire studio/state = %q/%q, want the studio and pending", created.Studio, created.InitialPromptState)
	}
	m, ok := session.ReadMeta(created.Name)
	if !ok || m.Studio != studioID || m.InitialPromptState != session.InitialPromptPending {
		t.Fatalf("meta studio/state = %q/%q (ok=%v)", m.Studio, m.InitialPromptState, ok)
	}

	plain := env.createOK(map[string]any{"kind": "claude", "dir": repo})
	if plain.Studio != "" || plain.InitialPromptState != "" {
		t.Fatalf("a create with neither has studio/state %q/%q", plain.Studio, plain.InitialPromptState)
	}

	env.plantConversation(session.Meta{Name: created.Name, Dir: repo})
	fork := env.fork(created.Name)
	if fm, _ := session.ReadMeta(fork.Name); fm.Studio != "" || fork.Studio != "" {
		t.Fatalf("fork inherited the studio (%q / wire %q): one studio has one session", fm.Studio, fork.Studio)
	}

	code, raw := roundtrip(t, env.srv, "POST", "/sessions/"+created.Name+"/recreate", nil)
	if code != http.StatusOK {
		t.Fatalf("recreate = %d %s", code, raw)
	}
	var recreated session.Session
	if err := json.Unmarshal(raw, &recreated); err != nil {
		t.Fatal(err)
	}
	if rm, _ := session.ReadMeta(recreated.Name); rm.Studio != studioID || rm.InitialPromptState != "" {
		t.Fatalf("recreate studio/state = %q/%q, want the studio kept and no initial prompt", rm.Studio, rm.InitialPromptState)
	}
}

func TestSpawnedCreateMayNotBindAStudio(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	code, raw := env.create(spawnBody(map[string]any{"dir": repo, "worktree": false, "studio": studioID}))
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "bad_studio") {
		t.Fatalf("spawned create with a studio = %d %s, want 400 bad_studio", code, raw)
	}
}

func TestInitialPromptStateSettlesOnlyFromPending(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "a", InitialPromptState: session.InitialPromptPending})
	session.WriteMeta(session.Meta{Name: "b"})
	session.WriteMeta(session.Meta{Name: "c", InitialPromptState: session.InitialPromptDelivered})
	for _, n := range []string{"a", "b", "c"} {
		settleInitialPrompt(n, session.InitialPromptFailed)
	}
	for n, want := range map[string]string{"a": session.InitialPromptFailed, "b": "", "c": session.InitialPromptDelivered} {
		if m, _ := session.ReadMeta(n); m.InitialPromptState != want {
			t.Errorf("%s state = %q, want %q", n, m.InitialPromptState, want)
		}
	}
}

func TestRecoverPendingInitialPromptsAtStart(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "a", InitialPromptState: session.InitialPromptPending})
	session.WriteMeta(session.Meta{Name: "b", InitialPromptState: session.InitialPromptDelivered})
	if n := RecoverPendingInitialPrompts(); n != 1 {
		t.Fatalf("recovered %d, want 1", n)
	}
	if m, _ := session.ReadMeta("a"); m.InitialPromptState != session.InitialPromptUnknown {
		t.Fatalf("a = %q, want unknown", m.InitialPromptState)
	}
	if m, _ := session.ReadMeta("b"); m.InitialPromptState != session.InitialPromptDelivered {
		t.Fatalf("b = %q, want delivered untouched", m.InitialPromptState)
	}
}

// The list writes StoppedAt from a snapshot it read seconds earlier; the delivery goroutine or a
// bind may have written in between, and the snapshot must not roll that back.
func TestListSnapshotKeepsStudioAndInitialPromptState(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	stale := session.Meta{Name: "a", InitialPromptState: session.InitialPromptPending}
	session.WriteMeta(session.Meta{Name: "a", InitialPromptState: session.InitialPromptDelivered, Studio: studioID})
	stale.StoppedAt = "2026-09-23T10:00:00+09:00"
	WriteSessionMetaKeepingLock(stale)
	m, _ := session.ReadMeta("a")
	if m.InitialPromptState != session.InitialPromptDelivered || m.Studio != studioID || m.StoppedAt == "" {
		t.Fatalf("meta = %+v, want the newer state and studio kept and StoppedAt written", m)
	}
}
