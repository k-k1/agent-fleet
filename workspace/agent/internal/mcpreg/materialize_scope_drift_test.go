//go:build contract

// The contract of project-local MCP scopes (docs/log/48 §8.4). It runs against the REAL
// agent CLIs, so the `contract` build tag keeps it out of `go test ./...`.
//
// Why it is needed: af writes exactly one place in each CLI, its user/global scope, and
// leaves the repository's project scope (`.mcp.json` / `.cursor/mcp.json` / `opencode.json`
// / …) alone as the user's. That division rests on two premises:
//
//  1. The two are merged — af's server does not disappear in a repository that has its own
//     project config.
//  2. The winner of a name collision is known — it decides whether af's own server (self
//     reporting, handoff proposals, Chromium attach) gets taken over when a project config
//     happens to define, or deliberately defines, the name `af`. `reservedNames` only works
//     inside the AF registry, so a project file cannot be stopped that way.
//
// Both depend on CLI-side implementation and can change silently with a release. When this
// goes red, update the table in docs/log/48 §8.4.
//
// No authentication: `mcp list` only reads the config files and tries to connect. The probes
// are things like /bin/true and the handshake may fail — only the name and its origin
// appearing in the listing is being observed. kiro is unverified because every `mcp`
// subcommand demands a login; agy's MCP config is global-only, so there is no project scope
// for this test to observe in the first place.
package mcpreg

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// scopeSpec describes one CLI's two-scope layout and how its `mcp list` reveals which
// definition won a name collision.
type scopeSpec struct {
	kind string
	bin  string
	// env for the CLI so both scopes resolve inside the test's sandbox.
	env func(home string) []string
	// writeUser materializes af's user-scope file through af's OWN writer, so this
	// tests the file af really ships, not a hand-written approximation.
	writeUser func(t *testing.T, defs []ServerDef)
	// projectFile is the repo-owned config, relative to the project directory.
	projectFile string
	projectDoc  func(servers map[string]string) string
	list        []string
	// listFn replaces running `list` when the CLI's own listing no longer reflects the
	// config (opencode 2.x); it returns text the winner reader understands.
	listFn func(t *testing.T, dir string, env []string, bin string) string
	// prepare runs after both scopes are written, for gates the CLI puts in front of the
	// project scope (copilot's folder trust).
	prepare func(t *testing.T, home, proj string)
	// winner reads a listing produced with the SAME name in both scopes and reports
	// which definition the CLI resolved: "user", "project", or "" for unreadable.
	winner func(out string) string
	// wantWinner is the recorded behaviour. A change here is the point of the test.
	wantWinner string
}

const (
	scopeProjectCmd = "/bin/PROJECT-WINS"
	scopeUserCmd    = "/bin/USER-WINS"
)

