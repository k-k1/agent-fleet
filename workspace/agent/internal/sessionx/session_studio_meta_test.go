package sessionx

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
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
	binds := stubStudioBind(t, nil)

	created := env.createOK(map[string]any{"kind": "claude", "dir": repo, "studio": studioID, "initial_prompt": "persona"})
	if created.Studio != studioID || created.InitialPromptState != session.InitialPromptPending {
		t.Fatalf("wire studio/state = %q/%q, want the studio and pending", created.Studio, created.InitialPromptState)
	}
	m, ok := session.ReadMeta(created.Name)
	if !ok || m.Studio != studioID || m.InitialPromptState != session.InitialPromptPending {
		t.Fatalf("meta studio/state = %q/%q (ok=%v)", m.Studio, m.InitialPromptState, ok)
	}
	// The studio was pointed at the session BEFORE the session existed anywhere: no meta on
	// disk yet when the hook ran, so no launch and no first turn can have preceded it.
	if len(*binds) != 1 || (*binds)[0] != (studioBindCall{studioID, created.Name, "", false}) {
		t.Fatalf("binds = %+v, want one create-time bind with no meta written yet", *binds)
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
	last := (*binds)[len(*binds)-1]
	if last != (studioBindCall{studioID, recreated.Name, created.Name, false}) {
		t.Fatalf("recreate bind = %+v, want the studio moved from the old slot to the new one", last)
	}
}

// A recreate whose studio has moved on starts unbound rather than claiming a studio that does
// not name it back (every studio tool and generate_image would refuse it).
func TestRecreateDropsAStudioItCannotTakeOver(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "old1", Kind: session.KindClaude, Dir: repo, Studio: studioID})
	stubStudioBind(t, func(_, _, previous string) error {
		if previous != "" {
			return imagegen.ErrStudioBound
		}
		return nil
	})
	code, raw := roundtrip(t, env.srv, "POST", "/sessions/old1/recreate", nil)
	if code != http.StatusOK {
		t.Fatalf("recreate = %d %s", code, raw)
	}
	var recreated session.Session
	if err := json.Unmarshal(raw, &recreated); err != nil {
		t.Fatal(err)
	}
	if rm, _ := session.ReadMeta(recreated.Name); rm.Studio != "" || recreated.Studio != "" {
		t.Fatalf("recreate kept studio %q (wire %q) it could not take over", rm.Studio, recreated.Studio)
	}
}

// A create naming a studio is refused, not started half-bound, when the studio cannot be bound.
func TestCreateWithAStudioThatCannotBeBound(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		hook func(string, string, string) error
		nil_ bool
		code int
		want string
	}{
		{name: "no store", nil_: true, code: http.StatusNotImplemented, want: "studio_unavailable"},
		{name: "no such studio", hook: func(string, string, string) error { return imagegen.ErrStudioNotFound }, code: http.StatusNotFound, want: "no_studio"},
		{name: "bound elsewhere", hook: func(string, string, string) error { return imagegen.ErrStudioBound }, code: http.StatusConflict, want: "studio_bound"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.nil_ {
				old := imagegen.BindStudioSession
				imagegen.BindStudioSession = nil
				t.Cleanup(func() { imagegen.BindStudioSession = old })
			} else {
				stubStudioBind(t, c.hook)
			}
			before := len(session.ListMetas())
			code, raw := env.create(map[string]any{"kind": "claude", "dir": repo, "studio": studioID})
			if code != c.code || !strings.Contains(string(raw), c.want) {
				t.Fatalf("create = %d %s, want %d %s", code, raw, c.code, c.want)
			}
			if n := len(session.ListMetas()); n != before {
				t.Fatalf("a refused create left a session behind (%d metas, was %d)", n, before)
			}
		})
	}
}

type studioBindCall struct {
	studio, session, previous string
	metaOnDisk                bool
}

// stubStudioBind installs the studio store's bind hook for one test, recording every call and
// whether the session's meta already existed when it was made.
func stubStudioBind(t *testing.T, answer func(studio, session, previous string) error) *[]studioBindCall {
	t.Helper()
	calls := &[]studioBindCall{}
	old := imagegen.BindStudioSession
	imagegen.BindStudioSession = func(studio, name, previous string) error {
		_, onDisk := session.ReadMeta(name)
		*calls = append(*calls, studioBindCall{studio, name, previous, onDisk})
		if answer != nil {
			return answer(studio, name, previous)
		}
		return nil
	}
	t.Cleanup(func() { imagegen.BindStudioSession = old })
	return calls
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

// The rollbacks only run when a launch fails, so they are driven with a launch that fails: the
// studio is handed back with a CONDITIONAL move naming the slot that never started.
func TestStudioBindingIsHandedBackWhenTheLaunchFails(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := launchTmuxFn
	launchTmuxFn = func(session.Meta, bool) error { return errors.New("no tmux today") }
	t.Cleanup(func() { launchTmuxFn = orig })

	t.Run("create", func(t *testing.T) {
		binds := stubStudioBind(t, nil)
		code, raw := env.create(map[string]any{"kind": "claude", "dir": repo, "studio": studioID})
		if code != http.StatusInternalServerError {
			t.Fatalf("create = %d %s, want the launch failure", code, raw)
		}
		if len(*binds) != 2 || (*binds)[0].previous != "" || (*binds)[1].studio != studioID ||
			(*binds)[1].session != "" || (*binds)[1].previous != (*binds)[0].session {
			t.Fatalf("binds = %+v, want the bind and then (studio, \"\", that session)", *binds)
		}
	})
	t.Run("recreate", func(t *testing.T) {
		env.fixture(session.Meta{Name: "old2", Kind: session.KindClaude, Dir: repo, Studio: studioID})
		binds := stubStudioBind(t, nil)
		code, raw := roundtrip(t, env.srv, "POST", "/sessions/old2/recreate", nil)
		if code != http.StatusInternalServerError {
			t.Fatalf("recreate = %d %s, want the launch failure", code, raw)
		}
		if len(*binds) != 2 || (*binds)[0].previous != "old2" ||
			(*binds)[1] != (studioBindCall{studioID, "old2", (*binds)[0].session, true}) {
			t.Fatalf("binds = %+v, want the move to the new slot and then (studio, old2, new slot)", *binds)
		}
	})
}

// With no studio store there is nobody to move the studio to the new slot, so the new slot
// must not claim it.
func TestRecreateWithoutAStudioStoreStartsUnbound(t *testing.T) {
	env := spawnServer(t)
	repo := filepath.Join(env.home, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	env.fixture(session.Meta{Name: "old3", Kind: session.KindClaude, Dir: repo, Studio: studioID})
	old := imagegen.BindStudioSession
	imagegen.BindStudioSession = nil
	t.Cleanup(func() { imagegen.BindStudioSession = old })
	code, raw := roundtrip(t, env.srv, "POST", "/sessions/old3/recreate", nil)
	if code != http.StatusOK {
		t.Fatalf("recreate = %d %s", code, raw)
	}
	var recreated session.Session
	if err := json.Unmarshal(raw, &recreated); err != nil {
		t.Fatal(err)
	}
	if rm, _ := session.ReadMeta(recreated.Name); rm.Studio != "" {
		t.Fatalf("recreate with no store kept studio %q", rm.Studio)
	}
}
