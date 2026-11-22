package bootinstall

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// npmStub is a fake npm: `ls` prints $D/ls.json; `install` logs its arguments, fails with the
// text of $D/fail when that file exists, and otherwise writes ~/.local/bin/<cli> for each spec
// from $D/body.<cli> (a working `--version` script when there is none). Pass 2 of a repair reads
// $D/body2.<cli> instead when it exists, so a test can make the first install broken and the
// reinstall good.
const npmStub = `#!/bin/sh
D="$(dirname "$0")"
case "$1" in
ls) [ -f "$D/ls.json" ] && cat "$D/ls.json"; exit 0 ;;
install)
  echo "$*" >>"$D/npm.log"
  if [ -f "$D/fail" ]; then cat "$D/fail" >&2; exit 1; fi
  n=$(wc -l <"$D/npm.log")
  shift; prefix=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --prefix) prefix="$2"; shift ;;
      -*) ;;
      *)
        cli=""
        case "$1" in
          @anthropic-ai/claude-code@*) cli=claude ;;
          @opencode/cli@*) cli=opencode ;;
          @openai/codex@*) cli=codex ;;
          @github/copilot@*) cli=copilot ;;
        esac
        if [ -n "$cli" ]; then
          mkdir -p "$prefix/bin"
          f="$D/body.$cli"; [ "$n" -gt 1 ] && [ -f "$D/body2.$cli" ] && f="$D/body2.$cli"
          if [ -f "$f" ]; then cp "$f" "$prefix/bin/$cli"; else printf '#!/bin/sh\necho 1.0.0\n' >"$prefix/bin/$cli"; fi
          chmod +x "$prefix/bin/$cli"
        fi ;;
    esac
    shift
  done
  if [ -f "$D/failafter" ]; then cat "$D/failafter" >&2; exit 1; fi ;;
esac
exit 0
`

const brokenCLI = "#!/bin/sh\nexit 1\n"

type npmFixture struct {
	env    Env
	home   string
	dir    string // the stub's directory
	status string
	out    *bytes.Buffer
}

func newNPM(t *testing.T, pins map[string]string) *npmFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(npmStub), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	status := filepath.Join(t.TempDir(), "install.tsv")
	if err := os.WriteFile(status, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &npmFixture{home: home, dir: dir, status: status, out: out, env: Env{
		Home: home, StatusPath: status, Pins: pins, NPM: filepath.Join(dir, "npm"),
		BakedBin: t.TempDir(), FreeMiB: func(string) (int64, bool) { return 100000, true },
		Stdout: out, Stderr: out, RunTimeout: 10 * time.Second,
	}}
}

var allPins = map[string]string{"claude": "1.0.0", "opencode": "2.0.0", "codex": "3.0.0", "copilot": "4.0.0"}

func (f *npmFixture) put(name, body string) {
	f.putIn(filepath.Join(f.home, ".local", "bin"), name, body)
}

func (f *npmFixture) putIn(dir, name, body string) {
	f.write(filepath.Join(dir, name), body)
	os.Chmod(filepath.Join(dir, name), 0o755)
}

func (f *npmFixture) write(p, body string) {
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		panic(err)
	}
}

func (f *npmFixture) stub(name, body string) { f.write(filepath.Join(f.dir, name), body) }
func (f *npmFixture) bin(c string) string    { return filepath.Join(f.home, ".local", "bin", c) }
func (f *npmFixture) statusText() string {
	b, _ := os.ReadFile(f.status)
	return string(b)
}
func (f *npmFixture) npmLog() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "npm.log"))
	return string(b)
}

