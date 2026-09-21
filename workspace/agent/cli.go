package main

import (
	"fmt"
	"io"
	"log"
	"runtime"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// subcommand is one entry in this binary's argument contract.
//
// The table below is the single source of truth: dispatch reads it, usage prints from it, and
// anything it does not name is rejected. That last part is the point. A chain of
// `os.Args[1] == "…"` tests with no default lets an unrecognised argument — `--version`, a typo —
// fall through into the boot path, and a second Agent rotates af's MCP server name and rewrites
// every CLI's config with it (measured 2026-09-10: the name the running sessions had been given
// was replaced and the old one removed). Inspecting the binary must never cost that.
type subcommand struct {
	name    string
	aliases []string
	// operands sketches the arguments for usage ("" when it takes none).
	operands string
	summary  string
	// hidden keeps internal plumbing (hooks, PATH shims, launchers the Agent execs itself)
	// dispatchable but out of the usage a human reads.
	hidden bool
	run    func(args []string)
}

func (s subcommand) matches(name string) bool {
	if s.name == name {
		return true
	}
	for _, a := range s.aliases {
		if a == name {
			return true
		}
	}
	return false
}

// subcommands is the whole argument contract, in usage order.
var subcommands = []subcommand{
	{
		name:     "af-db",
		operands: "<verb>",
		summary:  "per-working-copy Postgres for tests: up|url|env|reset|down|status",
		run:      runAFDB,
	},
	// On-demand pinned installers (docs/log/35 §35.7.2, jdk.go / install_tools.go /
	// install_kiro.go / install_postgres.go / install_pg_client.go / install_mysql.go): a lean
	// rootfs deployment installs these into the per-user home on first use, so the entrypoint
	// and the launch programs run them on demand as well as the member from a terminal.
	{
		name:     "install-jdk",
		operands: "<major>",
		summary:  "download the latest GA Temurin for this arch into the home volume",
		run:      runInstallJDK,
	},
	{
		name:    "install-chromium",
		summary: "download the pinned chromium + CJK font for the browser pane",
		run:     runInstallChromium,
	},
	{
		name:    "install-node",
		summary: "download the pinned node major",
		run:     runInstallNode,
	},
	{
		name:     "install-go",
		operands: "[version]",
		summary:  "download the pinned Go toolchain",
		run:      runInstallGo,
	},
	{
		name:    "install-awscli",
		summary: "download the AWS CLI + Session Manager plugin",
		run:     runInstallAWSCLI,
	},
	{
		// kiro is ~855MB extracted, so unlike the other agent CLIs it is not baked for
		// everyone: the launch program and the connection card run this on demand.
		name:    "install-kiro",
		summary: "download the pinned Kiro CLI into the home volume",
		run:     runInstallKiro,
	},
	{
		// Muse Code is proprietary (never in the distributed image) and ~299MB, so it takes
		// kiro's on-demand shape. Unlike kiro there is no launch guard behind it: muse is
		// managed-only, so there is no pane program to prepend one to — the driver's Resume
		// refuses and names this subcommand instead (ADR 0095 decision 8).
		name:    "install-muse",
		summary: "download the pinned Muse Code binary into the home volume",
		run:     runInstallMuse,
	},
	{
		name:     "install-postgres",
		operands: "[major]",
		summary:  "download the Zonky embedded-postgres binaries for this arch",
		run:      runInstallPostgres,
	},
	{
		name:     "install-pg-client",
		operands: "[major]",
		summary:  "download psql + libpq into ~/.local/bin",
		run:      runInstallPgClient,
	},
	{
		name:     "install-mysql",
		operands: "[major]",
		summary:  "download the MySQL tarball for this arch",
		run:      runInstallMySQL,
	},

	// --- internal plumbing: dispatched, not advertised ---

	{
		// git invokes this binary as its credential helper (`workspace-agent cred get`),
		// backed by the encrypted store. `bitbucket-cred` is kept as an alias for any git
		// config left over from before the unified helper.
		name:    "cred",
		aliases: []string{"bitbucket-cred"},
		summary: "git credential helper backed by the encrypted store",
		hidden:  true,
		run:     runCredHelper,
	},
	{
		// Transparent SVN auth: the PATH shim at /usr/local/bin/svn re-enters this binary,
		// which fills in the credential and execs the real svn — so the process svn's caller
		// sees is still svn. See svn_wrapper.go.
		name:    "svn-run",
		summary: "credential-injecting svn wrapper behind the PATH shim",
		hidden:  true,
		run:     runSvnWrapper,
	},
	{
		// Arch self-repair's Console face: what af-arch-repair could NOT put back only ever
		// reached the container's stdout. See arch_residue.go.
		name:    "notify-arch-residue",
		summary: "turn arch self-repair residue into a Console notification",
		hidden:  true,
		run:     runNotifyArchResidue,
	},
	{
		name:    "session-status",
		summary: "claude hook: record working/idle/question state",
		hidden:  true,
		run:     sessionx.RunSessionStatusHook,
	},
	{
		// Appended after the agent CLI by startSessionTmux, so a crash / OOM is recorded.
		name:    "record-exit",
		summary: "record why a session's pane terminated",
		hidden:  true,
		run:     runRecordExit,
	},
	{
		name:    "record-terminal",
		summary: "bounded terminal-output sink fed by tmux pipe-pane",
		hidden:  true,
		run:     runRecordTerminal,
	},
	{
		// Assistant chat tools, or the narrowly scoped interactive-session builtin,
		// selected by args.
		name:    "mcp-stdio",
		summary: "local stdio MCP server",
		hidden:  true,
		run:     mcpx.RunStdio,
	},
	{
		// Loads the encrypted store, injects the provider key as env and execs the real MCP
		// server — which keeps API keys out of any MCP config file.
		name:    "mcp-run",
		summary: "credential-injecting launcher for external MCP servers",
		hidden:  true,
		run:     mcpx.RunSubcommand,
	},
	{
		// claude pipes the session JSON (incl. rate_limits) on stdin every render; we persist
		// the 5h/weekly usage locally for the WsBar chip.
		name:    "statusline",
		summary: "claude statusLine capture",
		hidden:  true,
		run:     claude.RunStatusLine,
	},
	{
		// Exercises the production BrowserManager — pipe CDP, sandbox, two simultaneous Pages,
		// capture pacing — without booting the rest of the Agent. deploy/local/e2e-smoke.sh
		// is the caller.
		name:    "browser-smoke",
		summary: "image-only browser verification",
		hidden:  true,
		run:     runBrowserSmoke,
	},
}

// lookupSubcommand resolves a name or alias against the table.
func lookupSubcommand(name string) (subcommand, bool) {
	for _, sc := range subcommands {
		if sc.matches(name) {
			return sc, true
		}
	}
	return subcommand{}, false
}

// versionLine is what `workspace-agent --version` prints. buildVersion alone is ambiguous on a
// two-architecture image, and "which arch is this container" is the other question asked of a
// binary nobody may run bare.
func versionLine() string {
	return fmt.Sprintf("workspace-agent %s (%s/%s)", buildVersion, runtime.GOOS, runtime.GOARCH)
}

func writeUsage(w io.Writer) {
	var b strings.Builder
	b.WriteString("workspace-agent — the in-container Agent the Control Plane drives.\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  workspace-agent                       start the Agent (the container's CMD; not a way to inspect it)\n")
	b.WriteString("  workspace-agent serve                 the same, said explicitly\n")
	b.WriteString("  workspace-agent <subcommand> [args…]\n")
	b.WriteString("  workspace-agent --version | --help\n\n")
	b.WriteString("Subcommands:\n")
	for _, sc := range subcommands {
		if sc.hidden {
			continue
		}
		left := "  " + sc.name
		if sc.operands != "" {
			left += " " + sc.operands
		}
		// One space even when the left column overflows, or the two run together.
		b.WriteString(fmt.Sprintf("%-37s %s\n", left, sc.summary))
	}
	b.WriteString("\nInternal subcommands (hooks, PATH shims, launchers the Agent execs itself) are\n")
	b.WriteString("dispatched but not listed here; the full table is cli.go.\n")
	fmt.Fprint(w, b.String())
}

// dispatchCLI runs whatever os.Args[1:] names and reports the process exit code.
//
// handled=false means "boot the Agent", and only two inputs produce it: no arguments at all
// (the container's CMD, the native runtime's exec) and the explicit `serve`. Everything else
// either matches the table or is rejected with usage and exit 2 — before main touches the
// encrypted store, the MCP registry or any CLI's config.
func dispatchCLI(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "serve":
		return 0, false
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, versionLine())
		return 0, true
	case "help", "--help", "-h":
		writeUsage(stdout)
		return 0, true
	}
	if sc, ok := lookupSubcommand(args[0]); ok {
		sc.run(args[1:])
		return 0, true
	}
	fmt.Fprintf(stderr, "workspace-agent: unknown subcommand %q\n\n", args[0])
	writeUsage(stderr)
	return 2, true
}

func runBrowserSmoke([]string) {
	if err := browserx.RunBrowserImageSmoke(); err != nil {
		log.Fatal(err)
	}
}
