package mcpx

// deps.go — every hand mcpx reaches out to the caller (package main) with, collected on one
// sheet.
//
// The MCP stdio server exposes nearly the agent's whole feature surface as tools (session
// creation, peer send, completion report, the approval gate, UI preferences, tool version
// pins, …), so extracting the family inevitably scatters its outward dependencies across
// main's families. Rather than hide that seam, this file gathers it in one place where it can
// be counted:
//
//   - mcpx does not import main (it cannot; the dependency already runs the other way)
//   - so "call a function in main" is received as a function value
//   - wiring happens once at startup (the init in main's mcp_wiring.go). Configure checks for
//     gaps and panics: filling a missing wire with a default silently opens holes such as an
//     approval gate that waves everything through, so failing beats running quietly
//
// mcpx's own tests have no main, so TestMain wires fakes (see deps_test).

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Deps is the outside world as mcpx sees it, per the note above. It holds none of main's
// types (the moment it does, the seam stops closing), so growing it adds no imports.
type Deps struct {
	// Session title (session.go). The limit is NOT held again on the mcpx side: a number
	// kept per layer produces the kind of failure that only shows at the instant of launch
	// (memory: session-title-limit-one-source).
	CleanTitle           func(s string) (string, bool)
	SessionTitleMaxRunes int

	// Session-to-session messages (session_peer.go).
	PeerIntentNames       []string
	PeerReachableSessions func(from string) []session.Meta

	// The completion report kind (chat_report.go).
	ReportKindSelfReport string

	// Operator approval (bridge_approval.go). The first thing that must never get a
	// default: turning an unwired gate into "wave through" erases approval itself.
	ApprovalGate      func(op, summary string) error
	ApprovalLabel     func(op string) string
	ShellCreateTarget func(dir, prompt string) string
	ShellSendTarget   func(name, prompt string) string
	SessionIsShell    func(name string) bool

	// UI preferences (ui_prefs.go) and claude's settings wiring (session_status.go).
	ReadUIPrefs                func() map[string]any
	EnsureClaudeSettingsWiring func(kind string)

	// Repository resolution (git.go): looks up the worktree from the HTTP path.
	RepoAnyDirFromPath func(w http.ResponseWriter, r *http.Request) (string, bool)

	// Tool version pins and installation (env_tool_versions.go / install_tools.go).
	ReadBuildPins      func() map[string]string
	AgentFleetShareDir func() string
	InstallGrafanaMCP  func(ver string) (string, error)

	// Writing an SSM session's config (session_ssm.go).
	WriteSSMConfig func(path string, s session.SSMMeta) error

	// --- the three flags that decide the tool set ---
	//
	// The storage stays on the caller's side. package main's tests switch the tool set by
	// assigning directly (`mcpWriteEnabled = true`), and a var alias here would never
	// receive that assignment: the stub would be dead while the test stayed green (hit
	// twice already). Only the read and write ports are held here; the value lives in
	// exactly one place.
	//
	//   WriteEnabled          … `--write`. Whether to expose write tools (assistant surface)
	//   SelfReportOnly        … `--self-report`. The session surface (self-report only)
	//   SessionChromiumEnabled… set only when `--self-report --chromium-attach` are given
	//                            together. RunStdio, not this flag, decides the conjunction,
	//                            so a lone --chromium-attach cannot widen the assistant
	//                            surface
	WriteEnabled              func() bool
	SetWriteEnabled           func(bool)
	SelfReportOnly            func() bool
	SetSelfReportOnly         func(bool)
	SessionChromiumEnabled    func() bool
	SetSessionChromiumEnabled func(bool)

	// Conversation id (`--conv <id>`). Its storage is on the caller's side too, and it is
	// the classic value a var alias cannot carry: main may take a copy at startup, but this
	// id is decided AFTERWARDS (when the arguments are parsed), so the copy freezes as the
	// empty string and bridge_approval.go posts approvals without knowing which
	// conversation they belong to. main's tests read the parse RESULT (did conv land?), so
	// a one-way notification is not enough — both the read and the write live here.
	ConvID    func() string
	SetConvID func(id string)
}

var deps Deps

