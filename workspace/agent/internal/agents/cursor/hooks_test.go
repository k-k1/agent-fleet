package cursor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const hookChat = "33cf6d19-0000-4000-8000-000000000001"

// hookFixture isolates HOME and the ledger and registers a cursor session.
func hookFixture(t *testing.T, mutate func(*session.Meta)) session.Meta {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	m := session.Meta{Dir: "/home/dev/repos/proj", Name: "slot-a", Kind: session.KindCursor}
	if mutate != nil {
		mutate(&m)
	}
	session.WriteMeta(m)
	return m
}

func stopFixture(t *testing.T, file string) stopInput {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hooks", file))
	if err != nil {
		t.Fatal(err)
	}
	var in stopInput
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestRecordStopWritesACompletedTurn(t *testing.T) {
	m := hookFixture(t, nil)
	recordStop(m.Name, stopFixture(t, "stop_completed_multistep.json"), time.Now())
	rows := usagex.ReadRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Feature != usagex.FeatureSession || r.Kind != session.KindCursor || r.Ref != m.Name {
		t.Fatalf("identity wrong: %+v", r)
	}
	// input_tokens includes the cached prefix: In is the fresh part, so spend does not count it.
	if r.In != 639 || r.Out != 196 || r.CacheRead != 63296 || r.CacheCreate != 0 || r.Spend != 835 {
		t.Fatalf("tokens wrong: %+v", r)
	}
	if r.Key != "cursor-stop:8f432487-0000-4000-8000-000000000002" {
		t.Fatalf("key = %q", r.Key)
	}
	if r.Measured != usagex.MeasuredPartial || !r.TokensOnly || !r.OK {
		t.Fatalf("measured/flags wrong: %+v", r)
	}
	// The raw selector survives; Auto is not turned into a model, and nothing is priced.
	if r.ModelRaw != "default" || r.Model != "" || r.ModelSrc != usagex.ModelUnknown || r.CostUSD != 0 {
		t.Fatalf("model/cost wrong: %+v", r)
	}
}

func TestRecordStopKeepsAnExplicitModel(t *testing.T) {
	m := hookFixture(t, nil)
	in := stopFixture(t, "stop_completed_plain.json")
	in.Model = "gpt-5.3-codex-low-fast"
	recordStop(m.Name, in, time.Now())
	r := usagex.ReadRows()[0]
	if r.Model != in.Model || r.ModelRaw != in.Model || r.ModelReq != in.Model || r.ModelSrc != usagex.ModelRequest {
		t.Fatalf("model wrong: %+v", r)
	}
	if r.In != 10312 || r.CacheRead != 0 {
		t.Fatalf("tokens wrong: %+v", r)
	}
}

func TestRecordStopIgnoresTurnsWithoutTokens(t *testing.T) {
	m := hookFixture(t, nil)
	// An interrupted turn fires stop twice with one generation id, neither carrying tokens.
	recordStop(m.Name, stopFixture(t, "stop_aborted.json"), time.Now())
	recordStop(m.Name, stopFixture(t, "stop_error.json"), time.Now())
	// A completed payload with no counts is not a zero-token turn either.
	in := stopFixture(t, "stop_completed_plain.json")
	in.InputTokens, in.OutputTokens = nil, nil
	recordStop(m.Name, in, time.Now())
	// ... and neither is a status other than completed that does carry counts.
	in = stopFixture(t, "stop_completed_plain.json")
	in.Status = "error"
	recordStop(m.Name, in, time.Now())
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Fatalf("wrote rows for turns without tokens: %+v", rows)
	}
}

