package sessionx

// testhelp_test.go — copies of the package main test helpers this family of tests used.
//
// Go cannot share test helpers across packages: an identifier in a `_test.go` file exists
// only in that package's test binary, so neither main's `withTempHome` nor its `gitInit` is
// visible from `internal/sessionx` (and neither can be carried on an alias). Moving the
// family's 30 test files together therefore leaves no option but copies here — the same shape
// `internal/browserx/mux_test.go` and `internal/memoryx/mux_test.go` took.
//
// A copy rots silently. To limit the rot:
//
//   - copy with the same spelling, the same arguments and the same defaults (never "improve"
//     the behaviour: README §4 warns that rebuilding something as an approximation of the
//     standard library shrinks coverage while even a two-sided mutation test stays green)
//   - name the file each function was copied from right above it
//   - `buildMux` alone is a subset rather than a copy, so mux_test.go detects its rot by
//     comparing it against `routes.golden` (the 247 routes captured from the real mux)
//
// The tmux isolation (isolatedTmuxSocket / isolateAgentState / paneShowing) also stays on the
// main side, deliberately: `session_rate_limit_state_test.go` was left in main because main's
// `shutdown_isolation_test.go` (not owned here) depends on those three, and moving them would
// mean rewriting a file outside this ownership. Writing the same thing twice leaves one copy
// stale, so this is reported as debt — with the package boundary in the way, sharing them
// means lifting them into a non-test shared package, which is a separate work package.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// tmuxSocketSeq is the serial number in an isolated socket name (copy of main's
// session_rate_limit_state_test.go).
var tmuxSocketSeq atomic.Int64

// --- withTempHome: copy of workspace/agent/chat_main_test.go ---
// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree.
//
// Register the wait on the delivery goroutines AFTER `t.Setenv`: Cleanup runs LIFO, so it runs
// before HOME is restored. Without the wait, a chatx delivery writes a notification into the
// real HOME once it is back and a ghost notification appears in the user's Console
// (interimDeliveries in chatx/chat_report.go).
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Cleanup(chatx.WaitInterimDeliveries)
	t.Cleanup(WaitInputMirrors) // same reason; this one is the secrets store (measured: secrets.enc.lock)
	return dir
}

// --- writeFile: copy of workspace/agent/repo_prompts_test.go ---
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- isolateAgentConfigDirs: copy of workspace/agent/routes_test.go ---
// isolateAgentConfigDirs points HOME **and every config dir that is pinned by its own
// environment variable** at one throwaway tree.
//
// HOME alone is not enough, and the gap is not theoretical: paths.ClaudeConfigDir()
// honours $CLAUDE_CONFIG_DIR, which production sets to /var/lib/af/claude (a dedicated
// mount outside home). So a test that only isolated HOME and then hit
// POST /mcp-servers — which materializes the registry into every CLI's config — wrote
// its fixture server into the developer's REAL .claude.json. It was found there on
// 2026-08-09 as a live `wiki` → https://mcp.example.com/mcp entry, straight out of
// mcp_servers_test.go.
//
// Worse than the stray row: the ownership ledger (mcp-managed.json) DID land in the
// temp HOME, so af never learned it wrote that name — the row became an orphan no
// later materialize is allowed to remove (docs/log/48 §8.2), and only a hand-run
// `claude mcp remove` clears it.
//
// The other kinds escaped only by luck: CODEX_HOME / COPILOT_HOME / KIRO_HOME /
// XDG_CONFIG_HOME are unset in this container, so they resolved under the temp HOME.
// Set them here too rather than depend on that.
func isolateAgentConfigDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for k, v := range map[string]string{
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"COPILOT_HOME":      filepath.Join(home, ".copilot"),
		"KIRO_HOME":         filepath.Join(home, ".kiro"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
	} {
		t.Setenv(k, v)
	}
}

