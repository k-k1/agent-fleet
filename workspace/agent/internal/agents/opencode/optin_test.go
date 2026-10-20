package opencode

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// optInHome gives the test its own HOME with a pinned-1.x install on PATH-resolution
// (opencodeBinary left at its real default, so the opt-in's own branch is what is measured).
func optInHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(MajorEnv, "")
	// The opt-in only exists next to a 1.x on PATH; pin that so a real opencode 2 on the machine
	// (the image pin since the cut-over) cannot decide these tests.
	useMajor(t, 1)
	oldPins := buildPinsPath
	buildPinsPath = filepath.Join(home, "no-versions.json")
	t.Cleanup(func() { buildPinsPath = oldPins })
	return home
}

// stubTerminalSlots replaces terminalSlots for the test. The restore waits for the reconciles the
// code under test started in the background (goReconcile): they read terminalSlots, and restoring
// it under them is a data race. The same goes for the idle watchers (idleWatch) an earlier test's
// Supervisor armed: they poll terminalSlots on a timer, so they are ended before every write.
func stubTerminalSlots(t *testing.T, fn func() []session.Meta) {
	t.Helper()
	idleWatch.stop()
	old := terminalSlots
	terminalSlots = fn
	t.Cleanup(func() {
		reconcileInflight.Wait()
		idleWatch.stop()
		terminalSlots = old
	})
}

// installFakeV2 lays out the private prefix the way `install-opencode2` leaves it.
func installFakeV2(t *testing.T) string {
	t.Helper()
	pkg := filepath.Join(V2Prefix(), "node_modules", "@opencode", "cli")
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@opencode/cli","version":"2.0.28"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(pkg, "bin", "opencode.exe")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// With nothing opted in, every launch surface answers what it answered before the opt-in
// existed — even when the v2 package is installed. This is the "default stays as it is" pin.
func TestOptInOffChangesNothingEvenWithV2Installed(t *testing.T) {
	optInHome(t)
	useMajor(t, 1)
	stubUsagePref(t, UsageOwn)
	before := buildProgram("anthropic/claude-x", "plan", "ses_abc", false)
	envBefore := strings.Join(env(), "\n")
	installFakeV2(t) // installed, but not switched on

	if UseV2() || Binary() != "opencode" {
		t.Fatalf("UseV2=%v Binary=%q, want off and the bare name", UseV2(), Binary())
	}
	if got := buildProgram("anthropic/claude-x", "plan", "ses_abc", false); got != before {
		t.Fatalf("launch line changed by an installed-but-off v2:\n got %s\nwant %s", got, before)
	}
	if want := "opencode --auto --session 'ses_abc' --model 'anthropic/claude-x' --agent plan"; before != want {
		t.Fatalf("v1 launch line = %q, want %q", before, want)
	}
	if got := strings.Join(env(), "\n"); got != envBefore || strings.Contains(got, "OPENCODE_DB") {
		t.Fatalf("env changed or carries OPENCODE_DB while off: %q", got)
	}
	if launchKey() != "" {
		t.Fatalf("launchKey = %q, want empty", launchKey())
	}
}

func TestOptInOnLaunchesTheV2BinaryWithItsOwnDatabase(t *testing.T) {
	home := optInHome(t)
	exe := installFakeV2(t)
	stubUsagePref(t, UsageOwn)
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}

	if !UseV2() || Binary() != exe {
		t.Fatalf("UseV2=%v Binary=%q, want on and %q", UseV2(), Binary(), exe)
	}
	if got := installedMajor(); got != 2 {
		t.Fatalf("installedMajor = %d, want 2 from the opt-in binary's manifest", got)
	}
	line := buildProgram("", "", "", false)
	if !strings.HasPrefix(line, "'"+exe+"' --standalone") {
		t.Fatalf("v2 line does not start with the quoted opt-in binary: %q", line)
	}
	resume := buildProgram("anthropic/m", "plan", "ses_abc", false)
	if !strings.Contains(resume, "AF_OC_OUT=$('"+exe+"' api --standalone") || strings.Contains(resume, "$(opencode ") || strings.Contains(resume, "then opencode ") {
		t.Fatalf("resume line still names the bare opencode: %q", resume)
	}
	wantDB := filepath.Join(home, ".local", "share", "agent-fleet", "opencode2-data", "opencode.db")
	found := false
	for _, kv := range env() {
		if kv == "OPENCODE_DB="+wantDB {
			found = true
		}
	}
	if !found {
		t.Fatalf("env() lacks OPENCODE_DB=%s: %v", wantDB, env())
	}
	if filepath.Dir(V2DBPath()) == filepath.Join(home, ".local", "share", "opencode") {
		t.Fatal("the v2 database sits in the v1 data directory")
	}
}

