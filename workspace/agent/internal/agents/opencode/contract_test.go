//go:build clicontract

// opencode CLI contract tests — the drift alarm for the store and app-server contracts
// this package reads. Unlike the rest of the package's tests (synthetic `CREATE TABLE`
// schemas and httptest mocks, which keep passing no matter what the real CLI does),
// these run against the REAL opencode binary, its REAL SQLite store and a REAL
// `opencode serve`, so an upstream change that moves a contract fails here.
//
// Why this exists: the fleet tracks opencode@latest by design (entrypoint.sh's opt-in
// self-update), so a new CLI arrives unannounced at any container restart — the image's
// ARG pin is the fallback, not what runs. The existing L1 smoke asserts "image version ==
// ARG pin", which is a pin-conformance check: it stays green when a NEW opencode breaks
// our parsing. That gap is how claude's false-idle reached the live fleet.
//
// Credential-free by design: opencode ships free OpenCode Zen models and boots its
// composer without a login, so Tier A needs no API key and no spend (unlike claude's L4).
//
// Tier A (this file, default): deterministic, no LLM turn. Tier B (live_contract_test.go):
// one free-model turn; flaky by nature (external service) and gated + non-blocking.
//
//	go test -tags clicontract ./internal/agents/opencode/
package opencode

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// requireOpencode skips (or fails under E2E_REQUIRE=1, like e2e/) when the CLI is absent,
// so a dev box without opencode doesn't fail while CI insists on a real run.
func requireOpencode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("opencode"); err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("opencode not on PATH and E2E_REQUIRE=1: %v", err)
		}
		t.Skip("opencode not on PATH — contract test skipped (set E2E_REQUIRE=1 to demand it)")
	}
}

func opencodeVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("opencode", "--version").Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// envWithHome replaces HOME so a contract run never reads or writes the user's real
// opencode store (~/.local/share/opencode) — the tests must be isolated and repeatable.
//
// The XDG_* roots have to go with it, or the isolation is only nominal: opencode resolves
// its config/state through XDG_CONFIG_HOME / XDG_DATA_HOME when those are set, and a
// GitHub Actions runner exports them pointing at the real /home/runner. Replacing HOME
// alone therefore leaves the run reading someone else's roots on CI while looking
// hermetic on a workstation (that gap is what made the precedence contract in
// ../../opencode_contract_test.go red on CI only).
func envWithHome(home string) []string {
	out := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_") {
			out = append(out, kv)
		}
	}
	return out
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// startServe boots a real `opencode serve` against an isolated HOME and returns its addr
// plus that HOME. The store it writes is the one dbPath() resolves once HOME is set.
func startServe(t *testing.T) (addr, home string) {
	t.Helper()
	return startServeIn(t, t.TempDir())
}

// startServeIn is startServe against a HOME the caller prepared, for tests that must
// seed config (e.g. an `mcp` entry) BEFORE the daemon reads it at boot.
func startServeIn(t *testing.T, home string) (addr, _ string) {
	t.Helper()
	requireOpencode(t)
	port := freePort(t)
	addr = "http://127.0.0.1:" + port
	cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = envWithHome(home)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatalf("opencode serve start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(addr) { // the production health probe, not a test-local one
			return addr, home
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("opencode serve (%s) never became healthy at %s", opencodeVersion(t), addr)
	return "", ""
}

// TestContractStoreSchema pins the store contract LiveState/readTranscript depend on:
// the tables + columns our SQL names, and the JSON paths our queries json_extract().
// A future opencode migration (the store already carries ~38 applied migrations, and a
// v2 session_message store exists beside the v1 message/part one we read) fails here
// FIRST — before the fleet silently degrades.
func TestContractStoreSchema(t *testing.T) {
	addr, home := startServe(t)
	t.Setenv("HOME", home) // dbPath()/openRO() resolve under the isolated HOME
	dir := t.TempDir()

	// A real session — created through the production code path, no LLM turn needed.
	ses, err := serveCreateSession(addr, dir, "contract")
	if err != nil {
		t.Fatalf("serveCreateSession against real opencode %s: %v", opencodeVersion(t), err)
	}

	db, ok := openRO()
	if !ok {
		t.Fatalf("openRO could not open the real store at %s", dbPath())
	}
	defer db.Close()

	// (1) Columns our SQL names must still exist.
	want := map[string][]string{
		"message": {"id", "session_id", "time_created", "data"},
		"part":    {"session_id", "time_created", "data"},
		"session": {"id", "parent_id", "directory", "time_created", "time_compacting"},
	}
	for table, cols := range want {
		have := map[string]bool{}
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Errorf("pragma_table_info(%s): %v", table, err)
			continue
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err == nil {
				have[n] = true
			}
		}
		rows.Close()
		if len(have) == 0 {
			t.Errorf("table %q is gone from opencode %s's store — the read layer's contract moved", table, opencodeVersion(t))
			continue
		}
		for _, c := range cols {
			if !have[c] {
				t.Errorf("%s.%s missing in opencode %s — a query in this package names it", table, c, opencodeVersion(t))
			}
		}
	}

	// (2) The production queries must still be valid SQL against the real schema. No rows
	// yet (no turn) — ErrNoRows/empty is fine; a SQL error is the contract breaking.
	if _, err := activeSessionErr(db, session.Meta{Dir: dir, Name: "contract"}); err != nil {
		t.Errorf("activeSessionErr's session⋈message query broke against opencode %s: %v", opencodeVersion(t), err)
	}
	rows, err := db.Query(
		`SELECT data FROM part WHERE session_id = ? AND json_extract(data,'$.tool') = 'question' AND json_extract(data,'$.state.status') = 'running'`, ses)
	if err != nil {
		t.Errorf("pending()'s question-tool query broke against opencode %s: %v", opencodeVersion(t), err)
	} else {
		rows.Close()
	}

	// (3) The quiet-degradation alarm. LiveState answers "" only when it cannot derive a
	// state, and at runtime that silently falls back to the (unreliable) plugin status —
	// a real turn would look fine while the SQLite path is dead. Against a real store in a
	// known state it must give a definite answer, so "" here IS the drift signal.
	if st := LiveState(session.Meta{Dir: dir, Name: "contract"}); st != "idle" {
		t.Errorf("LiveState = %q against a real opencode %s store with a fresh session, want \"idle\"; "+
			"%q means the store contract moved and the fleet would quietly fall back to the plugin status",
			st, opencodeVersion(t), st)
	}
}

