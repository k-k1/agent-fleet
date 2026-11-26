package sessionx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	// The created session's wire answer starts background refreshes that read globals other
	// tests set.
	t.Cleanup(WaitListBackground)
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
	// The created session's wire answer starts background refreshes that read globals other
	// tests set.
	t.Cleanup(WaitListBackground)
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

// A launch whose Subdir lies in a folder a delete has marked is refused once the folder has been
// moved away: the Subdir is matched as stored, not through Meta.CWD's fallback to Dir, and a link
// on the way to it is followed even though its target is gone now.
func TestLaunchIntoAMovedSubdirMeetsTheMark(t *testing.T) {
	home := writebackEnv(t)
	space := filepath.Join(home, "spaces", "notes")
	if err := os.MkdirAll(filepath.Join(space, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(space, filepath.Join(home, "shortcut")); err != nil {
		t.Fatal(err)
	}
	end, ok, why := gitx.BeginDelete(space, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	if err := os.Rename(space, filepath.Join(home, "moved")); err != nil {
		t.Fatal(err)
	}
	for _, m := range []session.Meta{
		{Name: "subd01", Dir: home, Subdir: "spaces/notes/sub", Kind: session.KindShell},
		{Name: "subd02", Dir: home, Subdir: "shortcut/sub", Kind: session.KindShell},
	} {
		session.WriteMeta(m)
		if release, ref := reserveLaunch(m, &m); ref == nil || ref.Code != gitx.ErrCodeDeleteInProgress() {
			if release != nil {
				release()
			}
			t.Errorf("%s: ref = %+v, want %s", m.Subdir, ref, gitx.ErrCodeDeleteInProgress())
		}
	}
}

// A release waits for the launch gate. A delete reads the reservations and then the running
// sessions inside it; a release between the two reads would hide a session that has just been
// published from both. Only the first call waits: a second one returns without the gate.
func TestReleaseWaitsForTheLaunchGate(t *testing.T) {
	home := writebackEnv(t)
	m := session.Meta{Name: "rel01", Dir: home, Kind: session.KindShell}
	release, ref := reserveLaunch(m, nil)
	if ref != nil {
		t.Fatal(ref.Message)
	}
	done := make(chan struct{})
	gitx.WithLaunchGate(func() {
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
	again := make(chan struct{})
	gitx.WithLaunchGate(func() {
		go func() { release(); close(again) }()
		select {
		case <-again:
		case <-time.After(5 * time.Second):
			t.Error("a second release waited for the gate")
		}
	})
}

// A launch does not wait for the deletion gate, which a worktree recreate or restore holds across
// `git worktree add`: a hook there that starts a session through the Agent's API and waits for it
// would otherwise never return.
func TestLaunchDoesNotWaitForTheDeletionGate(t *testing.T) {
	home := writebackEnv(t)
	m := session.Meta{Name: "gate01", Dir: home, Kind: session.KindShell}
	WithDeletionGate(func() {
		done := make(chan *SpawnRefusal, 1)
		go func() {
			release, ref := reserveLaunch(m, nil)
			if release != nil {
				release()
			}
			done <- ref
		}()
		select {
		case ref := <-done:
			if ref != nil {
				t.Errorf("refused: %+v", ref)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a launch waited for the deletion gate")
		}
	})
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
	// The request's own earlier read is what a start is judged against (ensureSessionTmuxFrom):
	// the launch's later read may already be the delete's result.
	shelvedNow := session.Meta{Name: "settled2", Dir: filepath.Join(home, "gone"), Kind: session.KindClaude, Archived: true}
	session.WriteMeta(shelvedNow)
	before := shelvedNow
	before.Archived = false
	if _, ref := reserveLaunch(shelvedNow, &before); ref == nil {
		t.Fatal("a session shelved since the request read it was started")
	}
	// Shelved already when the request read it, its folder removed by hand: it starts as on main
	// (a shell falls back to home).
	release, ref := reserveLaunch(shelvedNow, &shelvedNow)
	if ref != nil {
		t.Fatalf("a shelved session whose folder was removed by hand was refused: %+v", ref)
	}
	release()
	session.WriteMeta(snap)
	release, ref = reserveLaunch(snap, &snap)
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

// A recreate of a shelved shell whose folder was removed by hand starts as it does on main (the
// shell falls back to home): only a delete's settlement refuses it.
func TestRecreateOfAShelvedShellWithItsFolderRemovedByHand(t *testing.T) {
	p := newCreateProbe(t)
	// The created session's wire answer starts background refreshes that read globals other
	// tests set.
	t.Cleanup(WaitListBackground)
	launched := false
	launchTmuxFn = func(session.Meta, bool) error { launched = true; return nil }
	const name = "byhand02"
	session.WriteMeta(session.Meta{Name: name, Dir: filepath.Join(p.home, "removed"), Kind: session.KindShell, Archived: true})
	w := callHandler(HandleRecreateSession, http.MethodPost, name, "")
	if w.Code != http.StatusOK || !launched {
		t.Fatalf("recreate = %d %s launched=%v, want 200 and a launch", w.Code, w.Body, launched)
	}
}

// A Managed create holds its folder until its meta is published, and has released it by the time
// the launch task is delivered (the publish is observed through the booked-name seam).
func TestManagedCreateHoldsItsFolderUntilPublished(t *testing.T) {
	p := newCreateProbe(t)
	// The created session's wire answer starts background refreshes that read globals other
	// tests set.
	t.Cleanup(WaitListBackground)
	var events []string
	log := func(e string) {
		held := "free"
		if gitx.LaunchingUnder(p.repo) {
			held = "held"
		}
		events = append(events, e+"("+held+")")
	}
	prev, had := managedDrivers["codex"]
	managedDrivers["codex"] = eventDriver{log: log}
	t.Cleanup(func() {
		if had {
			managedDrivers["codex"] = prev
		} else {
			delete(managedDrivers, "codex")
		}
	})
	prevMeta := writeMoveTargetMeta
	t.Cleanup(func() { writeMoveTargetMeta = prevMeta })
	writeMoveTargetMeta = func(m session.Meta) error { log("publish"); return session.WriteMetaChecked(m) }
	rec := p.post(map[string]any{"kind": "codex", "driver": "managed", "dir": p.repo, "initial_prompt": "go"}, "mcreate1")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got, want := strings.Join(events, ","), "launch(held),publish(held),deliver(free)"; got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
}

// /start judges a settlement against the meta it read first, not against the launch's own later
// read, which may already show the delete's shelving. The shelving is put in place through the
// fresh-start seam, the one step between the two reads.
func TestStartJudgesTheMetaItReadFirst(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "first01"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged,
		StoppedAt: "2026-09-25T10:00:00+09:00"})
	resumed := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { resumed = true }})
	prev := startFreshFn
	t.Cleanup(func() { startFreshFn = prev })
	startFreshFn = func(n string) error {
		UpdateSessionMeta(n, func(m *session.Meta) bool { m.Archived = true; return true })
		return nil
	}
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/start?fresh=1", nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleStartSession(w, r)
	if w.Code != http.StatusConflict || spaceErrCode(t, w.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() {
		t.Fatalf("start = %d %s, want 409 %s", w.Code, w.Body, gitx.ErrCodeDeleteInProgress())
	}
	if resumed {
		t.Fatal("a session shelved during the start was launched")
	}
}

// A link before `..` takes the kernel out of the folder a lexical clean would name: the session
// below runs in other/app. A delete of repos/app does not settle it, but records it in its mark
// (the guards count the lexical clean too, for a session started before the link was re-pointed),
// and the mark's name list does not refuse its start.
func TestStartIsNotRefusedByTheMarksNameList(t *testing.T) {
	home := writebackEnv(t)
	repos, other := filepath.Join(home, "repos"), filepath.Join(home, "other")
	for _, d := range []string{filepath.Join(repos, "app"), filepath.Join(other, "nested"), filepath.Join(other, "app")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(other, "nested"), filepath.Join(repos, "jump")); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(repos, "app")
	m := session.Meta{Name: "clname1", Dir: filepath.Join(repos, "jump") + "/../app", Kind: session.KindShell}
	session.WriteMeta(m)
	if gitx.SessionLivesIn(m, app) {
		t.Fatalf("%s is counted as living in %s", m.Dir, app)
	}
	sessions, err := gitx.ReadDirSessions(app)
	if err != nil {
		t.Fatal(err)
	}
	if names := sessions.Names(); len(names) != 1 {
		t.Fatalf("the mark's name list = %v; the test needs it to hold the session", names)
	}
	if settle := sessions.ToSettle(); len(settle) != 0 {
		t.Fatalf("a session that runs elsewhere would be settled: %+v", settle)
	}
	end, ok, why := gitx.BeginDelete(app, "", sessions.Names()...)
	if !ok {
		t.Fatal(why)
	}
	defer end()
	release, ref := reserveLaunch(m, &m)
	if ref != nil {
		t.Fatalf("a session the kernel runs in %s was refused: %+v", filepath.Join(other, "app"), ref)
	}
	release()
}

// A carried answer judges a settlement against the meta the request read first, before it
// composed the answer, not against the helper's own later read.
func TestCarriedAnswerJudgesTheMetaItReadFirst(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "carry01"
	first := session.Meta{Name: name, Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged,
		StoppedAt: "2026-09-25T10:00:00+09:00"}
	shelved := first
	shelved.Archived = true
	session.WriteMeta(shelved)
	resumed := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { resumed = true }})
	rec := httptest.NewRecorder()
	if startForCarriedAnswer(rec, testLease(t, name), name, &first) {
		t.Fatal("a session shelved after the answer was composed was started")
	}
	if rec.Code != http.StatusConflict || spaceErrCode(t, rec.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() || resumed {
		t.Fatalf("answer = %d %s resumed=%v, want 409 %s and no launch", rec.Code, rec.Body, resumed, gitx.ErrCodeDeleteInProgress())
	}
}

// A Managed input to a stopped session whose meta was removed after the request read it (the
// session was deleted meanwhile) never reaches the driver.
func TestManagedInputToASessionDeletedMeanwhileIsRefused(t *testing.T) {
	home := writebackEnv(t)
	dir := filepath.Join(home, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "mgone01", Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged,
		StoppedAt: "2026-09-25T10:00:00+09:00"}
	session.WriteMeta(m)
	read, ok := session.ReadMeta(m.Name)
	if !ok {
		t.Fatal("meta not written")
	}
	if err := os.Remove(session.MetaPath(m.Name)); err != nil {
		t.Fatal(err)
	}
	resumed := false
	useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { resumed = true }})
	d, _ := driverOf(read)
	_, err := resumeManaged(d, read)
	wantDeleteRefusal(t, "managed input", err)
	if resumed {
		t.Fatal("a session deleted after the request read it reached the driver")
	}
}