// After the cut-over the PATH opencode is 2.x and owns the main database. A workspace that had the
// opt-in on keeps its switch file and its private install, and none of it is used: the launch is the
// bare pinned binary, no OPENCODE_DB, no daemon key, so nothing splits the member's sessions in two.
func TestOptInIsInertWhenThePinnedOpencodeIsV2(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	stubUsagePref(t, UsageOwn)
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	useMajor(t, 2) // the pinned opencode on PATH is 2.x (after optInHome, which pins 1.x)

	if !WantsV2() || V2Binary() == "" {
		t.Fatal("the leftover switch and install must still read as they are on disk")
	}
	if UseV2() || !PinnedV2() {
		t.Fatalf("UseV2=%v PinnedV2=%v, want the opt-in unused", UseV2(), PinnedV2())
	}
	spec := CurrentLaunch()
	if spec.V2 || spec.Bin != "opencode" || spec.Maj != 2 || spec.Key != "" {
		t.Fatalf("launch = %+v, want the bare pinned opencode 2", spec)
	}
	for _, kv := range env() {
		if strings.HasPrefix(kv, "OPENCODE_DB=") {
			t.Fatalf("env carries %s: the pinned opencode must use the main database", kv)
		}
	}
	if line := buildProgram("", "", "", false); !strings.HasPrefix(line, "opencode --standalone") {
		t.Fatalf("launch line = %q, want the pinned opencode (not the private one)", line)
	}
	if installedMajor() != 2 || installedVersion() != "2.0.28" {
		t.Fatalf("major/version = %d/%q", installedMajor(), installedVersion())
	}
	// The same leftovers are live again once the image is re-pinned to 1.x (rollback).
	useMajor(t, 1)
	if !UseV2() || CurrentLaunch().Bin != V2Binary() {
		t.Fatal("a rollback to a 1.x pin must give the opt-in back")
	}
}

func TestOptInOnWithoutBinaryStaysOnV1(t *testing.T) {
	optInHome(t)
	useMajor(t, 1)
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	if UseV2() || Binary() != "opencode" || len(v2ProcessEnv()) != 0 {
		t.Fatalf("a request without an installed binary must not change anything (UseV2=%v)", UseV2())
	}
}

func TestOptInEnvironmentVariableWinsOverTheFile(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MajorEnv, "1")
	if UseV2() {
		t.Fatal("AF_OPENCODE_MAJOR=1 must opt out whatever the file says")
	}
	if err := SetWantsV2(false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MajorEnv, "2")
	if !UseV2() {
		t.Fatal("AF_OPENCODE_MAJOR=2 must opt in")
	}
}

// The shell of a session must not inherit the v2 database: a 1.x `opencode` run there would
// write 1.x tables into it. The value decides, not the switch as it is now: a pane built while
// the opt-in was on keeps it after the switch went back.
func TestOptInKeepsTheDatabasePathOutOfSessionShells(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	got := shellEnvironment([]string{"OPENCODE_DB=/members/own.db", "FOO=1"})
	if got["OPENCODE_DB"] != "/members/own.db" {
		t.Fatal("a member's own OPENCODE_DB must pass")
	}
	_ = SetWantsV2(true)
	paneEnv := CurrentLaunch().Env() // built while on
	_ = SetWantsV2(false)            // the switch went back before the PUT
	got = shellEnvironment(append(paneEnv, "FOO=1"))
	if _, ok := got["OPENCODE_DB"]; ok || got["FOO"] != "1" {
		t.Fatalf("shell environment = %v", got)
	}
}

