package sessionx

import (
	"net/http"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// recordingDriver is a managed driver that remembers every meta it was asked to resume.
type recordingDriver struct {
	agents.Driver
	mu    sync.Mutex
	metas []session.Meta
}

func (d *recordingDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	d.mu.Lock()
	d.metas = append(d.metas, m)
	d.mu.Unlock()
	return &lockingHandle{onUpdate: func() {}}, nil
}

func useDriver(t *testing.T, kind string, d agents.Driver) {
	t.Helper()
	prev, had := managedDrivers[kind]
	managedDrivers[kind] = d
	t.Cleanup(func() {
		if had {
			managedDrivers[kind] = prev
			return
		}
		delete(managedDrivers, kind)
	})
}

func (d *recordingDriver) last(t *testing.T) session.Meta {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.metas) == 0 {
		t.Fatal("the driver was never asked to start the session")
	}
	return d.metas[len(d.metas)-1]
}

// A muse session records the member's contributor setting at create, and a recreate of it keeps
// what was recorded even after the setting changed (#2535). The driver passes the recorded value
// as --contributor-mode on every spawn (muse/contributor_test.go), so this is what makes the mode
// survive a stop and start.
func TestMuseSessionRecordsContributorModeAtCreateAndKeepsIt(t *testing.T) {
	for _, tc := range []struct {
		name, prefs, flipped string
		want                 bool
	}{
		{"setting on", `{"museContributorMode":true}`, `{"museContributorMode":false}`, true},
		{"setting off", `{"museContributorMode":false}`, `{"museContributorMode":true}`, false},
		{"setting missing", `{}`, `{"museContributorMode":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateAgentState(t)
			writeUIPrefs(t, tc.prefs)
			d := &recordingDriver{}
			useDriver(t, session.KindMuse, d)
			srv := lcppTestServer(t)

			var created session.Session
			do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "muse", "model": "muse-spark-1.3"}, http.StatusCreated, &created)
			m, ok := session.ReadMeta(created.Name)
			if !ok {
				t.Fatal("meta not persisted")
			}
			if m.MuseContributorMode != tc.want {
				t.Fatalf("recorded MuseContributorMode = %v, want %v", m.MuseContributorMode, tc.want)
			}
			if got := d.last(t).MuseContributorMode; got != tc.want {
				t.Fatalf("the driver started the session with MuseContributorMode = %v, want %v", got, tc.want)
			}

			// The member changes the setting; the existing session's slot keeps its own mode.
			writeUIPrefs(t, tc.flipped)
			var recreated session.Session
			do(t, srv, "POST", "/sessions/"+created.Name+"/recreate", map[string]any{}, http.StatusOK, &recreated)
			rm, ok := session.ReadMeta(recreated.Name)
			if !ok {
				t.Fatal("recreated meta not persisted")
			}
			if rm.MuseContributorMode != tc.want {
				t.Fatalf("recreate recorded MuseContributorMode = %v, want the original %v", rm.MuseContributorMode, tc.want)
			}
		})
	}
}

// The request cannot set it: create_session is callable by a session, and switching the mode
// on is the member's decision.
func TestMuseContributorModeIsNotTakenFromTheCreateRequest(t *testing.T) {
	isolateAgentState(t)
	writeUIPrefs(t, `{}`)
	d := &recordingDriver{}
	useDriver(t, session.KindMuse, d)
	srv := lcppTestServer(t)

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "muse", "model": "muse-spark-1.3",
		"museContributorMode": true, "contributorMode": true, "contributor_mode": true}, http.StatusCreated, &created)
	if m, _ := session.ReadMeta(created.Name); m.MuseContributorMode {
		t.Fatal("a create request switched contributor mode on")
	}
}

// Only muse records it: the setting means nothing to another kind's CLI.
func TestOtherKindsNeverRecordMuseContributorMode(t *testing.T) {
	isolateAgentState(t)
	writeUIPrefs(t, `{"museContributorMode":true}`)
	d := &recordingDriver{}
	useDriver(t, session.KindCodex, d)
	srv := lcppTestServer(t)

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{"dir": t.TempDir(), "kind": "codex", "driver": "managed"}, http.StatusCreated, &created)
	if m, _ := session.ReadMeta(created.Name); m.MuseContributorMode {
		t.Fatal("a codex session recorded the muse contributor mode")
	}
}

func TestForkMetaKeepsMuseContributorMode(t *testing.T) {
	for _, on := range []bool{true, false} {
		src := metaFor("sfmuse01")
		src.Kind = session.KindMuse
		src.MuseContributorMode = on
		if got := forkMeta(src, "sfmuse02", "", "", "").MuseContributorMode; got != on {
			t.Errorf("fork of a session with mode %v recorded %v", on, got)
		}
	}
}
