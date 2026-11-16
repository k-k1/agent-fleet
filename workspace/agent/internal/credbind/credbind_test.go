package credbind

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func freshHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// The bindings are stable for one credential and Workspace, change with another key, account,
// endpoint or generation, carry no input, and cannot be rebuilt by hashing candidates without the
// Workspace's key (ADR 0138 D5b).
func TestBindings(t *testing.T) {
	freshHome(t)
	k1 := Key("ANTHROPIC_API_KEY", "1234")
	if k1 == "" || k1 != Key("ANTHROPIC_API_KEY", " 1234 ") || k1 == Key("ANTHROPIC_API_KEY", "1235") ||
		!strings.HasPrefix(k1, "ANTHROPIC_API_KEY@") || len(k1) != len("ANTHROPIC_API_KEY@")+16 {
		t.Fatalf("Key: %q", k1)
	}
	if Key("codex-api-key", "1234") == k1 {
		t.Error("two slots holding one key must stay apart")
	}
	// A candidate search over plain digests of every four-digit key finds nothing.
	digest := strings.TrimPrefix(k1, "ANTHROPIC_API_KEY@")
	for i := 0; i < 10000; i++ {
		c := []byte{byte('0' + i/1000), byte('0' + i/100%10), byte('0' + i/10%10), byte('0' + i%10)}
		h := sha256.Sum256(c)
		if hex.EncodeToString(h[:])[:16] == digest {
			t.Fatalf("the binding is a plain digest of %q", c)
		}
	}
	a := Account("agy-login", "a@example.com")
	if a == "" || strings.Contains(a, "example") || a == Account("agy-login", "b@example.com") || a == Account("cursor-login", "a@example.com") {
		t.Errorf("Account: %q", a)
	}
	e := Endpoint("member-llamacpp", "http://gpu.example:8080/", "k")
	if e != Endpoint("member-llamacpp", "http://gpu.example:8080", "k") || e == Endpoint("member-llamacpp", "http://gpu.example:8080", "k2") ||
		e == Endpoint("member-llamacpp", "http://other.example:8080", "k") || strings.Contains(e, "gpu") {
		t.Errorf("Endpoint: %q", e)
	}
	if Key("X", " ") != "" || Account("x", "") != "" || Endpoint("x", "", "k") != "" {
		t.Error("an empty credential has no binding")
	}
	// Another Workspace (another key) gives other bindings for the same credential.
	freshHome(t)
	if Key("ANTHROPIC_API_KEY", "1234") == k1 {
		t.Error("the binding does not depend on the Workspace's key")
	}
}

// The generation fallback: stable until the Agent bumps it, per login.
func TestGeneration(t *testing.T) {
	freshHome(t)
	g := Generation("muse-login")
	if g == "" || g != Generation("muse-login") || g != AccountOrGeneration("muse-login", "") {
		t.Fatalf("Generation: %q", g)
	}
	bump("codex-login-other")
	if Generation("muse-login") != g {
		t.Error("bumping another login moved this one")
	}
	bump("muse-login")
	if n := Generation("muse-login"); n == g || n == "" {
		t.Errorf("a bump kept the binding: %q", n)
	}
	if AccountOrGeneration("muse-login", "a@example.com") != Account("muse-login", "a@example.com") {
		t.Error("an account id wins over the generation")
	}
}