func TestRecordStopOnlyForATerminalCursorSlot(t *testing.T) {
	in := stopFixture(t, "stop_completed_plain.json")
	for name, mutate := range map[string]func(*session.Meta){
		"claude":  func(m *session.Meta) { m.Kind = session.KindClaude },
		"managed": func(m *session.Meta) { m.Driver = session.DriverManaged },
	} {
		m := hookFixture(t, mutate)
		recordStop(m.Name, in, time.Now())
		if rows := usagex.ReadRows(); len(rows) != 0 {
			t.Fatalf("%s session recorded %+v", name, rows)
		}
	}
	m := hookFixture(t, nil)
	recordStop("", in, time.Now())             // a cursor-agent run by hand: no AF_SESSION_NAME
	recordStop("no-such-slot", in, time.Now()) // a name with no session
	other := in
	other.GenerationID = ""
	recordStop(m.Name, other, time.Now()) // nothing to dedup on
	// A cursor-agent the pane's command started: same env, another conversation.
	sids.Write(session.UUID(m.Dir, m.Name), "11111111-0000-4000-8000-000000000009")
	recordStop(m.Name, in, time.Now())
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Fatalf("unattributable stops recorded %+v", rows)
	}
	sids.Write(session.UUID(m.Dir, m.Name), hookChat)
	recordStop(m.Name, in, time.Now())
	if rows := usagex.ReadRows(); len(rows) != 1 {
		t.Fatalf("the slot's own conversation was not recorded: %+v", rows)
	}
}

func TestRecordStopRespectsTheLedgerSwitch(t *testing.T) {
	m := hookFixture(t, nil)
	t.Setenv("AF_USAGE_RECORD", "0")
	recordStop(m.Name, stopFixture(t, "stop_completed_plain.json"), time.Now())
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Fatalf("AF_USAGE_RECORD=0 still wrote %+v", rows)
	}
}

const userHooks = `{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [{"command": "./audit.sh"}],
    "stop": [{"command": "./mine.sh", "timeout": 3}]
  }
}`

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", path, err, b)
	}
	return v
}

func stopEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range readJSON(t, path)["hooks"].(map[string]any)["stop"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestEnsureHooksCreatesTheFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := hooksPath()
	if err := ensureHooksAt(path, "/opt/af/workspace-agent"); err != nil {
		t.Fatal(err)
	}
	if v := readJSON(t, path)["version"]; v != float64(1) {
		t.Fatalf("version = %v", v)
	}
	es := stopEntries(t, path)
	if len(es) != 1 || es[0]["command"] != "'/opt/af/workspace-agent' cursor-hook stop" {
		t.Fatalf("entries = %+v", es)
	}
}

func TestEnsureHooksKeepsTheUsersHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := hooksPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(userHooks), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(path, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	// The binary moved: our entry is replaced where it stands, the user's stay as written.
	if err := ensureHooksAt(path, "/opt/af/b"); err != nil {
		t.Fatal(err)
	}
	es := stopEntries(t, path)
	if len(es) != 2 || es[0]["command"] != "./mine.sh" || es[0]["timeout"] != float64(3) ||
		!strings.Contains(es[1]["command"].(string), "/opt/af/b") {
		t.Fatalf("stop entries = %+v", es)
	}
	hooks := readJSON(t, path)["hooks"].(map[string]any)
	if _, ok := hooks["beforeShellExecution"]; !ok {
		t.Fatalf("the user's other event was dropped: %+v", hooks)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want the user's 0640", fi.Mode().Perm())
	}
	// Current already: no rewrite at all.
	before, _ := os.ReadFile(path)
	if err := ensureHooksAt(path, "/opt/af/b"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("an up-to-date file was rewritten")
	}
}

func TestEnsureHooksFailsClosed(t *testing.T) {
	for name, content := range map[string]string{
		"not json":         `{"version":1,`,
		"not an object":    `[1]`,
		"null":             `null`,
		"other version":    `{"version":2,"hooks":{}}`,
		"hooks not object": `{"version":1,"hooks":[]}`,
		"hooks null":       `{"version":1,"hooks":null}`,
		"stop not array":   `{"version":1,"hooks":{"stop":{"command":"x"}}}`,
		"stop null":        `{"version":1,"hooks":{"stop":null}}`,
		"no version":       `{"hooks":{"stop":[]}}`,
		"empty file":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := hooksPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ensureHooksAt(path, "/opt/af/a"); err == nil {
				t.Fatal("expected an error")
			}
			if got, _ := os.ReadFile(path); string(got) != content {
				t.Fatalf("the file was touched: %s", got)
			}
		})
	}
}

func TestEnsureHooksWritesThroughASymlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(real, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	path := hooksPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(path, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v %v", fi, err)
	}
	if len(stopEntries(t, real)) != 2 {
		t.Fatal("the link target was not updated")
	}
}

