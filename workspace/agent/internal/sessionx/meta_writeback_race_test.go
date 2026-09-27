package sessionx

// Issue #950: the slow handlers read a meta, spend seconds in a launch / kill / driver RPC, then
// write. A lock set during that step must still be on disk afterwards. Each test sets the lock
// from INSIDE the slow step (through the seam the step already has), which is the only moment
// the old write-back of the snapshot could undo it.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// lockOnDisk is POST /lock landing mid-handler.
func lockOnDisk(t *testing.T, name string) {
	t.Helper()
	if _, ok := UpdateSessionMeta(name, func(m *session.Meta) bool { m.Locked = true; return true }); !ok {
		t.Fatalf("lockOnDisk: no meta for %s", name)
	}
}

func assertLocked(t *testing.T, name string) session.Meta {
	t.Helper()
	m, ok := session.ReadMeta(name)
	if !ok {
		t.Fatalf("meta %s is gone", name)
	}
	if !m.Locked {
		t.Fatalf("the lock set mid-handler was rolled back: %+v", m)
	}
	return m
}

func writebackEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

// lockingHandle / lockingDriver run a hook inside Resume and UpdateSettings — the steps that
// take seconds against a real runtime.
type lockingHandle struct {
	agents.ThreadHandle
	onUpdate func()
}

func (h *lockingHandle) UpdateSettings(agents.ThreadSettings) error {
	h.onUpdate()
	return nil
}

func (h *lockingHandle) Snapshot() (agents.ThreadSnapshot, error) {
	return agents.ThreadSnapshot{}, nil
}

type lockingDriver struct {
	agents.Driver
	onResume  func()
	onUpdate  func()
	resumeErr error
}

func (d *lockingDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	if d.onResume != nil {
		d.onResume()
	}
	if d.resumeErr != nil {
		return nil, d.resumeErr
	}
	return &lockingHandle{onUpdate: func() {
		if d.onUpdate != nil {
			d.onUpdate()
		}
	}}, nil
}

func useLockingDriver(t *testing.T, kind string, d *lockingDriver) {
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

func callHandler(h http.HandlerFunc, method, name, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/sessions/"+name, strings.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// POST /settings: the meta is read before the driver's UpdateSettings RPC.
func TestSettingsUpdateKeepsALockSetDuringTheRPC(t *testing.T) {
	home := writebackEnv(t)
	const name = "setlock1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged})
	useLockingDriver(t, session.KindCodex, &lockingDriver{onUpdate: func() { lockOnDisk(t, name) }})

	if w := callHandler(HandleSessionSettings, http.MethodPost, name, `{"effort":"high"}`); w.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", w.Code, w.Body)
	}
	if m := assertLocked(t, name); m.Effort != "high" {
		t.Fatalf("effort = %q, want high", m.Effort)
	}
}

// /start of a stopped managed session: StoppedAt is cleared after the runtime launch.
func TestManagedResumeKeepsALockSetDuringTheLaunch(t *testing.T) {
	home := writebackEnv(t)
	const name = "reslock1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
		StoppedAt: "2026-09-25T10:00:00+09:00"})
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { lockOnDisk(t, name) }})

	if err := ensureSessionTmux(name, false); err != nil {
		t.Fatal(err)
	}
	if m := assertLocked(t, name); m.StoppedAt != "" {
		t.Fatalf("StoppedAt = %q, want cleared by the resume", m.StoppedAt)
	}
}

// POST /driver: the switch is written after the old runtime is stopped and the new one launched.
func TestDriverSwitchKeepsALockSetDuringTheRelaunch(t *testing.T) {
	home := writebackEnv(t)
	const name = "drvlock1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex})
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { lockOnDisk(t, name) }})

	if w := callHandler(HandleSessionDriver, http.MethodPost, name, `{"driver":"managed"}`); w.Code != http.StatusOK {
		t.Fatalf("driver switch = %d %s", w.Code, w.Body)
	}
	if m := assertLocked(t, name); m.DriverKind() != session.DriverManaged {
		t.Fatalf("driver = %q, want managed", m.Driver)
	}
}

// A failed switch leaves the old driver on disk — and the lock.
func TestFailedDriverSwitchKeepsALockSetDuringTheRelaunch(t *testing.T) {
	home := writebackEnv(t)
	const name = "drvlock2"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex})
	useLockingDriver(t, session.KindCodex, &lockingDriver{
		onResume: func() { lockOnDisk(t, name) }, resumeErr: errors.New("no runtime today")})

	if w := callHandler(HandleSessionDriver, http.MethodPost, name, `{"driver":"managed"}`); w.Code == http.StatusOK {
		t.Fatalf("driver switch = 200, want the launch failure")
	}
	if m := assertLocked(t, name); m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver = %q, want the old tui", m.Driver)
	}
}

// A delete landing during the relaunch: the switch must not answer 200 for a session that is
// gone, nor write it back.
func TestDriverSwitchIntoADeletedSessionIsNotFound(t *testing.T) {
	home := writebackEnv(t)
	const name = "drvgone1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex})
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { session.RemoveMeta(name) }})

	if w := callHandler(HandleSessionDriver, http.MethodPost, name, `{"driver":"managed"}`); w.Code != http.StatusNotFound {
		t.Fatalf("driver switch = %d %s, want 404", w.Code, w.Body)
	}
	if _, ok := session.ReadMeta(name); ok {
		t.Fatal("the switch wrote a deleted session back")
	}
}

// POST /recreate whose successor fails to launch: the old slot is un-archived after the launch.
func TestFailedRecreateKeepsALockSetDuringTheLaunch(t *testing.T) {
	home := writebackEnv(t)
	const name = "reclock1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	orig := launchTmuxFn
	launchTmuxFn = func(session.Meta, bool) error {
		lockOnDisk(t, name)
		return errors.New("no tmux today")
	}
	t.Cleanup(func() { launchTmuxFn = orig })

	if w := callHandler(HandleRecreateSession, http.MethodPost, name, ""); w.Code == http.StatusOK {
		t.Fatalf("recreate = 200, want the launch failure")
	}
	if m := assertLocked(t, name); m.Archived {
		t.Fatal("the old slot stayed archived after its successor failed to launch")
	}
}

// A halt is written after the kill; the lock set during it survives, and the arm is consumed.
func TestHaltKeepsALockSetDuringTheKill(t *testing.T) {
	home := writebackEnv(t)
	const name = "haltlock1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	snapshot, _ := session.ReadMeta(name)
	lockOnDisk(t, name)
	if _, ok := setStopArm(name, true); !ok {
		t.Fatal("arm")
	}

	stampHalted(snapshot)
	if m := assertLocked(t, name); m.StoppedAt == "" || m.StopAfterTurnAt != "" {
		t.Fatalf("meta = %+v, want StoppedAt written and the arm consumed", m)
	}
}