func scopeSpecs() []scopeSpec {
	return []scopeSpec{
		{
			kind: "claude",
			bin:  "claude",
			env: func(home string) []string {
				return []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "cfg")}
			},
			writeUser: func(t *testing.T, defs []ServerDef) {
				if _, _, _, err := materializeClaude(defs, nil); err != nil {
					t.Fatalf("materializeClaude: %v", err)
				}
			},
			projectFile: ".mcp.json",
			projectDoc:  jsonScopeDoc("mcpServers", `"type":"stdio",`),
			list:        []string{"mcp", "list"},
			// claude prints the RESOLVED command beside the name — and, separately, a
			// "Conflicting scopes" diagnostic naming both. Only the resolved line says
			// who won, so a plain substring search would read the diagnostic and get it
			// backwards.
			winner:     resolvedLineWinner("af"),
			wantWinner: "user",
		},
		{
			kind: "opencode",
			bin:  "opencode",
			env: func(home string) []string {
				cfg := filepath.Join(home, ".config")
				return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + cfg,
					"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
					"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
					"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state")}
			},
			writeUser: func(t *testing.T, defs []ServerDef) {
				if _, _, _, err := materializeOpencode(defs, nil); err != nil {
					t.Fatalf("materializeOpencode: %v", err)
				}
			},
			projectFile: "opencode.json",
			projectDoc:  opencodeScopeDoc,
			list:        []string{"mcp", "list"},
			// 2.x `mcp list` asks a background service rooted at $HOME that loads MCP
			// lazily, so it prints "No MCP servers configured" for any config; the
			// per-project server's /api/mcp is what reports the resolved servers.
			listFn:     opencodeServeList,
			winner:     commandWinner,
			wantWinner: "project",
		},
		{
			kind: "cursor",
			bin:  "cursor-agent",
			env:  func(home string) []string { return []string{"HOME=" + home} },
			writeUser: func(t *testing.T, defs []ServerDef) {
				if _, _, _, err := materializeCursor(defs, nil); err != nil {
					t.Fatalf("materializeCursor: %v", err)
				}
			},
			projectFile: filepath.Join(".cursor", "mcp.json"),
			projectDoc:  jsonScopeDoc("mcpServers", ""),
			list:        []string{"mcp", "list"},
			// cursor prints no command. Its tell is the approval gate, which only the
			// PROJECT scope carries: "needs approval" on af's own name means the repo's
			// definition took the name over.
			winner:     approvalWinner,
			wantWinner: "project",
		},
		{
			kind: "copilot",
			bin:  "copilot",
			env: func(home string) []string {
				return []string{"HOME=" + home, "COPILOT_HOME=" + filepath.Join(home, ".copilot")}
			},
			writeUser: func(t *testing.T, defs []ServerDef) {
				if _, _, _, err := materializeCopilot(defs, nil); err != nil {
					t.Fatalf("materializeCopilot: %v", err)
				}
			},
			// copilot reads the SAME file name claude's project scope uses, so one
			// `.mcp.json` feeds both kinds.
			projectFile: ".mcp.json",
			projectDoc:  jsonScopeDoc("mcpServers", `"type":"local",`),
			list:        []string{"mcp", "list"},
			// copilot ignores the project scope of an untrusted folder; af's launcher
			// pre-trusts the working directory (agents/copilot/trust.go), so the test
			// does the same.
			prepare: copilotTrustFolder,
			// copilot prints no command either, but groups by origin — the losing
			// scope's section disappears entirely.
			winner:     sectionWinner,
			wantWinner: "project",
		},
	}
}

// TestDriftMCPScopesMerge pins premise 1: af's user-scope server survives in a
// repository that has its own project-scoped MCP config.
func TestDriftMCPScopesMerge(t *testing.T) {
	for _, sp := range scopeSpecs() {
		t.Run(sp.kind, func(t *testing.T) {
			out := runScopeList(t, sp, map[string]string{"afdrift_proj": "/bin/true"})
			for _, want := range []string{"af", "afdrift_proj"} {
				// Whole word: "af" is also a prefix of "afdrift_proj".
				if !regexp.MustCompile(`\b` + want + `\b`).MatchString(out) {
					t.Fatalf("%s: %q is not in the listing — the user scope and the project "+
						"scope are no longer merged, so af's server disappears in any repository "+
						"that has a project config (docs/log/48 §8.4)\n%s", sp.kind, want, out)
				}
			}
		})
	}
}

// TestDriftMCPScopeCollisionWinner pins premise 2. For every kind whose PROJECT scope
// wins, a repository can shadow af's own `af` server — that is a real, documented
// consequence, not a bug in af, and this test exists so it cannot change silently.
func TestDriftMCPScopeCollisionWinner(t *testing.T) {
	for _, sp := range scopeSpecs() {
		t.Run(sp.kind, func(t *testing.T) {
			out := runScopeList(t, sp, map[string]string{"af": scopeProjectCmd})
			got := sp.winner(out)
			if got == "" {
				t.Fatalf("%s: could not read the winner out of the listing (the output shape changed):\n%s", sp.kind, out)
			}
			if got != sp.wantWinner {
				t.Fatalf("%s: the winner of a name collision changed from %q to %q, so the table "+
					"in docs/log/48 §8.4 and the answer to \"can a project config take over af's "+
					"server\" are both out of date:\n%s",
					sp.kind, sp.wantWinner, got, out)
			}
			t.Logf("%s: collision winner = %s", sp.kind, got)
		})
	}
}