func sleepServe(t *testing.T) *exec.Cmd {
	return sleepArgs(t, "serve", "--hostname", "127.0.0.1", "--port", "7799")
}

// sleepArgs starts a process that is not a daemon but carries the given argv tokens. The `; :`
// keeps sh from exec'ing straight into sleep, which would drop the tokens.
func sleepArgs(t *testing.T, tokens ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", "sleep 30; :", "sh"}, tokens...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	awaitExec(t, cmd, tokens)
	return cmd
}

// awaitExec waits until /proc shows the started process with its final argv. Start returns once
// the child has passed the point where its close-on-exec descriptors are dropped, but the kernel
// fills in the new argv range a moment later; a /proc/<pid>/cmdline read in that window is empty,
// so isRecordedServe says "not the daemon" and the code under test, which asks it, wrongly sees no
// daemon to stop (measured: 1 in ~300 runs under CPU load, the 5 s timeout of issue 2354).
func awaitExec(t *testing.T, cmd *exec.Cmd, tokens []string) {
	t.Helper()
	want := strings.Join(tokens, "\x00") + "\x00"
	path := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cmdline"
	var got []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		got, _ = os.ReadFile(path)
		if strings.HasSuffix(string(got), want) {
			return
		}
	}
	t.Fatalf("%s never showed the argv tail %q after Start; last read %q", path, want, got)
}

func exitedBySignal(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) bool {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return false
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		return ok && ws.Signaled() && ws.Signal() == sig
	case <-time.After(5 * time.Second):
		return false
	}
}

// Flipping the switch while a daemon the Agent started is up replaces it (Restart: drain, stop,
// re-resume); a daemon already started the new way is left alone.
func TestFlippingTheSwitchRestartsTheOwnedDaemon(t *testing.T) {
	s := unspawnableAddr(t)
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	cmd := sleepServe(t)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, cmd, "" // started on the pinned 1.x
	s.mu.Unlock()

	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	if err := s.retireStale(); err != nil {
		t.Fatal(err)
	}
	if !exitedBySignal(t, cmd, syscall.SIGTERM) {
		t.Fatal("the 1.x daemon was not stopped when the switch went to 2")
	}
	if s.Running() {
		t.Fatal("the supervisor still believes the old daemon is up")
	}
}

func TestSwitchUnchangedLeavesTheDaemonAlone(t *testing.T) {
	s := unspawnableAddr(t)
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	_ = SetWantsV2(true)
	cmd := sleepServe(t)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, cmd, launchKey()
	s.mu.Unlock()
	if err := s.retireStale(); err != nil {
		t.Fatal(err)
	}
	if !s.Running() || cmd.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("a daemon started under the current setting was replaced")
	}
}

func TestSwitchingBackReplacesTheV2Daemon(t *testing.T) {
	s := unspawnableAddr(t)
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	_ = SetWantsV2(true)
	key := launchKey()
	cmd := sleepServe(t)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, cmd, key
	s.mu.Unlock()
	if err := SetWantsV2(false); err != nil {
		t.Fatal(err)
	}
	if err := s.retireStale(); err != nil {
		t.Fatal(err)
	}
	if !exitedBySignal(t, cmd, syscall.SIGTERM) {
		t.Fatal("the 2.x daemon was not stopped when the switch went back to 1")
	}
}

// A daemon an earlier Agent process started is found by the pid it recorded.
func TestFlippingTheSwitchStopsAnAdoptedDaemonByItsRecordedPid(t *testing.T) {
	s := unspawnableAddr(t)
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	_ = SetWantsV2(true)
	cmd := sleepServe(t)
	noteServeStart(cmd.Process.Pid, launchKey())
	_ = SetWantsV2(false)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, nil, adoptedKey()
	s.mu.Unlock()
	if err := s.retireStale(); err != nil {
		t.Fatal(err)
	}
	if !exitedBySignal(t, cmd, syscall.SIGTERM) {
		t.Fatal("the adopted 2.x daemon was not stopped by its recorded pid")
	}
}

