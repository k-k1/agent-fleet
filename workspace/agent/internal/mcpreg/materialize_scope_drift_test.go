//go:build drift

// The contract of project-local MCP scopes (docs/log/48 §8.4). It runs against the REAL
// agent CLIs, so the `drift` build tag keeps it out of `go test ./...`.
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
// subcommand demands a login; agy cannot start on this host (no RDRAND) and its MCP config
// is global-only, with no project scope at all.
package mcpreg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
			winner:      commandWinner,
			wantWinner:  "project",
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
				if !strings.Contains(out, want) {
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