// runScopeList materializes af's user scope (always including af's own `af` entry),
// writes the repo's project file, and returns what the CLI's `mcp list` says from
// inside the project directory.
func runScopeList(t *testing.T, sp scopeSpec, project map[string]string) string {
	t.Helper()
	bin := cliBin(t, sp.bin)
	home := t.TempDir()
	for _, kv := range sp.env(home) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	sp.writeUser(t, []ServerDef{sessionDef(ServerDef{
		ID: BuiltinAF, Name: "af", Origin: OriginBuiltin, Transport: TransportStdio,
		Command: scopeUserCmd,
	})})

	proj := t.TempDir()
	path := filepath.Join(proj, sp.projectFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sp.projectDoc(project)), 0o600); err != nil {
		t.Fatal(err)
	}
	if sp.prepare != nil {
		sp.prepare(t, home, proj)
	}
	if sp.listFn != nil {
		return sp.listFn(t, proj, sp.env(home), bin)
	}
	return runScopeCLI(t, proj, sp.env(home), bin, sp.list)
}

// runScopeCLI tolerates a non-zero exit: several of these CLIs report a failed MCP
// handshake through the exit status, and the listing on stdout is what we are reading.
func runScopeCLI(t *testing.T, dir string, env []string, bin string, args []string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if len(out) == 0 && err != nil {
		t.Fatalf("%s %v: %v", bin, args, err)
	}
	return string(out)
}