func TestEnsureHooksKeepsLookalikeUserEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := hooksPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	mine := []string{
		`{"command":"printf 'completed cursor-hook stop' >> ./audit.log"}`,
		`{"command":"audit cursor-hook stop"}`,
		`{"command":"'/opt/x/tool' cursor-hook stop extra"}`,
		`{"command":"'/opt/x/tool' cursor-hook stop","matcher":"x"}`,
		`{"command":"/opt/x/tool cursor-hook stop"}`,
	}
	doc := `{"version":1,"hooks":{"stop":[` + strings.Join(mine, ",") + `]}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(path, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	es := stopEntries(t, path)
	if len(es) != len(mine)+1 {
		t.Fatalf("a user entry was replaced or dropped: %+v", es)
	}
	for i, m := range mine {
		var want map[string]any
		_ = json.Unmarshal([]byte(m), &want)
		if es[i]["command"] != want["command"] {
			t.Fatalf("entry %d changed: %+v", i, es[i])
		}
	}
	// A path with a quote in it is still recognised as ours when we wrote it.
	if err := ensureHooksAt(path, "/opt/it's/a"); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(path, "/opt/af/b"); err != nil {
		t.Fatal(err)
	}
	if got := stopEntries(t, path); len(got) != len(mine)+1 {
		t.Fatalf("our entry was duplicated: %+v", got)
	}
}

func TestEnsureHooksRefusesWhatIsNotARegularFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := hooksPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() { done <- ensureHooksAt(path, "/opt/af/a") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensureHooksAt blocked on a FIFO")
	}
	// Too large to be a hooks.json.
	os.Remove(path)
	if err := os.WriteFile(path, append([]byte(`{"version":1,"x":"`), make([]byte, maxHooksSize)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(path, "/opt/af/a"); err == nil {
		t.Fatal("expected an error for an oversized file")
	}
}

// Saves that land during the publish are merged, never dropped, and every user version stays
// on disk: in the file, or beside it as an .af-prev file.
func prevFiles(t *testing.T, target string) []string {
	t.Helper()
	m, _ := filepath.Glob(target + ".af-prev.*")
	return m
}

func anyContains(t *testing.T, files []string, sub string) bool {
	t.Helper()
	for _, f := range files {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), sub) {
			return true
		}
	}
	return false
}