// --- isolatedTmuxSocket: copy of workspace/agent/session_rate_limit_state_test.go ---
// isolatedTmuxSocket returns a tmux socket name shared with nobody.
//
// A fixed name for the isolated socket makes the tests that fire `kill-server` race each other
// (the reason is in isolateAgentState's note). The rule for building the name lives in this
// one place because writing the same rule twice leaves one copy stale — and it did:
// `shutdown_isolation_test.go` assembles the same name itself and was never fixed.
//
// This function sits in this file for ownership reasons; in meaning it is a shared piece of
// the tmux isolation.
func isolatedTmuxSocket() string {
	return fmt.Sprintf("af-test-%d-%d", os.Getpid(), tmuxSocketSeq.Add(1))
}

// --- isolateAgentState: copy of workspace/agent/session_rate_limit_state_test.go ---
func isolateAgentState(t *testing.T) {
	t.Helper()
	// Vary the socket name per test. It used to be a fixed `af-test-<pid>`, so every test in
	// the four files that use this isolation (plus shutdown_isolation_test.go, which builds the
	// same name) shared ONE tmux server. Each test's Cleanup fires `kill-server`, but tmux
	// returns as soon as it has taken the command and the server's shutdown is asynchronous. So
	// the next test's `new-session` reaches a dying server and fails with
	// `server exited unexpectedly` — a red with no visible reason, unrelated to the test body.
	//
	// The window widens with load (measured 2026-09-02: 0 occurrences at `-count=30` with no
	// load, 7 at `-count=40` under 6 CPU hogs; the failures were TestDriveStateIdlePaneNotBlocked
	// and TestDriveStateAuthValid, the same shape as real CI run 33584943716).
	//
	// The serial number is there for `-count=N`: with the test name alone, a round races the
	// kill-server of the PREVIOUS round of the same name.
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// The status store sits directly under HOME (paths.AgentConfigDir) — never write a marker
	// into the real fleet.
	t.Setenv("HOME", t.TempDir())
	// Isolate claude's config/credentials too. HOME alone is not enough: in this container
	// CLAUDE_CONFIG_DIR points at the real fleet's tree, so the state decision (auth expired,
	// docs/log/47 §4-8) would depend on the real login's expiry.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// kill-server is allowed only against a dedicated socket (dev/04 §4.11).
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
}

// --- paneShowing: copy of workspace/agent/session_rate_limit_state_test.go ---
// paneShowing starts an isolated tmux session whose pane displays frame's contents and
// then stays alive, and returns the session meta for it.
func paneShowing(t *testing.T, name, frame string) session.Meta {
	t.Helper()
	// Check that the frame exists, here. Without it only `cat` fails while `new-session` still
	// succeeds, so callers wave an EMPTY pane through as the thing under test — which is exactly
	// what happened when the move changed the depth of the relative paths. A check that lists
	// the callers by hand can only notice the list shrinking, so the call site itself is guarded.
	if _, err := os.Stat(frame); err != nil {
		t.Fatalf("frame %s is missing: %v (suspect the depth of the relative path; left alone the pane shows nothing and the check stays green)", frame, err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tn := session.TmuxName(name)
	// Take a real pane's width: different wrapping changes how the footer/choice lines look.
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50",
		"sh", "-c", fmt.Sprintf("cat %q; sleep 60", frame)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	// Wait for cat to finish drawing into the pane (capture-pane reads the drawn screen).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmuxx.CapturePane(tn) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m
}

// --- do: copy of workspace/agent/worktree_flow_test.go ---
// do sends a JSON request, asserts the status, and decodes the body into out (if any).
func do(t *testing.T, srv *httptest.Server, method, path string, body any, want int, out any) {
	t.Helper()
	code, raw := roundtrip(t, srv, method, path, body)
	if code != want {
		t.Fatalf("%s %s = %d (%s), want %d", method, path, code, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, raw)
		}
	}
}

// --- httpStatus: copy of workspace/agent/worktree_flow_test.go ---
func httpStatus(t *testing.T, srv *httptest.Server, method, path string, body any) int {
	t.Helper()
	code, _ := roundtrip(t, srv, method, path, body)
	return code
}

// --- roundtrip: copy of workspace/agent/worktree_flow_test.go ---
func roundtrip(t *testing.T, srv *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

// --- gitInit: copy of workspace/agent/git_integration_helpers_test.go ---
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")
	run("branch", "feature")
}

// --- runIntegrationGit: copy of workspace/agent/git_integration_helpers_test.go ---
func runIntegrationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// --- writeUIPrefs: copy of workspace/agent/ui_prefs_test.go ---
func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- consoleCatalog: copy of workspace/agent/console_catalog_test.go ---
// consoleCatalog returns one locale of the Console catalogue as a single string, for checking
// that a key exists.
//
// The catalogue is split per domain into `locales/<locale>/*.ts` (ADR 0067 decision 4), and
// `locales/<locale>.ts` is a composed file holding nothing but imports and spreads. Reading
// that one turns the check into "the key is there, but reported missing" — go through this
// function.
//
// Skips when the catalogue is absent, for builds from a distribution without console/.
func consoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	// Zero files means "nothing was read", not "the key is missing". Waving that through makes
	// this check the same as not having one.
	if n == 0 {
		t.Fatalf("no .ts file at all under %s (did the catalogue move?)", dir)
	}
	return b.String()
}

