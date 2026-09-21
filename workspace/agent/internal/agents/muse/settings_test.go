package muse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// museHome points ConfigHome at a throwaway directory. paths.HomeDir reads the environment on
// every call, so this is enough to keep a test off the member's own settings.
func museHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".config", "muse")
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

func TestEnsureClampsWritesEveryOwnedKey(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatalf("EnsureClamps: %v", err)
	}
	m := readJSONFile(t, settingsPath())

	// The file requires this or every muse command fails at startup.
	if m["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v", m["schema_version"])
	}
	for _, c := range ownedClamps() {
		got, ok := getPath(m, c.path)
		if !ok {
			t.Errorf("%s is missing from the written file", pathString(c.path))
			continue
		}
		if got != c.value {
			t.Errorf("%s = %v, want %v", pathString(c.path), got, c.value)
		}
	}
}

// Both keys, not one: either alone leaves the other half of the foreign personal context on,
// and that half is the member's own ~/.claude/CLAUDE.md going to Meta.
func TestForeignContextTakesBothKeys(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(t, settingsPath())
	for _, key := range []string{"foreign_personal_rules", "foreign_personal_skills"} {
		v, ok := getPath(m, []string{"context", key})
		if !ok || v != false {
			t.Errorf("context.%s = %v (present=%v), want false", key, v, ok)
		}
	}
}