// The key is created once, private, and reused; one that cannot be read leaves bindings unproven
// rather than weaker.
func TestKeyFile(t *testing.T) {
	freshHome(t)
	first := Key("S", "v")
	p := filepath.Join(paths.AgentConfigDir(), keyFile)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	keyMu.Lock()
	keyVal = nil
	keyMu.Unlock()
	if Key("S", "v") != first {
		t.Error("the key was not reused after a reload")
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyMu.Lock()
	keyVal = nil
	keyMu.Unlock()
	if b := Key("S", "v"); b != "" {
		t.Errorf("a corrupt key still produced %q", b)
	}
}

// A generation state that cannot be trusted serves no binding rather than generation zero, and a
// Bump rebuilds it under a new epoch, so no login is handed a value an earlier state used.
func TestGenerationFailurePaths(t *testing.T) {
	freshHome(t)
	p := filepath.Join(paths.AgentConfigDir(), genFile)
	g0 := Generation("muse-login")
	other0 := Generation("agy-login")
	bump("muse-login")
	g1 := Generation("muse-login")
	if g1 == "" || g1 == g0 || Generation("agy-login") != other0 {
		t.Fatalf("bump: %q -> %q, agy %q", g0, g1, Generation("agy-login"))
	}
	for name, body := range map[string]string{"malformed": "{", "null": "null", "no logins": `{"epoch":"x"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if g := Generation("muse-login"); g != "" {
			t.Errorf("%s state: Generation = %q, want unproven", name, g)
		}
	}
	// Recovery: a Bump rebuilds the state; neither login gets back any earlier binding.
	bump("muse-login")
	for _, g := range []string{Generation("muse-login"), Generation("agy-login")} {
		if g == "" || g == g0 || g == g1 || g == other0 {
			t.Errorf("after a rebuild: %q reuses an earlier binding (%q %q %q)", g, g0, g1, other0)
		}
	}
	// A Bump that cannot persist leaves the login unproven, never on its replaced binding, and
	// removes the state so a restart (the in-memory mark gone) starts a fresh epoch.
	beforeFail := Generation("cursor-login")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	bump("cursor-login")
	if g := Generation("cursor-login"); g != "" {
		t.Errorf("after a failed bump: %q, want unproven", g)
	}
	genMu.Lock()
	genPoisoned = map[string]bool{} // a restart
	genMu.Unlock()
	if g := Generation("cursor-login"); g == "" || g == beforeFail {
		t.Errorf("after a failed bump and a restart: %q, want a fresh binding (old %q)", g, beforeFail)
	}
	bump("cursor-login")
	if g := Generation("cursor-login"); g == "" {
		t.Error("a successful bump did not clear the failure")
	}
	t.Cleanup(func() { genMu.Lock(); genPoisoned = map[string]bool{}; genMu.Unlock() })
}

// A lost state file never reissues a predecessor's binding: the next read starts and persists a
// fresh epoch, which then holds across a reload.
func TestGenerationAfterTheStateIsLost(t *testing.T) {
	freshHome(t)
	p := filepath.Join(paths.AgentConfigDir(), genFile)
	g0 := Generation("muse-login")
	bump("muse-login")
	g1 := Generation("muse-login")
	if g1 == "" || g1 == g0 {
		t.Fatalf("bump: %q -> %q", g0, g1)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	g2 := Generation("muse-login")
	if g2 == "" || g2 == g0 || g2 == g1 {
		t.Errorf("after the state was lost: %q, reissues %q or %q", g2, g0, g1)
	}
	ResetForTest() // a reload of the binding key; the state is read from disk again
	if g := Generation("muse-login"); g != g2 {
		t.Errorf("after a reload: %q, want the persisted %q", g, g2)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the fresh epoch was not persisted: %v", err)
	}
}

// A login change whose new generation cannot be persisted, and whose state cannot be removed
// either, must not come back after an Agent restart as the replaced login's binding: the pending
// marker Begin wrote before the change keeps it unproven until a change finishes.
func TestGenerationAfterAFailedPersistAndARestart(t *testing.T) {
	freshHome(t)
	dir := paths.AgentConfigDir()
	old := Generation("muse-login")
	if old == "" {
		t.Fatal("no starting generation")
	}
	op, err := Begin("muse-login")
	if err != nil {
		t.Fatal(err)
	}
	if g := Generation("muse-login"); g != "" {
		t.Errorf("between Begin and Finish: %q, want unproven", g)
	}
	// The directory turns read-only: the state stays readable (it still holds the old
	// generation), but it can be neither rewritten nor removed.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	op.Finish()
	if _, err := os.Stat(filepath.Join(dir, genFile)); err != nil {
		t.Fatalf("positive control: the state should still be on disk: %v", err)
	}
	if st, ok := readGenerations(); !ok || st.Epoch == "" {
		t.Fatalf("positive control: the old state should still be readable: %+v %v", st, ok)
	}
	restart()
	if g := Generation("muse-login"); g != "" {
		t.Errorf("after a failed persist and a restart: %q, want unproven (the old one was %q)", g, old)
	}
	// The directory recovers; the next completed change records a fresh generation and clears
	// the earlier process's marker with its own.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	next, err := Begin("muse-login")
	if err != nil {
		t.Fatal(err)
	}
	next.Finish()
	if g := Generation("muse-login"); g == "" || g == old {
		t.Errorf("after a completed change: %q (old %q)", g, old)
	}
	if !markerAbsent("muse-login") {
		t.Error("the completed change left a pending marker")
	}
}

// restart forgets everything this process holds (the poison marks, the live operations, the
// cached key), as an Agent restart does; the files stay what they are.
func restart() {
	genMu.Lock()
	genPoisoned = map[string]bool{}
	genMu.Unlock()
	opMu.Lock()
	liveOps = map[string]bool{}
	opMu.Unlock()
	ResetForTest()
}

// Begin refuses when it cannot record the change, so the caller does not make it; a begun change
// that finishes clears its marker; and Finish is safe on nil and twice.
func TestBegin(t *testing.T) {
	freshHome(t)
	g0 := Generation("agy-login")
	op, err := Begin("agy-login")
	if err != nil {
		t.Fatal(err)
	}
	op.Finish()
	op.Finish()
	var none *Op
	none.Finish()
	if g := Generation("agy-login"); g == "" || g == g0 || !markerAbsent("agy-login") {
		t.Errorf("begin then finish: %q (was %q), marker absent %v", g, g0, markerAbsent("agy-login"))
	}
	dir := paths.AgentConfigDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := Begin("kiro-login"); err == nil {
		t.Error("Begin succeeded in a directory it cannot write")
	}
}

// Two changes of one login in flight at once (a device login left open while a disconnect
// completes): the one that finishes first does not clear the other's protection, which a crash
// before the second finishes would otherwise turn into the first one's binding.
func TestOverlappingChangesStayUnproven(t *testing.T) {
	freshHome(t)
	g0 := Generation("codex-chatgpt")
	login, err := Begin("codex-chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	disconnect, err := Begin("codex-chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	disconnect.Finish()
	if g := Generation("codex-chatgpt"); g != "" {
		t.Errorf("one change finished, one still in flight: %q, want unproven", g)
	}
	restart() // the in-flight login dies with the Agent, its marker stays on disk
	if g := Generation("codex-chatgpt"); g != "" {
		t.Errorf("after a restart with the login still begun: %q, want unproven", g)
	}
	// The dead operation's marker is cleared by the next completed change, which records a
	// generation nobody had.
	next, err := Begin("codex-chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	next.Finish()
	if g := Generation("codex-chatgpt"); g == "" || g == g0 || !markerAbsent("codex-chatgpt") {
		t.Errorf("after the next completed change: %q, marker absent %v", g, markerAbsent("codex-chatgpt"))
	}
	_ = login // never finished: it died with the earlier process
}

// The protocol's order under a machine crash: the marker's file and directory are synced before
// Begin returns; the new state's file is synced before its rename and the directory after it; and
// only then is the marker removed and the directory synced again. A failing sync fails closed.
func TestDurableOrder(t *testing.T) {
	freshHome(t)
	_ = Generation("kiro-login") // start the state outside the recording
	var events []string
	oldF, oldD, oldR := syncFile, syncDir, removeFile
	t.Cleanup(func() { syncFile, syncDir, removeFile = oldF, oldD, oldR })
	name := func(p string) string {
		b := filepath.Base(p)
		switch {
		case strings.HasPrefix(b, pendingPrefix):
			return "marker"
		case strings.HasPrefix(b, genFile+"."):
			return "state"
		}
		return b
	}
	syncFile = func(f *os.File) error { events = append(events, "sync:"+name(f.Name())); return oldF(f) }
	syncDir = func(d string) error { events = append(events, "syncdir"); return oldD(d) }
	removeFile = func(p string) error { events = append(events, "remove:"+name(p)); return oldR(p) }
	op, err := Begin("kiro-login")
	if err != nil {
		t.Fatal(err)
	}
	op.Finish()
	want := []string{"sync:marker", "syncdir", "sync:state", "syncdir", "remove:marker", "syncdir"}
	if !slices.Equal(events, want) {
		t.Errorf("order = %v, want %v", events, want)
	}
	// A directory that cannot be synced: Begin refuses and leaves no marker behind.
	syncDir = func(string) error { return errors.New("injected") }
	if _, err := Begin("kiro-login"); err == nil {
		t.Error("Begin succeeded although its marker could not be made durable")
	}
	syncDir = oldD
	if !markerAbsent("kiro-login") {
		t.Error("a refused Begin left a marker")
	}
	// A state that cannot be synced: Finish keeps the marker and the login stays unproven.
	op, err = Begin("kiro-login")
	if err != nil {
		t.Fatal(err)
	}
	syncFile = func(f *os.File) error {
		if name(f.Name()) == "state" {
			return errors.New("injected")
		}
		return oldF(f)
	}
	op.Finish()
	syncFile = oldF
	restart()
	if g := Generation("kiro-login"); g != "" || markerAbsent("kiro-login") {
		t.Errorf("after an unsynced state: %q, marker absent %v; want unproven, marker kept", g, markerAbsent("kiro-login"))
	}
}

// A Begin that arrives while a Finish is cleaning up (here: from inside its removal of another
// login's marker) is not taken for an orphan: it waits for the Finish's transaction, then keeps
// its own marker, and its login stays unproven while it is in flight.
func TestBeginDuringFinishCleanup(t *testing.T) {
	freshHome(t)
	a, err := Begin("alpha", "beta")
	if err != nil {
		t.Fatal(err)
	}
	oldR := removeFile
	t.Cleanup(func() { removeFile = oldR })
	var b *Op
	done := make(chan struct{})
	started := false
	removeFile = func(p string) error {
		if !started && strings.HasPrefix(filepath.Base(p), pendingPrefix+"alpha.") {
			started = true
			go func() {
				defer close(done)
				b, _ = Begin("beta")
			}()
			// Give the Begin every chance to run now, as it would without the transaction lock.
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
			}
		}
		return oldR(p)
	}
	a.Finish()
	<-done
	removeFile = oldR
	if b == nil {
		t.Fatal("the second Begin failed")
	}
	if markerAbsent("beta") {
		t.Error("the Finish removed the marker of a Begin that arrived during its clean-up")
	}
	if g := Generation("beta"); g != "" {
		t.Errorf("beta with a change in flight: %q, want unproven", g)
	}
	if g := Generation("alpha"); g == "" {
		t.Error("alpha has no binding after its change finished")
	}
	b.Finish()
	if g := Generation("beta"); g == "" || !markerAbsent("beta") {
		t.Errorf("beta after its own change finished: %q, marker absent %v", g, markerAbsent("beta"))
	}
}

// No exported function moves a generation outside an Op: a caller without Begin would have only
// process memory to remember a failed persist, which a restart forgets (issue 4189).
func TestNoBumpOutsideAnOp(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					seen++
					if fd.Recv == nil && strings.EqualFold(fd.Name.Name, "bump") && fd.Name.IsExported() {
						t.Errorf("exported %s: a generation must move only through Begin/Finish", fd.Name.Name)
					}
				}
			}
		}
	}
	if seen < 10 {
		t.Fatalf("positive control: parsed only %d functions", seen)
	}
}