func TestFlippingWithAnUntraceableAdoptedDaemonSaysSo(t *testing.T) {
	s := unspawnableAddr(t)
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	_ = SetWantsV2(true)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, nil, "" // adopted, no record: started by an Agent without the opt-in
	s.mu.Unlock()
	if err := s.retireStale(); err == nil {
		t.Fatal("an adopted daemon of the other major with no recorded pid must be reported, not attached to")
	}
}

// A workspace that never opts in never gets the record file.
func TestDefaultStartLeavesNoServeRecord(t *testing.T) {
	optInHome(t)
	noteServeStart(os.Getpid(), "")
	if _, err := os.Stat(serveRecordFile()); err == nil {
		t.Fatal("a default start wrote the opt-in's record file")
	}
}

// A slot launched on 2.x keeps a "shared" record; a later 1.x launch of it must clear that or
// switching back to 2 would count the 1.x pane as attached to the serve.
func TestV1LaunchClearsAStaleSharedSlotMode(t *testing.T) {
	optInHome(t)
	useMajor(t, 1)
	stubUsagePref(t, UsageOwn)
	m := session.Meta{Name: "oc-1", Dir: t.TempDir(), Kind: session.KindOpencode}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := slotModes.Read(sid); got != "" {
		t.Fatalf("slot mode after a 1.x launch = %q, want none", got)
	}
}

// The replacement is driven by ensure itself, not only by retireStale: the Console's polling
// of Resume is what notices the flip.
func TestEnsureReplacesADaemonStartedUnderTheOtherSetting(t *testing.T) {
	s := unspawnableAddr(t)
	stubUsagePref(t, UsageOff) // past the replacement, ensure stops at the connection gate
	t.Setenv(MajorEnv, "")
	installFakeV2(t)
	cmd := sleepServe(t)
	s.mu.Lock()
	s.up, s.cmd, s.key = true, cmd, ""
	s.mu.Unlock()
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.Ensure()
	if !exitedBySignal(t, cmd, syscall.SIGTERM) {
		t.Fatal("Ensure left the daemon of the other setting running")
	}
}

func recordOf(t *testing.T, cmd *exec.Cmd) serveRecord {
	t.Helper()
	exe, _ := os.Readlink("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/exe")
	return serveRecord{PID: cmd.Process.Pid, Key: "v2:x", Start: procStart(cmd.Process.Pid), Exe: exe,
		Boot: bootID(), Port: "7799"}
}

// The record names a process instance; a recycled pid, another program, another port, or a command
// line that merely contains "serve" must never be taken for the daemon (and so never signalled).
func TestRecordedServeIdentity(t *testing.T) {
	good := sleepServe(t)
	if r := recordOf(t, good); !isRecordedServe(r) {
		t.Fatalf("the daemon itself was not recognised: %+v", r)
	}
	r := recordOf(t, good)
	r.Start++ // a different process instance that got the same pid
	if isRecordedServe(r) {
		t.Fatal("a pid whose start time differs was accepted")
	}
	r = recordOf(t, good)
	r.Exe = "/usr/bin/other"
	if isRecordedServe(r) {
		t.Fatal("another executable was accepted")
	}
	r = recordOf(t, good)
	r.Boot = "another-boot"
	if isRecordedServe(r) {
		t.Fatal("a record from another boot was accepted")
	}
	// Another opencode serve (other port).
	if r := recordOf(t, sleepArgs(t, "serve", "--hostname", "127.0.0.1", "--port", "1234")); isRecordedServe(r) {
		t.Fatal("a serve on another port was accepted")
	}
	// A command line that only contains the word.
	for _, tok := range [][]string{{"unrelated-language-server", "--port", "7799"}, {"observe-server", "--port=7799"}, {"--stdio", "myserver", "--port", "7799"}} {
		if r := recordOf(t, sleepArgs(t, tok...)); isRecordedServe(r) {
			t.Fatalf("%v was taken for the daemon", tok)
		}
	}
}