// Configure is called exactly once at startup (main's mcp_wiring.go, or the init of mcpx's
// tests). Never run with a gap: an approval gate or a session-title limit that works by
// accident of the zero value is a hole nobody can see.
//
// Completeness is taken with reflect, never a hand-written list. A check against a map of
// field names leaks whenever a field is added, and nothing happens when it does. Value types
// are the dangerous ones: an unwired function type dies on a nil dereference, but a value type
// such as SessionTitleMaxRunes runs on quietly as the zero value, and a title limit of 0 goes
// unnoticed. This struct already holds three value types, and that number only grows.
//
// To make an exception, tag the field `mcpx:"optional"` — no separate list, so an exception is
// always visible at the declaration.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("mcpx") == "optional" {
			continue
		}
		if unwired(v.Field(i)) {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("mcpx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
	sessionTitleMaxRunes = d.SessionTitleMaxRunes
	peerIntentNames = d.PeerIntentNames
	reportKindSelfReport = d.ReportKindSelfReport
}

// unwired decides what counts as "not wired". Besides the zero value it treats an empty slice
// and a number of 0 or less as a gap: `[]string{}` and `-1` are not zero values, but as
// dependencies they mean the same as unwired.
func unwired(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice:
		return v.Len() == 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() <= 0
	default:
		return v.IsZero()
	}
}

// Wired returns the current wiring. It is a read port for the CALLER to check end-to-end that
// the wiring is live; mcpx itself does not use it.
//
// Configure catches only what is UNWIRED, never what is wired WRONG: an `ApprovalGate` that
// always approves and a `ConvID` that returns the empty string both pass quietly — and since
// the wiring is a single line, it is the place a future cleanup is most likely to touch.
func Wired() Deps { return deps }

// The ones received by value. Configure writes them once; afterwards they are read-only.
var (
	sessionTitleMaxRunes int
	peerIntentNames      []string
	reportKindSelfReport string
)

// Thin delegations keeping the names the moved code already used, so none of it needed an
// edit; this is the one outward window.
func cleanTitle(s string) (string, bool) { return deps.CleanTitle(s) }

func peerReachableSessions(from string) []session.Meta { return deps.PeerReachableSessions(from) }

func bridgeApprovalGate(op, summary string) error { return deps.ApprovalGate(op, summary) }

func approvalLabel(op string) string { return deps.ApprovalLabel(op) }

func shellCreateTarget(dir, prompt string) string { return deps.ShellCreateTarget(dir, prompt) }

func shellSendTarget(name, prompt string) string { return deps.ShellSendTarget(name, prompt) }

func sessionIsShell(name string) bool { return deps.SessionIsShell(name) }

func readUIPrefs() map[string]any { return deps.ReadUIPrefs() }

func ensureClaudeSettingsWiring(kind string) { deps.EnsureClaudeSettingsWiring(kind) }

func repoAnyDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	return deps.RepoAnyDirFromPath(w, r)
}

func readBuildPins() map[string]string { return deps.ReadBuildPins() }

func agentFleetShareDir() string { return deps.AgentFleetShareDir() }

func installGrafanaMCP(ver string) (string, error) { return deps.InstallGrafanaMCP(ver) }

func writeSSMConfig(path string, s session.SSMMeta) error { return deps.WriteSSMConfig(path, s) }

// Tool-set flags (stored on the caller's side; see the note on Deps above).
func writeEnabled() bool           { return deps.WriteEnabled() }
func selfReportOnly() bool         { return deps.SelfReportOnly() }
func sessionChromiumEnabled() bool { return deps.SessionChromiumEnabled() }

func setWriteEnabled(v bool)           { deps.SetWriteEnabled(v) }
func setSelfReportOnly(v bool)         { deps.SetSelfReportOnly(v) }
func setSessionChromiumEnabled(v bool) { deps.SetSessionChromiumEnabled(v) }

func convID() string      { return deps.ConvID() }
func setConvID(id string) { deps.SetConvID(id) }

// Pure standard-library wrappers are not wired: they have no behaviour, so a copy cannot go
// stale.
func homeDir() string { return paths.HomeDir() }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
