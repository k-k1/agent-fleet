package sessionx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// A new session's folder, and its Subdir, are read again under the gate: a delete that finished
// after the create read them has taken them away, and nothing is started in a folder that is
// gone (Meta.CWD would fall back to Dir for a missing Subdir, and tmux to home for a missing Dir).
func TestFreshLaunchIntoAFolderThatIsGoneIsRefused(t *testing.T) {
	home := writebackEnv(t)
	gone := filepath.Join(home, "gone")
	for _, m := range []session.Meta{
		{Name: "fresh01", Dir: gone, Kind: session.KindShell},
		{Name: "fresh02", Dir: home, Subdir: "gone/sub", Kind: session.KindShell},
	} {
		release, ref := reserveLaunch(m, nil)
		if ref == nil || ref.Code != "bad_dir" {
			if release != nil {
				release()
			}
			t.Fatalf("%s: ref = %+v, want bad_dir", m.Name, ref)
		}
	}
	if gitx.LaunchingUnder(gone) {
		t.Fatal("a refused launch left a reservation behind")
	}
}

// A launch whose Subdir lies in a folder a delete has marked is refused even once the folder has
// been moved away: the Subdir is matched as stored, not through Meta.CWD's fallback to Dir.
func TestLaunchIntoAMovedSubdirMeetsTheMark(t *testing.T) {
	home := writebackEnv(t)
	space := filepath.Join(home, "spaces", "notes")
	end, ok, why := gitx.BeginDelete(space, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	m := session.Meta{Name: "subd01", Dir: home, Subdir: "spaces/notes/sub", Kind: session.KindShell}
	session.WriteMeta(m)
	if _, ref := reserveLaunch(m, &m); ref == nil || ref.Code != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("ref = %+v, want %s", ref, gitx.ErrCodeDeleteInProgress())
	}
}

// A release waits for the deletion gate. A delete reads the reservations and then the running
// sessions inside the gate; a release between the two reads would hide a session that has just
// been published from both.
func TestReleaseWaitsForTheDeletionGate(t *testing.T) {
	home := writebackEnv(t)
	m := session.Meta{Name: "rel01", Dir: home, Kind: session.KindShell}
	release, ref := reserveLaunch(m, nil)
	if ref != nil {
		t.Fatal(ref.Message)
	}
	done := make(chan struct{})
	WithDeletionGate(func() {
		go func() { release(); close(done) }()
		select {
		case <-done:
			t.Error("the reservation was released while a delete held the gate")
		case <-time.After(100 * time.Millisecond):
		}
		if !gitx.LaunchingUnder(home) {
			t.Error("the reservation is gone while a delete holds the gate")
		}
	})
	<-done
	if gitx.LaunchingUnder(home) {
		t.Fatal("the reservation outlived its release")
	}
}

// A start of an existing session reads its meta again under the gate: a delete that finished
// since the caller read it has trashed the session (shell) or shelved it (AI), after moving its
// folder away, and neither is started.
func TestStartOfASessionADeleteSettledMeanwhileIsRefused(t *testing.T) {
	home := writebackEnv(t)
	snap := session.Meta{Name: "settled1", Dir: home, Kind: session.KindShell}
	session.WriteMeta(session.Meta{Name: "settled1", Dir: home, Kind: session.KindShell, Archived: true})
	if _, ref := reserveLaunch(snap, &snap); ref == nil {
		t.Fatal("a session shelved since it was read was started")
	}
	if err := os.Remove(session.MetaPath("settled1")); err != nil {
		t.Fatal(err)
	}
	if _, ref := reserveLaunch(snap, &snap); ref == nil {
		t.Fatal("a session trashed since it was read was started")
	}
	// Read only after the delete was over: shelved, and its folder gone.
	gone := session.Meta{Name: "settled2", Dir: filepath.Join(home, "gone"), Kind: session.KindClaude, Archived: true}
	session.WriteMeta(gone)
	if _, ref := reserveLaunch(gone, &gone); ref == nil {
		t.Fatal("a shelved session whose folder is gone was started")
	}
	session.WriteMeta(snap)
	release, ref := reserveLaunch(snap, &snap)
	if ref != nil {
		t.Fatalf("an unchanged session was refused: %+v", ref)
	}
	release()
	// A slot whose folder was removed by hand still starts as on main (shell falls back to home):
	// only a delete's settlement refuses.
	byHand := session.Meta{Name: "byhand01", Dir: filepath.Join(home, "removed"), Kind: session.KindShell}
	session.WriteMeta(byHand)
	release, ref = reserveLaunch(byHand, &byHand)
	if ref != nil {
		t.Fatalf("a session whose folder was removed by hand was refused: %+v", ref)
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

// A recreate that meets a delete is refused before the old slot is stopped or archived, so the
// delete's guards and settlement still see it as it was.
func TestRecreateIsRefusedBeforeTouchingTheOldSlot(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "recr01"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindShell})
	end, ok, why := gitx.BeginDelete(dir, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	w := callHandler(HandleRecreateSession, http.MethodPost, name, "")
	if w.Code != http.StatusConflict || spaceErrCode(t, w.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("recreate = %d %s, want 409 %s", w.Code, w.Body, gitx.ErrCodeDeleteInProgress())
	}
	if m, _ := session.ReadMeta(name); m.Archived {
		t.Fatal("the refused recreate archived the old slot")
	}
	if n := len(session.ListMetas()); n != 1 {
		t.Fatalf("metas = %d, want the old slot alone", n)
	}
}

// /start?fresh=1 moves a lost transcript aside before the launch: with a delete in flight it is
// refused before that, not after.
func TestFreshStartIsRefusedBeforeItsPreparation(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "fresh09"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	prev := startFreshFn
	t.Cleanup(func() { startFreshFn = prev })
	prepared := false
	startFreshFn = func(string) error { prepared = true; return nil }
	end, ok, why := gitx.BeginDelete(dir, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/start?fresh=1", nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleStartSession(w, r)
	if w.Code != http.StatusConflict || spaceErrCode(t, w.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("start = %d %s, want 409 %s", w.Code, w.Body, gitx.ErrCodeDeleteInProgress())
	}
	if prepared {
		t.Fatal("the fresh start was prepared before the refusal")
	}
}

// A Managed input reserves even when the runtime looked alive: it can die before Resume, which
// then starts it again.
func TestManagedInputReservesEvenForALiveLookingRuntime(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "minput1", Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	prev := managedAlive
	t.Cleanup(func() { managedAlive = prev })
	managedAlive = func(session.Meta) bool { return true }
	held := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { held = gitx.LaunchingUnder(dir) }})
	d, _ := driverOf(m)
	if _, err := resumeManaged(d, m); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("Resume ran without the folder reserved")
	}
}