func TestNPMInstallsMissingCLIsInOneRun(t *testing.T) {
	f := newNPM(t, allPins)
	if rc := NPM(f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	log := f.npmLog()
	if strings.Count(log, "install") != 1 {
		t.Fatalf("want one npm run, got %q", log)
	}
	for _, spec := range []string{"@anthropic-ai/claude-code@1.0.0", "@opencode/cli@2.0.0", "@openai/codex@3.0.0", "@github/copilot@4.0.0"} {
		if !strings.Contains(log, spec) {
			t.Errorf("npm not asked for %s: %q", spec, log)
		}
	}
	for _, c := range []string{"claude", "opencode", "codex", "copilot"} {
		if !strings.Contains(f.statusText(), c+"\tok\t") {
			t.Errorf("%s not recorded ok: %q", c, f.statusText())
		}
	}
}

func TestNPMIntegrityFailureFailsTheInstall(t *testing.T) {
	f := newNPM(t, allPins)
	f.stub("fail", "npm error code EINTEGRITY\nnpm error sha512-x integrity checksum failed when using sha512\n")
	if rc := NPM(f.env); rc != ExitFailed {
		t.Fatalf("rc=%d, want %d", rc, ExitFailed)
	}
	st := f.statusText()
	for _, c := range []string{"claude", "opencode", "codex", "copilot"} {
		if !strings.Contains(st, c+"\tfailed\tchecksum\n") {
			t.Errorf("%s not failed/checksum: %q", c, st)
		}
		if strings.Contains(st, c+"\tok") {
			t.Errorf("%s recorded ok after an integrity failure", c)
		}
		if _, err := os.Lstat(f.bin(c)); err == nil {
			t.Errorf("%s landed despite the failure", c)
		}
	}
}

func TestNPMFailureReasons(t *testing.T) {
	for msg, want := range map[string]string{
		"npm error ENOSPC: no space left on device": "no_space",
		"npm error code ECONNRESET network aborted": "network",
		"npm error something odd":                   "other",
	} {
		f := newNPM(t, map[string]string{"claude": "1.0.0"})
		f.stub("fail", msg)
		if rc := NPM(f.env); rc != ExitFailed {
			t.Fatalf("%q: rc=%d", msg, rc)
		}
		if !strings.Contains(f.out.String(), "WARN: npm boot-install failed") {
			t.Errorf("%q: no WARN in the log: %s", msg, f.out)
		}
		if !strings.Contains(f.statusText(), "claude\tfailed\t"+want+"\n") {
			t.Errorf("%q: want reason %s, got %q", msg, want, f.statusText())
		}
	}
}

func TestNPMSkipsPresentCLIsWithoutRepin(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0", "codex": "3.0.0"})
	f.put("claude", "#!/bin/sh\necho 0.9\n") // older than the pin, but no repin
	f.put("codex", "#!/bin/sh\necho 3\n")
	if rc := NPM(f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if f.npmLog() != "" {
		t.Fatalf("npm ran: %q", f.npmLog())
	}
	if !strings.Contains(f.statusText(), "claude\tskipped\t") || !strings.Contains(f.out.String(), "already present") {
		t.Fatalf("status=%q out=%q", f.statusText(), f.out)
	}
}

func TestNPMRepinMovesDriftedCopyOnly(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0", "codex": "3.0.0"})
	f.put("claude", "#!/bin/sh\necho 0.9\n")
	f.put("codex", "#!/bin/sh\necho 3\n")
	f.stub("ls.json", `{"dependencies":{"@anthropic-ai/claude-code":{"version":"0.9.0"},"@openai/codex":{"version":"3.0.0"}}}`)
	f.env.Repin = true
	if rc := NPM(f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	log := f.npmLog()
	if !strings.Contains(log, "@anthropic-ai/claude-code@1.0.0") || strings.Contains(log, "codex") {
		t.Fatalf("npm log %q: want claude only", log)
	}
}

func TestNPMRepinLeavesUnmanagedCopyAlone(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0"})
	f.put("claude", "#!/bin/sh\necho 0.9\n")
	f.stub("ls.json", `{}`) // npm does not manage it: no version
	f.env.Repin = true
	if rc := NPM(f.env); rc != ExitOK || f.npmLog() != "" {
		t.Fatalf("rc=%d log=%q", rc, f.npmLog())
	}
}

func TestNPMNoSpaceSkipsAndSignals(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0", "codex": "3.0.0"})
	f.put("codex", "#!/bin/sh\necho 1\n") // present and working, so the repin below is an upgrade
	f.stub("ls.json", `{"dependencies":{"@openai/codex":{"version":"2.0.0"}}}`)
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 100, true }
	if rc := NPM(f.env); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	st := f.statusText()
	if !strings.Contains(st, "claude\tfailed\tno_space\n") || !strings.Contains(st, "codex\tskipped\tno_space\n") {
		t.Fatalf("status=%q", st)
	}
	if f.npmLog() != "" {
		t.Fatalf("npm ran with no space: %q", f.npmLog())
	}
}

func TestNPMBrokenOptionalDependencyIsRepairedAtThePin(t *testing.T) {
	f := newNPM(t, map[string]string{"codex": "3.0.0"})
	f.stub("body.codex", brokenCLI) // npm exited 0 but the binary is not there
	f.stub("body2.codex", "#!/bin/sh\necho 3\n")
	if rc := NPM(f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if strings.Count(f.npmLog(), "install") != 2 {
		t.Fatalf("want install + one repair, got %q", f.npmLog())
	}
	if !strings.Contains(f.out.String(), "codex repaired at the pin 3.0.0") || !f.env.runs(f.bin("codex")) {
		t.Fatalf("out=%s", f.out)
	}
	if !strings.HasSuffix(f.statusText(), "codex\tok\t\n") {
		t.Fatalf("status=%q", f.statusText())
	}
}

func TestNPMStillBrokenAfterRepairRemovesLinkAndFails(t *testing.T) {
	f := newNPM(t, map[string]string{"codex": "3.0.0"})
	f.stub("body.codex", brokenCLI)
	if rc := NPM(f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Lstat(f.bin("codex")); err == nil {
		t.Fatal("broken link left in place")
	}
	if !strings.HasSuffix(f.statusText(), "codex\tfailed\tother\n") {
		t.Fatalf("status=%q", f.statusText())
	}
}

func TestNPMVerifyBrokenWithoutPinIsRemovedNotReinstalled(t *testing.T) {
	f := newNPM(t, map[string]string{})
	f.put("claude", brokenCLI)
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if f.npmLog() != "" {
		t.Fatalf("npm ran without a pin: %q", f.npmLog())
	}
	if _, err := os.Lstat(f.bin("claude")); err == nil {
		t.Fatal("link left")
	}
}

func TestNPMVerifyLeavesMissingCopyToTheBakedOne(t *testing.T) {
	f := newNPM(t, allPins)
	f.putIn(f.env.BakedBin, "claude", "#!/bin/sh\necho 1\n")
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitOK || f.statusText() != "" || f.npmLog() != "" {
		t.Fatalf("rc=%d status=%q log=%q", rc, f.statusText(), f.npmLog())
	}
}

func TestNPMVerifyRemovesBrokenLinkShadowingBakedCopy(t *testing.T) {
	f := newNPM(t, map[string]string{})
	f.putIn(f.env.BakedBin, "claude", "#!/bin/sh\necho 1\n")
	if err := os.MkdirAll(filepath.Dir(f.bin("claude")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.home, "gone"), f.bin("claude")); err != nil {
		t.Fatal(err)
	}
	f.env.NPMVerify([]string{"claude"}, false)
	if _, err := os.Lstat(f.bin("claude")); err == nil {
		t.Fatal("dangling link left shadowing the baked copy")
	}
}

func TestNPMVerifyRepairNoSpaceSignals(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0"})
	f.put("claude", brokenCLI)
	f.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(f.statusText(), "claude\tfailed\tno_space\n") || f.npmLog() != "" {
		t.Fatalf("status=%q log=%q", f.statusText(), f.npmLog())
	}
}

func TestNPMVerifyPresentOnlyClearsEarlierFailure(t *testing.T) {
	f := newNPM(t, allPins)
	f.put("claude", "#!/bin/sh\necho 1\n")
	f.put("codex", "#!/bin/sh\necho 1\n")
	f.env.note("claude", statusSkip, "")
	f.env.note("codex", statusFailed, ReasonOther)
	if rc := f.env.NPMVerifyPresent(); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	st := f.statusText()
	if strings.Contains(st, "claude\tok") {
		t.Errorf("a working copy lost its skipped status: %q", st)
	}
	if !strings.HasSuffix(st, "codex\tok\t\n") {
		t.Errorf("failed status not cleared: %q", st)
	}
	if strings.Contains(st, "opencode") || strings.Contains(st, "copilot") {
		t.Errorf("CLIs with no ~/.local copy were touched: %q", st)
	}
}

func TestNPMFailedRepairRunIsNotRecordedRepaired(t *testing.T) {
	f := newNPM(t, map[string]string{"codex": "3.0.0", "claude": "1.0.0"})
	f.stub("body.codex", brokenCLI)
	// The repair run places claude's copy, then dies on codex's tarball.
	f.stub("failafter", "npm error code EINTEGRITY\n")
	f.put("claude", brokenCLI)
	if rc := f.env.NPMVerify([]string{"claude", "codex"}, false); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	st := f.statusText()
	if strings.Contains(st, "\tok\t") || strings.Contains(f.out.String(), "repaired") {
		t.Fatalf("a failed repair run was recorded repaired: status=%q out=%s", st, f.out)
	}
	if !strings.Contains(st, "claude\tfailed\tchecksum\n") {
		t.Fatalf("status=%q", st)
	}
}

func TestNPMVerifyNoSpaceOutranksFailure(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0"}) // codex has no pin: removed, failed
	f.put("claude", brokenCLI)
	f.put("codex", brokenCLI)
	f.env.FreeMiB = func(string) (int64, bool) { return 10, true }
	if rc := f.env.NPMVerify([]string{"claude", "codex"}, false); rc != ExitNoSpace {
		t.Fatalf("rc=%d, want no-space to win", rc)
	}
	if !strings.Contains(f.statusText(), "codex\tfailed\t") {
		t.Fatalf("status=%q", f.statusText())
	}
}

func TestTailWriterKeepsTheEnd(t *testing.T) {
	w := &tailWriter{max: 10}
	for i := 0; i < 50; i++ {
		w.Write([]byte("warn warn\n"))
	}
	w.Write([]byte("EINTEGRITY"))
	if s := w.String(); len(s) > 10 || !strings.HasSuffix(s, "EINTEGRITY") {
		t.Fatalf("got %q", s)
	}
}

// A failing npm whose postinstall child still holds the pipes must not leave the child running.
func TestNPMInstallFailureKillsStragglers(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0"})
	pidFile := filepath.Join(f.dir, "child.pid")
	f.write(f.env.NPM, "#!/bin/sh\nsleep 30 &\necho $! >"+pidFile+"\necho 'npm error code EINTEGRITY' >&2\nexit 1\n")
	os.Chmod(f.env.NPM, 0o755)
	if rc := NPM(f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	fmt.Sscan(strings.TrimSpace(string(b)), &pid)
	waitGone(t, pid)
}

func TestNPMVerifyUnremovableCopyIsReportedNotRemoved(t *testing.T) {
	f := newNPM(t, map[string]string{})
	if err := os.MkdirAll(filepath.Join(f.bin("claude"), "sub"), 0o755); err != nil { // a directory: runs() fails, Remove fails
		t.Fatal(err)
	}
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	out := f.out.String()
	if !strings.Contains(out, "could not remove") || strings.Contains(out, "removed its ~/.local link") {
		t.Fatalf("out=%s", out)
	}
}

func TestNPMVerifyPresentRepairsOnlyTheBrokenCopy(t *testing.T) {
	f := newNPM(t, allPins)
	f.put("claude", "#!/bin/sh\necho 1\n")
	f.put("codex", brokenCLI)
	f.env.note("claude", statusSkip, "")
	if rc := f.env.NPMVerifyPresent(); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	log := f.npmLog()
	if !strings.Contains(log, "@openai/codex@3.0.0") || strings.Contains(log, "claude") {
		t.Fatalf("npm log %q: want the codex pin only", log)
	}
	st := f.statusText()
	if !strings.HasSuffix(st, "codex\tok\t\n") || strings.Contains(st, "claude\tok") || !f.env.runs(f.bin("codex")) {
		t.Fatalf("status=%q", st)
	}
	// The healthy copy still runs and keeps its earlier skipped status.
	if !f.env.runs(f.bin("claude")) || f.env.lastStatus("claude") != statusSkip {
		t.Fatalf("healthy claude: runs=%v status=%q", f.env.runs(f.bin("claude")), st)
	}
}

func TestNPMVerifyReinstallsAMissingCopyWithoutABakedOne(t *testing.T) {
	f := newNPM(t, allPins)
	f.put("claude", "#!/bin/sh\necho 1\n") // codex's copy is gone entirely
	if rc := f.env.NPMVerify([]string{"claude", "codex"}, false); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	log := f.npmLog()
	if !strings.Contains(log, "@openai/codex@3.0.0") || strings.Contains(log, "claude") {
		t.Fatalf("npm log %q", log)
	}
	st := f.statusText()
	if !strings.Contains(st, "claude\tok\t") || !strings.Contains(st, "codex\tok\t") || !f.env.runs(f.bin("codex")) || !f.env.runs(f.bin("claude")) {
		t.Fatalf("status=%q", st)
	}
}

// Space is enough for the install and gone by the repair: the repair never runs, the broken link
// goes, and the outcome depends on whether a baked copy can take over.
func TestNPMRepairNoSpaceAfterInstall(t *testing.T) {
	for _, baked := range []bool{false, true} {
		f := newNPM(t, map[string]string{"codex": "3.0.0"})
		f.stub("body.codex", brokenCLI)
		calls := 0
		f.env.FreeMiB = func(string) (int64, bool) {
			calls++
			if calls == 1 {
				return 100000, true // the install's own check
			}
			return 10, true
		}
		want := "codex\tfailed\tno_space\n"
		if baked {
			// A baked copy takes over once the broken link is gone, so the install's check is not the
			// first call: the install is skipped for the present copy, and the repair finds no space.
			f.putIn(f.env.BakedBin, "codex", "#!/bin/sh\necho baked\n")
			f.put("codex", brokenCLI)
			calls = 1
			want = "codex\tskipped\tno_space\n"
		}
		var rc int
		if baked {
			rc = f.env.NPMVerify([]string{"codex"}, false)
		} else {
			rc = NPM(f.env)
		}
		if rc != ExitNoSpace {
			t.Fatalf("baked=%v: rc=%d out=%s", baked, rc, f.out)
		}
		// Without a baked copy only the first install ran; with one, no npm run at all.
		wantRuns := 1
		if baked {
			wantRuns = 0
		}
		if n := strings.Count(f.npmLog(), "install"); n != wantRuns {
			t.Fatalf("baked=%v: %d npm runs, want %d: %q", baked, n, wantRuns, f.npmLog())
		}
		if _, err := os.Lstat(f.bin("codex")); err == nil {
			t.Fatalf("baked=%v: broken link kept", baked)
		}
		if !strings.HasSuffix(f.statusText(), want) {
			t.Fatalf("baked=%v: status=%q want suffix %q", baked, f.statusText(), want)
		}
	}
}

func TestNPMSkipsACLIWhoseRestoreIsPending(t *testing.T) {
	f := newNPM(t, allPins)
	if err := os.MkdirAll(filepath.Join(f.home, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.env.npmWriteMarker("pending", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	f.put("opencode", "#!/bin/sh\necho 2.0.0\n")
	f.put("codex", "#!/bin/sh\necho 3.0.0\n")
	// copilot is missing: it alone is installed; claude's previous copy is still waiting.
	f.env.Repin = true
	NPM(f.env)
	log := f.npmLog()
	if !strings.Contains(log, "@github/copilot@4.0.0") || strings.Contains(log, "claude-code") {
		t.Fatalf("npm log %q: want the copilot pin only", log)
	}
}

// breakLock makes the lock path unopenable for any uid (a directory cannot be opened O_RDWR).
func breakLock(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNPMLockOpenErrorIsNeitherBusyNorFree(t *testing.T) {
	f := newNPM(t, allPins)
	breakLock(t, f.env.npmLockPath())
	e, _, ok := f.env.lockNPM()
	if ok || e.npmLockErr == nil {
		t.Fatalf("an unopenable lock file was reported as free: ok=%v err=%v", ok, e.npmLockErr)
	}
}

func TestNPMWithUnopenableLockDoesNothingDestructive(t *testing.T) {
	f := newNPM(t, allPins)
	breakLock(t, f.env.npmLockPath())
	if rc := NPM(f.env); rc != ExitFailed || f.npmLog() != "" {
		t.Fatalf("rc=%d npm ran=%q out=%s", rc, f.npmLog(), f.out)
	}
	if !strings.Contains(f.out.String(), "cannot open") {
		t.Errorf("no reason given: %q", f.out)
	}
	// No space is still answered without writing.
	f.env.FreeMiB = func(string) (int64, bool) { return 100, true }
	if rc := NPM(f.env); rc != ExitNoSpace {
		t.Fatalf("no-space rc=%d", rc)
	}
	// Nothing to install stays a read-only no-op.
	g := newNPM(t, allPins)
	for _, c := range []string{"claude", "opencode", "codex", "copilot"} {
		g.put(c, "#!/bin/sh\necho 1\n")
	}
	breakLock(t, g.env.npmLockPath())
	if rc := NPM(g.env); rc != ExitOK {
		t.Fatalf("no-op rc=%d out=%s", rc, g.out)
	}
}

func TestNPMVerifyWithUnopenableLockKeepsABrokenLinkAndPassesHealthyOnes(t *testing.T) {
	f := newNPM(t, allPins)
	f.put("claude", brokenCLI)
	f.put("codex", "#!/bin/sh\necho 1\n")
	breakLock(t, f.env.npmLockPath())
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitFailed {
		t.Fatalf("broken: rc=%d", rc)
	}
	if _, err := os.Lstat(f.bin("claude")); err != nil || f.npmLog() != "" {
		t.Fatalf("a repair ran without the lock (err=%v log=%q)", err, f.npmLog())
	}
	if rc := f.env.NPMVerify([]string{"codex"}, false); rc != ExitOK {
		t.Fatalf("healthy: rc=%d out=%s", rc, f.out)
	}
}

func TestNPMRereadsItsPlanOnceTheLockIsHeld(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0"})
	// A self-update that held the lock meanwhile finished the install.
	f.env.afterNPMLock = func() { f.put("claude", "#!/bin/sh\necho 1.0.0\n") }
	if rc := NPM(f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if f.npmLog() != "" {
		t.Fatalf("redundant reinstall: %q", f.npmLog())
	}
}

func TestNPMVerifyUnopenableLockProbesOnceAndFallsBackToBaked(t *testing.T) {
	f := newNPM(t, allPins)
	f.putIn(f.env.BakedBin, "claude", "#!/bin/sh\necho 1\n")
	breakLock(t, f.env.npmLockPath())
	// No ~/.local copy and a working baked one: as without the lock, nothing to do.
	if rc := f.env.NPMVerify([]string{"claude"}, false); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	// A link that runs on the first probe and fails on the next must not reach a repair.
	probes := filepath.Join(f.dir, "probes")
	f.put("codex", fmt.Sprintf("#!/bin/sh\necho x >>%q\n[ $(wc -l <%q) -le 1 ] || exit 1\necho 1\n", probes, probes))
	if rc := f.env.NPMVerify([]string{"codex"}, false); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Lstat(f.bin("codex")); err != nil || f.npmLog() != "" {
		t.Fatalf("a second probe led to a repair: err=%v log=%q", err, f.npmLog())
	}
}

func TestNPMRecheckKeepsTheDriftWhenNpmLsIsEmpty(t *testing.T) {
	f := newNPM(t, map[string]string{"codex": "3.0.0"})
	f.put("codex", "#!/bin/sh\necho 1\n")
	f.stub("ls.json", `{"dependencies":{"@openai/codex":{"version":"2.0.0"}}}`)
	f.env.Repin = true
	f.env.afterNPMLock = func() { os.Remove(filepath.Join(f.dir, "ls.json")) } // npm ls now answers nothing
	if rc := NPM(f.env); rc != ExitOK || !strings.Contains(f.npmLog(), "@openai/codex@3.0.0") {
		t.Fatalf("drift dropped: rc=%d log=%q out=%s", rc, f.npmLog(), f.out)
	}
}

func TestNPMRecordsEachSkipOnce(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0", "codex": "3.0.0"})
	f.put("claude", "#!/bin/sh\necho 1\n")
	NPM(f.env)
	if n := strings.Count(f.statusText(), "claude\tskipped\t"); n != 1 {
		t.Fatalf("skipped recorded %d times: %q", n, f.statusText())
	}
}

func TestNPMRecheckAddsACLIThatDriftedWhileWaitingForTheLock(t *testing.T) {
	f := newNPM(t, map[string]string{"claude": "1.0.0", "codex": "3.0.0"})
	f.put("claude", "#!/bin/sh\necho 1\n") // at the pin on the first look
	f.stub("ls.json", `{"dependencies":{"@anthropic-ai/claude-code":{"version":"1.0.0"}}}`)
	f.env.Repin = true
	// A self-update that held the lock moved claude off the pin; codex still needs installing.
	f.env.afterNPMLock = func() {
		f.stub("ls.json", `{"dependencies":{"@anthropic-ai/claude-code":{"version":"9.9.9"}}}`)
	}
	NPM(f.env)
	if log := f.npmLog(); !strings.Contains(log, "@anthropic-ai/claude-code@1.0.0") || !strings.Contains(log, "@openai/codex@3.0.0") {
		t.Fatalf("npm asked for %q", log)
	}
}

func TestNPMVerifyReadOnlyKeepsAnEarlierNoSpaceReason(t *testing.T) {
	f := newNPM(t, allPins)
	f.put("claude", brokenCLI)
	f.env.note("claude", statusFailed, ReasonNoSpace)
	breakLock(t, f.env.npmLockPath())
	f.env.NPMVerify([]string{"claude"}, true)
	if !strings.HasSuffix(f.statusText(), "claude\tfailed\tno_space\n") {
		t.Fatalf("status=%q", f.statusText())
	}
}