func TestEnsureHooksMergesASaveBeforeTheSwap(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := `{"version":1,"hooks":{"stop":[{"command":"./saved-later.sh"}]}}`
	hookAfterRead = func() { _ = os.WriteFile(target, []byte(saved), 0o600) }
	defer func() { hookAfterRead = nil }()
	if err := ensureHooksAt(target, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	es := stopEntries(t, target)
	if len(es) != 2 || es[0]["command"] != "./saved-later.sh" {
		t.Fatalf("published = %+v", es)
	}
	if !anyContains(t, prevFiles(t, target), "saved-later.sh") {
		t.Fatal("the saved version is not kept beside the file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".hooks.json.af-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// An editor saves by renaming its own temp file over the path inside the window: that inode is
// the one the exchange swaps out, so it is found, merged and kept.
func TestEnsureHooksKeepsAReplaceSaveDuringPublish(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := `{"version":1,"hooks":{"stop":[{"command":"./replaced-in-window.sh"}]}}`
	hookBeforeSwap = func() {
		hookBeforeSwap = nil // one save
		ed := filepath.Join(dir, "editor.tmp")
		_ = os.WriteFile(ed, []byte(saved), 0o600)
		_ = os.Rename(ed, target)
	}
	defer func() { hookBeforeSwap = nil }()
	if err := ensureHooksAt(target, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	es := stopEntries(t, target)
	if len(es) != 2 || es[0]["command"] != "./replaced-in-window.sh" {
		t.Fatalf("target = %+v", es)
	}
	prev := prevFiles(t, target)
	// The inode the editor replaced is the editor's to drop; the one it put there must survive us.
	if !anyContains(t, prev, "replaced-in-window.sh") {
		t.Fatalf("the user's replace-saved version was not kept: %v", prev)
	}
}

// A user's .af-prev file that already exists is never touched.
func TestEnsureHooksLeavesAnExistingBackupAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "hooks.json.af-prev")
	if err := os.Mkdir(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureHooksAt(target, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(mine); err != nil || !fi.IsDir() {
		t.Fatalf("the user's own %s was disturbed: %v", mine, err)
	}
}

// TestHelperPublishCrash is the child of the crash tests: it dies inside the publish.
func TestHelperPublishCrash(t *testing.T) {
	stage := os.Getenv("AF_TEST_CRASH_STAGE")
	if stage == "" {
		t.Skip("child process only")
	}
	die := func() { os.Exit(3) }
	switch stage {
	case "before-swap":
		hookBeforeSwap = die
	case "after-swap":
		hookAfterSwap = die
	}
	_ = ensureHooksAt(os.Getenv("AF_TEST_HOOKS"), "/opt/af/a")
	os.Exit(0) // not reached when the seam fired
}

// A process killed while publishing never leaves the user's path empty, and the next ensure
// still has every user hook.
func TestEnsureHooksSurvivesAKilledPublish(t *testing.T) {
	for _, stage := range []string{"before-swap", "after-swap"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := hooksPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(userHooks), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPublishCrash$")
			cmd.Env = append(os.Environ(), "AF_TEST_CRASH_STAGE="+stage, "AF_TEST_HOOKS="+path, "HOME="+home)
			if err := cmd.Run(); err == nil {
				t.Fatal("the child was expected to die inside the publish")
			}
			if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
				t.Fatalf("the user's path is gone or empty after a kill: %v", err)
			}
			if err := ensureHooksAt(path, "/opt/af/b"); err != nil {
				t.Fatal(err)
			}
			es := stopEntries(t, path)
			if len(es) != 2 || es[0]["command"] != "./mine.sh" {
				t.Fatalf("stop entries after recovery = %+v", es)
			}
			if _, ok := readJSON(t, path)["hooks"].(map[string]any)["beforeShellExecution"]; !ok {
				t.Fatal("the user's other event is gone")
			}
		})
	}
}

// An editor holding the old file open writes into it after our final check: that content is
// merged into the published file rather than dropped, and the backup keeps the user's text.
func TestEnsureHooksKeepsAnInPlaceWriteDuringPublish(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	// os.O_TRUNC emptied it; restore through the same descriptor, as an editor would.
	if _, err := fd.WriteString(userHooks); err != nil {
		t.Fatal(err)
	}
	saved := `{"version":1,"hooks":{"stop":[{"command":"./saved-in-place.sh"}]}}`
	hookBeforeSwap = func() {
		hookBeforeSwap = nil // one write
		_ = fd.Truncate(0)
		_, _ = fd.WriteAt([]byte(saved), 0)
	}
	defer func() { hookBeforeSwap = nil }()
	if err := ensureHooksAt(target, "/opt/af/a"); err != nil {
		t.Fatal(err)
	}
	es := stopEntries(t, target)
	if len(es) != 2 || es[0]["command"] != "./saved-in-place.sh" || !strings.Contains(es[1]["command"].(string), "cursor-hook") {
		t.Fatalf("published entries = %+v", es)
	}
	if !anyContains(t, prevFiles(t, target), "saved-in-place.sh") {
		t.Fatal("the in-place version is not kept beside the file")
	}
}

// The restore path of an unmergeable save must not drop a newer, valid save that replaced the
// path while it ran.
func TestEnsureHooksInvalidRestoreKeepsTheNewestSave(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte(userHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	replace := func(content string) {
		ed := filepath.Join(dir, "editor.tmp")
		_ = os.WriteFile(ed, []byte(content), 0o600)
		_ = os.Rename(ed, target)
	}
	newest := `{"version":1,"hooks":{"stop":[{"command":"./newest.sh"}]}}`
	hookBeforeSwap = func() { hookBeforeSwap = nil; replace(`{"version":1,`) } // broken save
	hookAfterSwap = func() { hookAfterSwap = nil; replace(newest) }            // then a valid one
	defer func() { hookBeforeSwap, hookAfterSwap = nil, nil }()
	_ = ensureHooksAt(target, "/opt/af/a") // an error is acceptable; losing the save is not
	files := append(prevFiles(t, target), target)
	if !anyContains(t, files, "newest.sh") {
		t.Fatal("the newest save is neither in the file nor kept beside it")
	}
}