// --- consoleCatalogHasKey: copy of workspace/agent/console_catalog_test.go ---
// consoleCatalogHasKey reports whether "key" is defined in the catalogue.
func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}

// --- awaitReported: copy of workspace/agent/chat_main_test.go ---
// awaitReported polls until the session has no open instruction row left. The row is moved
// to reported only after delivery (the append to the conversation) succeeds, so at the
// moment the report card appears the row can still be pending: wait before asserting it.
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !chatx.SessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("a delivered report must move the instruction row to reported (one instruction = one report): %s", name)
}

// --- withTestReconciler: copy of workspace/agent/chat_main_test.go ---
// withTestReconciler installs the real reconciler at a short interval. The implementation is
// inside chatx, so only the installation is called through the seam.
func withTestReconciler(t *testing.T, interval time.Duration) {
	t.Helper()
	t.Cleanup(chatx.InstallReconcilerForTest(interval))
}

// --- reportBodyForTest: copy of workspace/agent/prompt_lang_test.go ---
// reportBodyForTest builds the PROMPT BODY of one session report (heading + facts +
// instructions + notes) from the same materials, assembled the same way, as the real delivery
// (recordSessionReport). Used by the existing tests that inspect the Japanese wording.
func reportBodyForTest(display, name, kind, reason string) string {
	args := chatx.ReportArgs(display, name, kind, reason, 0)
	return chatx.ReportPromptFor(chatx.ChatMessage{
		Role: "report", ReportKind: kind, ReportReason: reason, NoticeArgs: args,
	}, "ja")
}

// TestFixturePathsResolve checks that the relative paths whose depth changed in the move still
// resolve.
//
// This is the trace of actually hitting README §4's "the depth of a relative path always
// changes in a move, and forgetting to fix one passes silently". Right after the move, the
// `internal/tmuxx/testdata/footers/idle_bypass_hint.txt` handed to `paneShowing` did not exist
// as seen from sessionx, but only the `cat` running inside tmux failed, so new-session
// succeeded and TestDriveStateAuthExpired stayed green (measured). It was calling a pane with
// no frame on it "auth expired", i.e. it never once reached the branch under test.
// `rate_limit_resume_test.go` had an explicit Fatal, so that one failed and gave it away.
//
// So a path has its existence checked here, once, before use. That is why this test exists and
// it is the intended +1 against "the set of test functions matches develop".
func TestFixturePathsResolve(t *testing.T) {
	paths := []string{
		"../tmuxx/testdata/footers/idle_bypass_hint.txt",
		"../tmuxx/testdata/footers/modal_rate_limit.txt",
		filepath.Join("..", "..", "testdata", "routes.golden"),
		filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", "ja"),
	}
	if len(paths) < 4 {
		t.Fatal("fewer than 4 paths scanned = this check has gone silent")
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not found: %v (the move changed the depth of the relative path and it was "+
				"never fixed; the cat inside tmux can fail while new-session still succeeds, so "+
				"until it is fixed the check stays green and measures nothing)", p, err)
		}
	}
}
