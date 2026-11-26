package sessionx

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// wantDeleteRefusal fails unless err is a start refusal with the delete's code.
func wantDeleteRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var ref *StartRefusalError
	if !errors.As(err, &ref) || ref.Ref.Code != gitx.ErrCodeDeleteInProgress() || ref.Ref.Status != http.StatusConflict {
		t.Fatalf("%s: err = %v, want a 409 %s refusal", what, err, gitx.ErrCodeDeleteInProgress())
	}
}

// A create holds its folder from before the process starts until its meta is on disk, so a delete
// deciding at any moment in between sees it (#5122). The session runs at home with its cwd inside
// the folder, which a match on Meta.Dir alone would miss.
func TestCreateHoldsItsFolderUntilTheMetaIsPublished(t *testing.T) {
	p := newCreateProbe(t)
	folder := filepath.Join(p.home, "notes")
	if err := os.MkdirAll(filepath.Join(folder, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	launched := false
	launchTmuxFn = func(m session.Meta, _ bool) error {
		launched = true
		if !gitx.LaunchingUnder(folder) {
			t.Error("the folder is not held while the process starts")
		}
		if _, ok := session.ReadMeta(m.Name); ok {
			t.Error("the meta was published before the process started")
		}
		return nil
	}
	rec := p.post(map[string]any{"kind": "shell", "dir": p.home, "subdir": "notes/sub"}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if !launched {
		t.Fatal("the launch never ran")
	}
	if gitx.LaunchingUnder(folder) {
		t.Fatal("the create left its reservation behind")
	}
}

// A create naming a folder a delete has decided on is refused before anything starts, whatever
// the folder is: the space path's own check covers spaces only.
func TestCreateInAWorkingCopyBeingDeletedIsRefused(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error {
		t.Error("a create in a working copy being deleted launched")
		return nil
	}
	end, ok, why := gitx.BeginDelete(p.repo, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	before := len(session.ListMetas())
	rec := p.post(map[string]any{"kind": "shell", "dir": p.repo}, "")
	if rec.Code != http.StatusConflict || spaceErrCode(t, rec.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("create: %d %s, want 409 %s", rec.Code, rec.Body, gitx.ErrCodeDeleteInProgress())
	}
	if got := len(session.ListMetas()); got != before {
		t.Fatalf("the refused create wrote a meta: %d -> %d", before, got)
	}
	if gitx.LaunchingUnder(p.repo) {
		t.Fatal("a refused create left a reservation behind")
	}
}

// A new session's folder is read again under the gate: a delete that finished after the create
// read it has taken it away, and nothing is started in a folder that is gone.
func TestFreshLaunchIntoAFolderThatIsGoneIsRefused(t *testing.T) {
	home := writebackEnv(t)
	gone := filepath.Join(home, "gone")
	release, ref := reserveLaunch(session.Meta{Name: "fresh01", Dir: gone, Kind: session.KindShell}, launchFresh)
	if ref == nil || ref.Code != "bad_dir" {
		release()
		t.Fatalf("ref = %+v, want bad_dir", ref)
	}
	if gitx.LaunchingUnder(gone) {
		t.Fatal("a refused launch left a reservation behind")
	}
}

// A start of an existing session reads its meta again under the gate: a delete that finished
// since the caller read it has trashed the session (shell) or shelved it (AI), after moving its
// folder away, and neither is started.
func TestStartOfASessionADeleteSettledMeanwhileIsRefused(t *testing.T) {
	home := writebackEnv(t)
	snap := session.Meta{Name: "settled1", Dir: home, Kind: session.KindShell}
	session.WriteMeta(session.Meta{Name: "settled1", Dir: home, Kind: session.KindShell, Archived: true})
	if _, ref := reserveLaunch(snap, launchExisting); ref == nil {
		t.Fatal("a session shelved since it was read was started")
	}
	if err := os.Remove(session.MetaPath("settled1")); err != nil {
		t.Fatal(err)
	}
	if _, ref := reserveLaunch(snap, launchExisting); ref == nil {
		t.Fatal("a session trashed since it was read was started")
	}
	session.WriteMeta(snap)
	release, ref := reserveLaunch(snap, launchExisting)
	if ref != nil {
		t.Fatalf("an unchanged session was refused: %+v", ref)
	}
	release()
}

// /start and a Managed input start a stopped session; both hold its folder while the runtime
// starts, and both are refused, with nothing launched, while a delete of the folder runs.
func TestManagedStartsHoldTheFolderAndMeetTheDelete(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "mstart01"
	m := session.Meta{Name: name, Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged,
		StoppedAt: "2026-09-25T10:00:00+09:00"}
	session.WriteMeta(m)
	resumed := 0
	held := true
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() {
		resumed++
		held = held && gitx.LaunchingUnder(dir)
	}})

	end, ok, why := gitx.BeginDelete(dir, "")
	if !ok {
		t.Fatal(why)
	}
	wantDeleteRefusal(t, "/start", ensureSessionTmux(testLease(t, name), name, false))
	d, _ := driverOf(m)
	_, err := resumeManaged(d, m)
	wantDeleteRefusal(t, "managed input", err)
	if resumed != 0 {
		t.Fatalf("a start during the delete launched %d time(s)", resumed)
	}
	end()

	if err := ensureSessionTmux(testLease(t, name), name, false); err != nil {
		t.Fatalf("/start after the delete ended: %v", err)
	}
	if resumed != 1 || !held {
		t.Fatalf("resumed=%d held=%v, want one launch with the folder held", resumed, held)
	}
	if gitx.LaunchingUnder(dir) {
		t.Fatal("the start left its reservation behind")
	}
}

// A Terminal (CLI) start of a stopped session is refused while a delete of its folder runs. No
// tmux pane is involved: the refusal comes before the launch.
func TestTerminalStartIsRefusedWhileItsFolderIsBeingDeleted(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "tstart01"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindShell})
	end, ok, why := gitx.BeginDelete(dir, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	wantDeleteRefusal(t, "/start", ensureSessionTmux(testLease(t, name), name, false))
}

// A driver switch is a stop and a relaunch: refused before the stop while a delete of the folder
// runs, so the session is left as it was.
func TestDriverSwitchIsRefusedWhileItsFolderIsBeingDeleted(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "dswitch1"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex})
	resumed := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { resumed = true }})
	end, ok, why := gitx.BeginDelete(dir, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	w := callHandler(HandleSessionDriver, http.MethodPost, name, `{"driver":"managed"}`)
	if w.Code != http.StatusConflict || spaceErrCode(t, w.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("driver switch = %d %s, want 409 %s", w.Code, w.Body, gitx.ErrCodeDeleteInProgress())
	}
	if resumed {
		t.Fatal("the switch relaunched the session")
	}
	if m, _ := session.ReadMeta(name); m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver = %q, want the old one", m.Driver)
	}
}