func TestSignalRecordedRefusesAMismatchedProcess(t *testing.T) {
	cmd := sleepServe(t)
	r := recordOf(t, cmd)
	r.Start++
	if signalRecorded(r, syscall.SIGTERM) {
		t.Fatal("signalled a process whose identity does not match")
	}
	if cmd.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("the process died")
	}
}

// recorder scripts: "<who> <OPENCODE_DB>" appended to a log per run.
func recorderBins(t *testing.T) (log string) {
	t.Helper()
	home := os.Getenv("HOME")
	log = filepath.Join(home, "ran.log")
	script := func(who string) []byte {
		return []byte("#!/bin/sh\necho \"" + who + " ${OPENCODE_DB:-none}\" >> " + log + "\n")
	}
	bin := filepath.Join(home, "bin")
	_ = os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), script("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := installFakeV2(t)
	if err := os.WriteFile(exe, script("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// A switch that flips while a start is being prepared must not pair one major's binary with the
// other's database. Models reads the environment first and the binary after a daemon probe; the
// flip is injected exactly between them, in both directions.
func TestModelsTakesBinaryAndDatabaseFromOneReading(t *testing.T) {
	optInHome(t)
	log := recorderBins(t)
	old := modelsFromDaemon
	t.Cleanup(func() { modelsFromDaemon = old })
	run := func() string {
		modelsAt = time.Time{}
		_ = os.Remove(log)
		_ = Models()
		b, _ := os.ReadFile(log)
		return strings.TrimSpace(string(b))
	}
	modelsFromDaemon = func() []string { _ = SetWantsV2(true); return nil } // 1 → 2 at the probe
	_ = SetWantsV2(false)
	if got := run(); got != "v1 none" {
		t.Fatalf("flip to 2 during the probe: ran %q, want the 1.x binary without OPENCODE_DB", got)
	}
	modelsFromDaemon = func() []string { _ = SetWantsV2(false); return nil } // 2 → 1 at the probe
	_ = SetWantsV2(true)
	if got := run(); got != "v2 "+V2DBPath() {
		t.Fatalf("flip to 1 during the probe: ran %q, want the 2.x binary with its own database", got)
	}
}

func TestLaunchSpecIsFixedAtTheReading(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	off := CurrentLaunch()
	_ = SetWantsV2(true)
	on := CurrentLaunch()
	_ = SetWantsV2(false)
	if off.Bin != "opencode" || strings.Contains(strings.Join(off.Env(), " "), "OPENCODE_DB") {
		t.Fatalf("reading taken while off: %+v %v", off, off.Env())
	}
	if on.Bin != V2Binary() || !strings.Contains(strings.Join(on.Env(), " "), "OPENCODE_DB=") {
		t.Fatalf("reading taken while on: %+v %v", on, on.Env())
	}
	if p := buildProgramFor(on, "", "", "", false); !strings.HasPrefix(p, "'"+V2Binary()+"' --standalone") {
		t.Fatalf("a reading taken while on launched %q after the switch went off", p)
	}
}

// 2 → 1: the old 2.x Terminal panes are still attached to the serve being replaced, so the drain
// must count them even though the switch no longer says 2.
func TestSwitchingToV1StillDrainsRunningV2TerminalPanes(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	_ = SetWantsV2(true)
	m := session.Meta{Name: "oc-term", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	markSlotMode(session.UUID(m.Dir, m.Name), modeShared) // what the 2.x launch recorded
	t.Cleanup(func() { slotModes.Remove(session.UUID(m.Dir, m.Name)) })
	if !sharedSlot(m) {
		t.Fatal("precondition: shared pane not recognised while on")
	}
	_ = SetWantsV2(false)
	if !sharedSlot(m) {
		t.Fatal("after the switch went to 1, the pane that is still on the 2.x serve dropped out of sharedSlot (drain and dependents skip it)")
	}
}

// The same through the drain's own query: after 2 → 1 a busy 2.x pane is still reported busy, so
// the drain waits for it (and aborts it at the timeout) before the serve is replaced.
func TestDrainSeesABusyV2PaneAfterTheSwitchWentToV1(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	_ = SetWantsV2(true)
	m := session.Meta{Name: "oc-busy", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	sids.Write(sid, "ses_busy")
	t.Cleanup(func() { slotModes.Remove(sid); sids.Remove(sid) })
	stubTerminalSlots(t, func() []session.Meta {
		if sharedSlot(m) {
			return []session.Meta{m}
		}
		return nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ses_busy":{"type":"running"}}}`))
	}))
	t.Cleanup(srv.Close)
	detectedMajor.Store(int32(apiV2))
	t.Cleanup(func() { detectedMajor.Store(int32(apiV1)) })

	_ = SetWantsV2(false)
	if got := busyTerminals(srv.URL); len(got) != 1 || got[0] != "ses_busy" {
		t.Fatalf("busy terminals after 2 → 1 = %v, want the running 2.x pane", got)
	}
}

// An off reading stays off: the switch going on while BuildLaunch is in flight (between its
// first reading and the engine environment) must not turn the 1.x launch into an attach to a
// 2.x serve.
func TestBuildLaunchOffReadingSurvivesAFlipOn(t *testing.T) {
	optInHome(t)
	useMajor(t, 1)
	installFakeV2(t)
	stubUsagePref(t, UsageOwn)
	old := EngineEnv
	EngineEnv = func(string) []string { _ = SetWantsV2(true); return nil }
	t.Cleanup(func() { EngineEnv = old })
	m := session.Meta{Name: "oc-flip", Dir: t.TempDir(), Kind: session.KindOpencode}
	plan, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Program != "opencode --auto" || strings.Contains(strings.Join(plan.Env, " "), "OPENCODE_SERVER") {
		t.Fatalf("a launch read while off became %q (env %v)", plan.Program, plan.Env)
	}
	if got := buildProgramFor(CurrentLaunch(), "", "", "", false); !strings.Contains(got, "--standalone") {
		t.Fatalf("precondition: the switch is on now: %q", got)
	}
}

// An adopted daemon (Agent restarted: not supervised, still healthy) is drained like a supervised
// one before it is signalled: its Terminal panes are still running turns on it.
func TestRetiringAnAdoptedDaemonDrainsItsBusyTerminals(t *testing.T) {
	optInHome(t)
	t.Setenv("AF_OPENCODE_DRAIN_TIMEOUT_SEC", "1")
	installFakeV2(t)
	var cmd *exec.Cmd
	dead := make(chan struct{})
	var mu sync.Mutex
	aborted := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-dead:
			http.Error(w, "gone", http.StatusServiceUnavailable)
			return
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/info":
			_, _ = w.Write([]byte(`{"version":"2.0.28"}`))
		case r.URL.Path == "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{"ses_busy":{"type":"running"}}}`))
		case strings.HasSuffix(r.URL.Path, "/interrupt"):
			mu.Lock()
			aborted = r.URL.Path
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"healthy":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(serveAddrEnv, srv.URL)
	detectedMajor.Store(int32(apiV2))
	t.Cleanup(func() { detectedMajor.Store(int32(apiV1)) })
	cmd = sleepArgs(t, "serve", "--hostname", "127.0.0.1", "--port", servePort(srv.URL))
	go func() { _ = cmd.Wait(); close(dead) }()
	noteServeStart(cmd.Process.Pid, "v2:x")

	m := session.Meta{Name: "oc-adopted", Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverTUI}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	sids.Write(sid, "ses_busy")
	t.Cleanup(func() { slotModes.Remove(sid); sids.Remove(sid) })
	stubTerminalSlots(t, func() []session.Meta { return []session.Meta{m} })

	s := &Supervisor{} // not up: the Agent just restarted
	if err := s.retireAdopted(srv.URL); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(aborted, "ses_busy") {
		t.Fatalf("the busy Terminal turn was neither waited for nor aborted before the daemon was stopped (abort %q)", aborted)
	}
}

// The resolver that decides an off reading's major is the default one, and it must not read the
// switch: a flip landing at its call boundary may not produce {off, major 2}. (useMajor would hide
// this by replacing the resolver, so the real one is kept and only wrapped.)
func TestOffReadingMajorDoesNotReadTheSwitchAgain(t *testing.T) {
	optInHome(t)
	installFakeV2(t)
	stubUsagePref(t, UsageOwn)
	t.Setenv(standaloneEnv, "1")
	orig := opencodeBinary
	opencodeBinary = func() string { _ = SetWantsV2(true); return orig() }
	t.Cleanup(func() { opencodeBinary = orig })

	spec := CurrentLaunch()
	if spec.V2 || spec.Maj == 2 || spec.Bin != "opencode" {
		t.Fatalf("mixed reading: %+v", spec)
	}
	_ = SetWantsV2(false)
	m := session.Meta{Name: "oc-boundary", Dir: t.TempDir(), Kind: session.KindOpencode}
	opencodeBinary = func() string { _ = SetWantsV2(true); return orig() }
	plan, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Program, "--standalone") || strings.Contains(strings.Join(plan.Env, " "), "OPENCODE_DB") {
		t.Fatalf("a 1.x launch got 2.x flags or database: %q %v", plan.Program, plan.Env)
	}
}

// One start decides "shared daemon" once: the password policy and AF_OPENCODE_SHARED_SERVE come
// from the same answer. The switch flips while ensure builds the child's environment (the engine
// environment is read there); the daemon must still be started the shared way.
func TestEnsureDecidesTheSharedDaemonOncePerStart(t *testing.T) {
	home := optInHome(t)
	stubUsagePref(t, UsageFree)
	t.Setenv(standaloneEnv, "")
	exe := installFakeV2(t)
	envLog := filepath.Join(home, "child-env")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nenv > "+envLog+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetWantsV2(true); err != nil {
		t.Fatal(err)
	}
	t.Setenv(serveAddrEnv, "http://127.0.0.1:"+freePortForTest(t))
	oldTimeout := serveStartTimeout
	serveStartTimeout = 1500 * time.Millisecond
	oldEngine := EngineEnv
	EngineEnv = func(string) []string { _ = SetWantsV2(false); return nil }
	t.Cleanup(func() { serveStartTimeout, EngineEnv = oldTimeout, oldEngine })

	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	_, _, _ = s.Ensure() // the fake never answers; only the environment it was started with matters
	b, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("the daemon was never started: %v", err)
	}
	if !strings.Contains(string(b), sharedServeEnv+"=1") {
		t.Fatalf("the daemon started for a 2.x reading lost %s after the switch flipped:\n%s", sharedServeEnv, b)
	}
	if !strings.Contains(string(b), "OPENCODE_DB="+V2DBPath()) {
		t.Fatalf("the daemon lost its database override:\n%s", b)
	}
}

func freePortForTest(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// A Supervisor an earlier test armed leaves its zero-demand watcher polling terminalSlots; the next
// stubTerminalSlots must end it before writing the var, or -race reports the write against the
// watcher's read (seen as TestRetiringAnAdoptedDaemonDrainsItsBusyTerminals failing after
// TestEnsureStartsServeWithPassword).
func TestStubbingTerminalSlotsEndsLeftoverIdleWatchers(t *testing.T) {
	prev := agents.IdleTickForTest(time.Millisecond)
	t.Cleanup(func() { idleWatch.stop(); agents.IdleTickForTest(prev) })
	t.Setenv(idleGraceEnv, "600")
	s := &Supervisor{}
	s.mu.Lock()
	s.armIdleWatchLocked()
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // several ticks: the watcher is reading terminalSlots
	t.Run("stub", func(t *testing.T) {
		stubTerminalSlots(t, func() []session.Meta { return nil })
		time.Sleep(20 * time.Millisecond)
	})
}