// copilotTrustFolder writes the trustedFolders entry copilot's folder-trust dialog would
// store, into $COPILOT_HOME/config.json.
func copilotTrustFolder(t *testing.T, home, proj string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"trustedFolders": []string{proj}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".copilot", "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// scopeServeBudget bounds the whole opencodeServeList exchange; a var so the hang test can
// shorten it.
var scopeServeBudget = 60 * time.Second

// opencodeServeList runs `opencode serve` in the project directory on a free loopback
// port and returns /api/mcp once every server has left its pending state. A failed
// status carries the spawn error, which names the command the CLI resolved — the same
// tell commandWinner reads. The server is this test's own process group and is killed on
// exit. Every wait is bounded: a hung CLI must fail the test, not stall until
// `go test -timeout` panics (t.Cleanup does not run then). For that case the server is
// started under timeout(1) when available, so it still ends on its own.
func opencodeServeList(t *testing.T, dir string, env []string, bin string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	var log lockedBuf
	args := []string{bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	if tp, err := exec.LookPath("timeout"); err == nil {
		secs := int(scopeServeBudget/time.Second) + 30
		args = append([]string{tp, "-k", "5", strconv.Itoa(secs)}, args...)
	}
	srv := exec.Command(args[0], args[1:]...)
	srv.Dir = dir
	srv.Env = append(os.Environ(), env...)
	srv.Stdout, srv.Stderr = &log, &log
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	srv.WaitDelay = 2 * time.Second
	if err := srv.Start(); err != nil {
		t.Fatalf("%s serve: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-srv.Process.Pid, syscall.SIGKILL)
		_ = srv.Wait()
	})
	pw := regexp.MustCompile(`server password (\S+)`)
	deadline := time.Now().Add(scopeServeBudget)
	var password string
	for time.Now().Before(deadline) {
		if m := pw.FindStringSubmatch(log.String()); m != nil {
			password = m[1]
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if password == "" {
		t.Fatalf("opencode serve printed no password:\n%s", log.String())
	}
	apiEnv := append(append([]string{}, env...), "OPENCODE_PASSWORD="+password)
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	var out string
	for time.Now().Before(deadline) {
		var ok bool
		out, ok = runScopeCLIBefore(dir, apiEnv, bin, []string{"api", "--server", url, "GET", "/api/mcp"}, deadline)
		if !ok {
			t.Fatalf("opencode api did not answer before the deadline; output so far:\n%s", out)
		}
		if strings.Contains(out, `"name":`) && !strings.Contains(out, `"pending"`) &&
			!strings.Contains(out, `"connecting"`) {
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	return out
}

// runScopeCLIBefore runs the CLI in its own process group and kills the group at
// deadline (or after 15s, whichever is sooner); ok is false when it had to.
func runScopeCLIBefore(dir string, env []string, bin string, args []string, deadline time.Time) (string, bool) {
	limit := min(15*time.Second, time.Until(deadline))
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out, _ := cmd.CombinedOutput()
	return string(out), ctx.Err() == nil
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// --- listing readers ----------------------------------------------------------

// resolvedLineWinner reads the one line where the CLI states what it actually resolved
// the name to, ignoring any advisory that quotes both candidates.
func resolvedLineWinner(name string) func(string) string {
	return func(out string) string {
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			if !strings.HasPrefix(ln, name+":") || strings.Contains(ln, "multiple scopes") {
				continue
			}
			if w := commandWinner(ln); w != "" {
				return w
			}
		}
		return ""
	}
}

// commandWinner works for the CLIs that print the resolved command.
func commandWinner(out string) string {
	switch {
	case strings.Contains(out, scopeProjectCmd):
		return "project"
	case strings.Contains(out, scopeUserCmd):
		return "user"
	}
	return ""
}

// approvalWinner works for cursor: only project-scoped servers sit behind approval, so
// af's name showing as unapproved means the repo's definition took it over.
func approvalWinner(out string) string {
	if !strings.Contains(out, "af") {
		return ""
	}
	if strings.Contains(out, "approval") {
		return "project"
	}
	return "user"
}

// sectionWinner works for copilot, which groups by origin and drops the losing scope's
// section for a shadowed name.
func sectionWinner(out string) string {
	user := strings.Contains(out, "User servers")
	workspace := strings.Contains(out, "Workspace servers")
	switch {
	case workspace && !user:
		return "project"
	case user && !workspace:
		return "user"
	}
	return ""
}

// --- project documents --------------------------------------------------------

// jsonScopeDoc builds the {"<key>": {"<name>": {…}}} shape claude, cursor and copilot
// project files share. extra carries the per-CLI discriminator, if any.
func jsonScopeDoc(key, extra string) func(map[string]string) string {
	return func(servers map[string]string) string {
		var b strings.Builder
		b.WriteString(`{"` + key + `":{`)
		first := true
		for name, cmd := range servers {
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(`"` + name + `":{` + extra + `"command":"` + cmd + `","args":[]}`)
		}
		b.WriteString("}}")
		return b.String()
	}
}

func opencodeScopeDoc(servers map[string]string) string {
	var b strings.Builder
	b.WriteString(`{"$schema":"https://opencode.ai/config.json","mcp":{`)
	first := true
	for name, cmd := range servers {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + name + `":{"type":"local","command":["` + cmd + `"],"enabled":true}`)
	}
	b.WriteString("}}")
	return b.String()
}

// TestScopeServeListHangIsBounded is the negative control for opencodeServeList: a fake CLI
// whose `serve` prints a password and whose `api` never answers must make the helper fail
// within its budget and leave none of its processes behind. The fake records its own PID,
// so only those PIDs are checked (never a process-table pattern, which could match another
// session's). It needs no real CLI.
func TestScopeServeListHangIsBounded(t *testing.T) {
	pidFile := os.Getenv("AF_SCOPE_HANG_PIDS")
	if os.Getenv("AF_SCOPE_HANG_CHILD") == "1" {
		fake := filepath.Join(t.TempDir(), "opencode")
		script := "#!/bin/sh\necho $$ >> \"$AF_SCOPE_HANG_PIDS\"\ncase \"$1\" in\nserve) echo 'server password pw'; exec sleep 300;;\n*) exec sleep 300;;\nesac\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		scopeServeBudget = 4 * time.Second
		opencodeServeList(t, t.TempDir(), nil, fake)
		return
	}
	pidFile = filepath.Join(t.TempDir(), "pids")
	// Run the failing helper in a child test so its t.Fatal is the observed outcome.
	cmd := exec.Command(os.Args[0], "-test.run", "^TestScopeServeListHangIsBounded$", "-test.v")
	cmd.Env = append(os.Environ(), "AF_SCOPE_HANG_CHILD=1", "AF_SCOPE_HANG_PIDS="+pidFile)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "did not answer before the deadline") {
		t.Fatalf("the hung api call must fail the helper (err=%v):\n%s", err, out)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("took %s, want it bounded by the budget", d)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake CLI recorded no PID (it never ran?): %v\n%s", err, out)
	}
	pids := strings.Fields(string(raw))
	if len(pids) < 2 {
		t.Fatalf("want the serve and at least one api PID, got %v", pids)
	}
	for _, f := range pids {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("bad PID %q: %v", f, err)
		}
		gone := false
		for i := 0; i < 50 && !gone; i++ {
			gone = syscall.Kill(pid, 0) == syscall.ESRCH
			if !gone {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if !gone {
			_ = syscall.Kill(pid, syscall.SIGKILL) // our own fake; do not leave it
			t.Errorf("fake CLI process %d is still alive after the helper failed", pid)
		}
	}
}