// TestContractAppServer pins the managed driver's protocol (docs/27 §12.2 measured it
// against 1.17.18; the fleet now runs whatever @latest is). These fail loudly at runtime
// rather than silently, but managed is the strategic path, so pin them anyway.
func TestContractAppServer(t *testing.T) {
	addr, _ := startServe(t)
	dir := t.TempDir()
	ver := opencodeVersion(t)

	// /global/health — the liveness probe Serve() gates on.
	res, err := http.Get(addr + "/global/health")
	if err != nil {
		t.Fatalf("GET /global/health: %v", err)
	}
	var health struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Errorf("/global/health payload changed in opencode %s: %v", ver, err)
	}
	res.Body.Close()
	if !health.Healthy {
		t.Errorf("/global/health healthy=false (version=%q)", health.Version)
	}

	// POST /session → the id the driver keys everything else on.
	ses, err := serveCreateSession(addr, dir, "contract")
	if err != nil {
		t.Fatalf("serveCreateSession broke against opencode %s: %v", ver, err)
	}
	if !serveSessionExists(addr, ses, dir) {
		t.Errorf("serveSessionExists=false for a session opencode %s just created — GET /session/{id} moved", ver)
	}

	// GET /session/status — the busy probe. Shape: {ses: {type: …}}, idle sessions absent.
	res, err = http.Get(addr + "/session/status?directory=" + dir)
	if err != nil {
		t.Fatalf("GET /session/status: %v", err)
	}
	var status map[string]struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Errorf("/session/status is no longer the session-keyed map serveSessionBusy decodes (opencode %s): %v", ver, err)
	}
	res.Body.Close()
	if serveSessionBusy(addr, ses, dir) {
		t.Errorf("a fresh session reads busy — /session/status semantics moved in opencode %s", ver)
	}

	// GET /question — the pending-question poll.
	res, err = http.Get(addr + "/question?directory=" + dir)
	if err != nil {
		t.Fatalf("GET /question: %v", err)
	}
	var qs []json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&qs); err != nil {
		t.Errorf("/question is no longer a JSON array in opencode %s: %v", ver, err)
	}
	res.Body.Close()

	// GET /global/event — the SSE envelope handleServeEvent unwraps ({payload:{type,…}}).
	req, _ := http.NewRequest("GET", addr+"/global/event", nil)
	client := &http.Client{Timeout: 20 * time.Second}
	sres, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /global/event: %v", err)
	}
	defer sres.Body.Close()
	buf := make([]byte, 4096)
	n, _ := sres.Body.Read(buf)
	line := string(buf[:n])
	data, ok := strings.CutPrefix(strings.TrimSpace(strings.SplitN(line, "\n", 2)[0]), "data: ")
	if !ok {
		t.Fatalf("/global/event is no longer an SSE `data: ` stream in opencode %s: %q", ver, line)
	}
	var ev struct {
		Payload struct {
			Type string `json:"type"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil || ev.Payload.Type == "" {
		t.Errorf("/global/event envelope moved in opencode %s — handleServeEvent expects {payload:{type,properties}}, got %q (err=%v)", ver, data, err)
	}
}

// TestContractProgramFlagsAccepted guards the launch flags buildProgram emits. `--auto`
// is load-bearing twice over: it auto-approves permissions AND it is why the composer
// renders "<Agent> auto ·", the token opencodeStatusAgentRe anchors on. A rename would
// break the mode chip and the launch-seed readiness signal at once.
func TestContractProgramFlagsAccepted(t *testing.T) {
	requireOpencode(t)
	home := t.TempDir()
	cmd := exec.Command("opencode", "--auto", "--help")
	cmd.Env = envWithHome(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode %s rejected `--auto` (buildProgram's default AGENT_OPENCODE_FLAGS): %v\n%s",
			opencodeVersion(t), err, out)
	}
}
