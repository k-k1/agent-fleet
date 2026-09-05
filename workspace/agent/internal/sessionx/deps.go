package sessionx

// deps.go — one page collecting every hand sessionx reaches out to the caller
// (package main).
//
// The session family is "the product as the Console sees it", so its outward dependencies
// come down to a handful of generic helpers plus the two pieces of state that sit outside
// the family (the MCP conversation id and the operator turn). This does not hide that seam;
// it gathers it in one place so it can be counted (the same shape as internal/gitx/deps.go
// and internal/memoryx/deps.go):
//
//   - sessionx does not import main (it cannot; the dependency already runs the other way)
//   - so "call a function in main" is taken as a function value instead
//   - wiring happens once at boot (the init in main's session_wiring.go), and Configure
//     panics on what is missing. Filling a gap with a default silently would, for instance,
//     leave `MaxUploadBytes` at 0 so every upload fails as "too large", or leave
//     `ErrCodeLocked` empty so the Console shows a raw code. Crashing beats running quietly.
//
// Error codes are not redeclared on the sessionx side: with two sources, the day one of them
// is fixed the screen shows a raw code. `errcodes.go` is a cross-cutting table read by 15
// files across the agent, so it belongs in package main and is passed in here as values
// (same treatment as gitx / memoryx).
//
// sessionx's own tests have no main, so the fakes are wired by init rather than TestMain
// (see deps_test.go).

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Deps is the outside world as sessionx sees it. It holds no type from main (the moment it
// does, the seam stops closing), so it can grow without adding imports.
type Deps struct {
	// --- Generic helpers (main.go / connections.go / repo_prompts.go) ---
	//
	// Each is the kind of function that exists exactly once somewhere in main; keeping a
	// copy means the two drift silently the day one of them is fixed (README §0.5
	// "original and copy").
	EnvOr            func(key, def string) string
	FirstNonEmpty    func(vals ...string) string
	SplitFrontmatter func(s string) (map[string]string, string)

	// --- Files (fs.go) ---
	//
	// BrowseRoot is the root that attachments and pasted images are stored under.
	// MaxUploadBytes is the size cap, and an unwired 0 means "a cap of 0 bytes" = reject
	// everything, so the zero value is not allowed.
	BrowseRoot     func() string
	MaxUploadBytes func() int64

	// --- Repositories (svn.go / repo_jobs.go) ---
	//
	// The session list shows worktree state alongside, so non-git (svn) and the number of
	// running import jobs are read from outside the family.
	IsSvnRepo       func(dir string) bool
	RepoJobsRunning func() int

	// --- Closing the usage ledger (usage_fold.go) ---
	//
	// Stopping or deleting a session closes its usage ledger. usage_fold.go stays in main:
	// the closing is driven by `usage_ledger_test.go` / `usage_dedup_test.go`, whose
	// subject is main's usage_ledger.go, and pulling it into the family would move the
	// ledger's tests away from the ledger. gitx takes the same function across its own
	// seam (gitx/deps.go).
	FinalizeSessionUsage  func(m session.Meta)
	MaybeFoldSessionUsage func()

	// --- Terminal history (terminal_history.go) ---
	//
	// Session teardown deletes the history file. Filling an unwired field with a no-op
	// would leave files silently undeleted, so the zero value is not allowed here either.
	RemoveTerminalHistory func(name string)

	// --- Toolchains (env_toolchains.go) ---
	ToolchainShellPrefix func() string

	// --- Bridge / operator (mcp_wiring.go / bridge_operator.go) ---
	//
	// MCPConvID takes a var through a function. main's `mcpConvID` is mutable state that
	// `mcp_wiring.go` rewrites at runtime, so taking it by value freezes it at the value it
	// had when it was wired and approval prompts then always go to the stale conversation.
	// (The same "far side you must not copy" shape as README's `var usageMu = usagex.Mu`.
	// This one is not a lock, so vet stays quiet — hence the explicit function.)
	MCPConvID       func() string
	RunOperatorTurn func(conv, text string) (string, error)

	// --- Stable error codes (errcodes.go) ---
	//
	// Strings paired with the Console's i18n catalogue (ERR_TEXT in
	// console/src/core/api/client.ts). Not redeclared on the sessionx side.
	// ErrCodeAgentNotConnected covers a permanent cause (not signed in, not connected) that
	// kept the shared daemon from being started. It is separate from runtime_failed (a
	// transient failure) because both the Console's wording and isTransientErr turn on
	// "does waiting fix it" (runtime_err.go).
	ErrCodeAgentNotConnected      string
	ErrCodeChatConversationNotFnd string
	ErrCodeForkAtUnsupported      string
	ErrCodeForkBadAnchor          string
	ErrCodeForkMissingDir         string
	ErrCodeForkUnsupportedKind    string
	ErrCodeLocked                 string
	ErrCodePasteTooLarge          string
	ErrCodePasteUnsupportedAgent  string
	ErrCodePasteUnsupportedKind   string
	ErrCodeTitleFeatureDisabled   string
	ErrCodeTitleNoContent         string
}

var deps Deps

// Configure is called exactly once at boot (main's session_wiring.go, or the init in
// sessionx's own tests). Nothing runs with a gap left in it.
//
// Completeness is taken with reflect, never a hand-written list: a hand-written map misses
// a field that was added later, and nothing happens when it does. The dangerous ones are
// the value types. An unwired func dies on a nil dereference, but a string like
// `ErrCodeLocked` runs on quietly as empty and the Console receives `""` as the code. This
// struct already holds 12 value-typed fields.
//
// To make an exception, tag the field `sessionx:"optional"` — there is no separate list, so
// an exception is always visible at the declaration.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("sessionx") == "optional" {
			continue
		}
		if v.Field(i).IsZero() {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("sessionx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
	errCodeAgentNotConnected = d.ErrCodeAgentNotConnected
	errCodeChatConversationNotFnd = d.ErrCodeChatConversationNotFnd
	errCodeForkAtUnsupported = d.ErrCodeForkAtUnsupported
	errCodeForkBadAnchor = d.ErrCodeForkBadAnchor
	errCodeForkMissingDir = d.ErrCodeForkMissingDir
	errCodeForkUnsupportedKind = d.ErrCodeForkUnsupportedKind
	errCodeLocked = d.ErrCodeLocked
	errCodePasteTooLarge = d.ErrCodePasteTooLarge
	errCodePasteUnsupportedAgent = d.ErrCodePasteUnsupportedAgent
	errCodePasteUnsupportedKind = d.ErrCodePasteUnsupportedKind
	errCodeTitleFeatureDisabled = d.ErrCodeTitleFeatureDisabled
	errCodeTitleNoContent = d.ErrCodeTitleNoContent
}

// Wired returns the current wiring. It is a read port for a caller checking end to end that
// the wiring is live; sessionx itself does not use it.
//
// Configure catches only what is unwired, never what is wired wrong. The 12 error codes are
// all the same `string` type, so swapping two of them trips neither the type checker nor the
// reflect completeness check. main's session_wiring_test.go stops that by matching them
// against the real constants.
func Wired() Deps { return deps }

// Taken by value. Configure writes them once; everything after that only reads.
var (
	errCodeAgentNotConnected      string
	errCodeChatConversationNotFnd string
	errCodeForkAtUnsupported      string
	errCodeForkBadAnchor          string
	errCodeForkMissingDir         string
	errCodeForkUnsupportedKind    string
	errCodeLocked                 string
	errCodePasteTooLarge          string
	errCodePasteUnsupportedAgent  string
	errCodePasteUnsupportedKind   string
	errCodeTitleFeatureDisabled   string
	errCodeTitleNoContent         string
)

// What follows are thin delegations under the same names the code used before the move, so
// that the 12,393 lines that came over need no edits. This is the only outward window.
func envOr(key, def string) string { return deps.EnvOr(key, def) }

func firstNonEmpty(vals ...string) string { return deps.FirstNonEmpty(vals...) }

func splitFrontmatter(s string) (map[string]string, string) { return deps.SplitFrontmatter(s) }

func browseRoot() string { return deps.BrowseRoot() }

func maxUploadBytes() int64 { return deps.MaxUploadBytes() }

func isSvnRepo(dir string) bool { return deps.IsSvnRepo(dir) }

func repoJobsRunning() int { return deps.RepoJobsRunning() }

func removeTerminalHistory(name string) { deps.RemoveTerminalHistory(name) }

func finalizeSessionUsage(m session.Meta) { deps.FinalizeSessionUsage(m) }

func maybeFoldSessionUsage() { deps.MaybeFoldSessionUsage() }

func toolchainShellPrefix() string { return deps.ToolchainShellPrefix() }

func runOperatorTurn(conv, text string) (string, error) { return deps.RunOperatorTurn(conv, text) }

// mcpConvID was a variable on the main side (mcp_wiring.go rewrites it at runtime). A
// variable cannot be shared across packages, so here alone "reading a variable" becomes
// "calling a function" — the three sites in bridge_approval.go become `mcpConvID()`. Copying
// the value would send approval prompts to the stale conversation forever, so this one hop
// cannot be skipped.
func mcpConvID() string { return deps.MCPConvID() }

// A thin skin over a pure internal package is not wired: it has no behaviour, so there is no
// room for a copy to go stale. main's homeDir is the same one line.
func homeDir() string { return paths.HomeDir() }