// A member's own configuration has to survive an AF write — that is what separates a merge
// from a reset.
func TestEnsureClampsPreservesKeysItDoesNotOwn(t *testing.T) {
	home := museHome(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	original := map[string]any{
		"schema_version": float64(1),
		"tui":            map[string]any{"theme": "dark"},
		"model":          map[string]any{"default": "muse-spark-1.3"},
		// Decision 11 puts AF's own servers on the wire, so a member's block is theirs alone.
		"mcp_servers": map[string]any{
			"mine": map[string]any{"transport": "stdio", "command": "my-server"},
		},
		// A key inside a section AF writes into must survive too.
		"run": map[string]any{"something_of_theirs": "keep me"},
	}
	b, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(filepath.Join(home, "settings.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureClamps(); err != nil {
		t.Fatalf("EnsureClamps: %v", err)
	}
	m := readJSONFile(t, settingsPath())

	if v, _ := getPath(m, []string{"tui", "theme"}); v != "dark" {
		t.Errorf("tui.theme = %v, want dark", v)
	}
	if v, _ := getPath(m, []string{"model", "default"}); v != "muse-spark-1.3" {
		t.Errorf("model.default = %v", v)
	}
	if v, _ := getPath(m, []string{"mcp_servers", "mine", "command"}); v != "my-server" {
		t.Errorf("the member's mcp_servers block was lost: %v", m["mcp_servers"])
	}
	if v, _ := getPath(m, []string{"run", "something_of_theirs"}); v != "keep me" {
		t.Errorf("a sibling key in a section AF writes into was lost: %v", m["run"])
	}
	// ...and the clamps still landed.
	if v, _ := getPath(m, []string{"run", "workflow_trigger_mode"}); v != "off" {
		t.Errorf("run.workflow_trigger_mode = %v", v)
	}
}

// AF writes no mcp_servers of its own: decision 11 puts servers on the wire, and a file block
// would be a second, diverging source for the same thing.
func TestEnsureClampsWritesNoMCPServers(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(t, settingsPath())
	if _, ok := m["mcp_servers"]; ok {
		t.Error("AF wrote an mcp_servers block; servers belong on the wire")
	}
}

func TestEnsureClampsIsIdempotent(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("a second EnsureClamps rewrote the file differently")
	}
}

// A member (or a muse command) that turns a clamp back off must be re-clamped on the next
// Resume — that is why this runs every time rather than once.
func TestEnsureClampsRepairsATamperedValue(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(t, settingsPath())
	setPath(m, []string{"run", "subagent_delegation_mode"}, "auto")
	b, _ := json.Marshal(m)
	if err := os.WriteFile(settingsPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	m = readJSONFile(t, settingsPath())
	if v, _ := getPath(m, []string{"run", "subagent_delegation_mode"}); v != "off" {
		t.Errorf("subagent_delegation_mode = %v, want the clamp restored", v)
	}
}

// Overwriting a file we could not parse would silently discard a member's own configuration,
// so a corrupt file is a refusal, not a reset.
func TestCorruptSettingsRefuseRatherThanReset(t *testing.T) {
	home := museHome(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{ this is not json")
	if err := os.WriteFile(filepath.Join(home, "settings.json"), broken, 0o644); err != nil {
		t.Fatal(err)
	}

	err := EnsureClamps()
	if err == nil {
		t.Fatal("a corrupt settings file was silently overwritten")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("the error does not name the file: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	if string(got) != string(broken) {
		t.Error("the member's file was modified despite the refusal")
	}
}

// An empty file is a file muse itself would treat as an empty document, not a corrupt one.
func TestEmptySettingsFileIsAnEmptyDocument(t *testing.T) {
	home := museHome(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureClamps(); err != nil {
		t.Fatalf("EnsureClamps on an empty file: %v", err)
	}
	if v, _ := getPath(readJSONFile(t, settingsPath()), []string{"run", "workflow_trigger_mode"}); v != "off" {
		t.Error("the clamps did not land on an empty file")
	}
}

// The lock is muse's own protocol, and AF has to actually take it — otherwise two writers
// read-merge-write the same file and one merge disappears.
func TestEnsureClampsTakesTheLockFile(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsLock()); err != nil {
		t.Errorf("the lock sidecar was not created: %v", err)
	}
}

// Held-lock behaviour, both directions: AF must WAIT for a lock another process holds (failing
// fast would refuse a launch because the member ran a muse command), and it must release it
// (muse blocks indefinitely on a held lock, so a leak hangs every muse command they type).
func TestEnsureClampsWaitsForAndReleasesTheLock(t *testing.T) {
	home := museHome(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(settingsLock(), os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- EnsureClamps() }()

	select {
	case err := <-done:
		t.Fatalf("EnsureClamps did not wait for the held lock (returned %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureClamps: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("EnsureClamps never proceeded after the lock was released")
	}

	// Released: a fresh non-blocking acquire must succeed.
	again, err := os.OpenFile(settingsLock(), os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := syscall.Flock(int(again.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("the lock was not released: %v", err)
	}
	syscall.Flock(int(again.Fd()), syscall.LOCK_UN)
}

// Several sessions resume at once on boot. Without AF's own mutex each would read-merge-write
// the same file and the last writer would win with a stale read.
func TestConcurrentEnsureClampsConverge(t *testing.T) {
	museHome(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := EnsureClamps(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent EnsureClamps: %v", err)
	}
	m := readJSONFile(t, settingsPath())
	for _, c := range ownedClamps() {
		if got, ok := getPath(m, c.path); !ok || got != c.value {
			t.Errorf("%s = %v (present=%v) after concurrent writes", pathString(c.path), got, ok)
		}
	}
}

// The verify pass is the whole reason the write is not assumed to have landed: muse reads the
// file before taking the lock, so a concurrent muse write can silently drop AF's merge. This
// drives that detection directly.
func TestVerifyReportsAClampThatDidNotSurvive(t *testing.T) {
	museHome(t)
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	m := readJSONFile(t, settingsPath())
	delete(m["run"].(map[string]any), "workflow_trigger_mode")
	b, _ := json.Marshal(m)
	os.WriteFile(settingsPath(), b, 0o644)

	missing, err := verifyClamps()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "run.workflow_trigger_mode" {
		t.Errorf("missing = %v, want exactly run.workflow_trigger_mode", missing)
	}
}

// setPath has to build the sections it needs, and must not be defeated by a non-object where
// a section belongs — muse would refuse to load such a file anyway.
func TestSetPathCreatesAndReplaces(t *testing.T) {
	m := map[string]any{"run": "not an object"}
	setPath(m, []string{"run", "workflow_trigger_mode"}, "off")
	if v, ok := getPath(m, []string{"run", "workflow_trigger_mode"}); !ok || v != "off" {
		t.Errorf("m = %v", m)
	}
	setPath(m, []string{"a", "b", "c"}, float64(1))
	if v, ok := getPath(m, []string{"a", "b", "c"}); !ok || v != float64(1) {
		t.Errorf("m = %v", m)
	}
	if _, ok := getPath(m, []string{"a", "b", "missing"}); ok {
		t.Error("getPath found a key that is not there")
	}
	if _, ok := getPath(m, []string{"a", "b", "c", "deeper"}); ok {
		t.Error("getPath walked into a scalar")
	}
}

// The activation key is a pack-qualified path, not a skill id. Getting it wrong is silently
// accepted by the file and leaves the skill enabled.
func TestBundledSkillKeyIsThePackQualifiedPath(t *testing.T) {
	if got := bundledSkillKey("resume-claude"); got != "bundled://muse-core/skills/resume-claude/SKILL.md" {
		t.Errorf("bundledSkillKey = %q", got)
	}
}

// read-session reads MUSE's own store and its own description says never to probe ~/.claude or
// ~/.codex, so it is not a foreign reader. Clamping it would remove a working feature for a
// reason that does not apply to it.
func TestForeignReaderSetIsTheForeignOnesOnly(t *testing.T) {
	want := map[string]bool{"resume-claude": true, "resume-codex": true, "import": true, "migrate": true}
	if len(foreignReaderSkills) != len(want) {
		t.Fatalf("foreignReaderSkills = %v", foreignReaderSkills)
	}
	for _, s := range foreignReaderSkills {
		if !want[s] {
			t.Errorf("%q is not a foreign reader", s)
		}
	}
	for _, s := range foreignReaderSkills {
		if s == "read-session" {
			t.Error("read-session reads muse's own store, not another agent's")
		}
	}
}

// The fail-close of decision 6, and the reason it lives in Resume: these keys are the safety
// mechanism, so a session that could not be clamped must not start at all. Without this the
// driver would spawn a host with eight subagents, workflows and the bundled foreign readers
// enabled, and nothing would say so.
func TestResumeRefusesWhenTheClampsCannotBeWritten(t *testing.T) {
	home := museHome(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file AF cannot parse is a file it refuses to overwrite, which makes EnsureClamps fail.
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Installed() must pass, or the refusal would come from the missing binary instead and
	// this test would prove nothing. /bin/true exists and is never actually spawned, because
	// the clamp gate runs first.
	t.Setenv("AGENT_MUSE_BIN", "/bin/true")

	dir := t.TempDir()
	_, err := NewDriver().Resume(session.Meta{Kind: session.KindMuse, Name: "clamp-gate", Dir: dir})
	if err == nil {
		t.Fatal("Resume started a session whose clamps could not be written")
	}
	if !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
	if ManagedAlive("clamp-gate") {
		t.Error("a handle was left alive after the refusal")
	}
}

// The gate must not be a one-shot: kiro's own ensureSettings used a sync.Once, and the defect
// that hides is a session started after a member edited the file running unclamped.
func TestTheClampGateRunsOnEveryResume(t *testing.T) {
	home := museHome(t)
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	// Tamper, then ask again — a Once-guarded writer would leave the tampered value in place.
	m := readJSONFile(t, settingsPath())
	setPath(m, []string{"run", "workflow_trigger_mode"}, "auto")
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(home, "settings.json"), b, 0o644)

	if err := EnsureClamps(); err != nil {
		t.Fatal(err)
	}
	if v, _ := getPath(readJSONFile(t, settingsPath()), []string{"run", "workflow_trigger_mode"}); v != "off" {
		t.Errorf("workflow_trigger_mode = %v after a second EnsureClamps", v)
	}
}
