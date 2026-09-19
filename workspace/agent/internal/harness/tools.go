package harness

// tools.go is segment E's own vocabulary for a tool the loop (loop.go) can
// execute — Tool, Runtime, Registry. It is deliberately NOT part of types.go: D/E/F/G
// share ToolDef (the model-facing shape) and ToolCall (what the model asked for),
// but how a NAME actually gets EXECUTED is entirely E's concern. Segment F's job
// (ADR 0093 decision 9) is to resolve external MCP tools into []ToolDef; wrapping
// an MCP tools/call into a Tool.Run so it can sit in the same Registry as E's
// builtins is glue code for a later phase (P2, when a caller wires E+F together),
// not something E and F need to agree on today — this keeps the two segments from
// having to share an interface while written in parallel.

import (
	"context"
	"sort"
)

// Tool is one function this package knows how to execute, plus the model-facing
// ToolDef that advertises it.
type Tool struct {
	Def ToolDef
	// Mutates marks a tool that can change state outside the model's own head:
	// write, edit, bash. Plan mode (Registry.Defs) drops these from what is
	// offered to the model at all; the loop also re-checks Mutates at execution
	// time (executeOne) as a second gate — the same fail-closed shape as ADR
	// 0093 decision 5's "承認は本当にツールを止める": a tool list is advice to
	// the model, not the only thing preventing the call.
	Mutates bool
	// Run executes the call; argsJSON is the ToolCall's raw Arguments string
	// (segment D never parses this — see types.go's ToolCall doc). Returning a
	// non-nil error does not stop the loop: the error text becomes the RoleTool
	// message's Content, the same way a CLI-driven kind reports "command failed"
	// back to itself rather than crashing. The one exception is a
	// context.Canceled/DeadlineExceeded error, which the loop propagates instead
	// (cancelling the whole turn, not just this call).
	Run func(ctx context.Context, rt *Runtime, argsJSON string) (string, error)
}

// ApproveFunc gates a Mutates tool call before Run executes. It is asked once per
// call, with a caller-facing summary (approvalSummary) already computed. Returning
// (false, nil) is a plain decline — the loop folds that into a RoleTool message so
// the model can react instead of the turn erroring outright.
type ApproveFunc func(ctx context.Context, call ToolCall, tool Tool, summary string) (approved bool, err error)

// AskUserFunc answers the ask_user builtin (tools_ask.go).
type AskUserFunc func(ctx context.Context, question string, options []string) (answer string, err error)

// TodoItem is one entry of the todo_write builtin's list (tools_todo.go).
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // "pending" | "in_progress" | "completed"
}

// Runtime is the sandbox and policy one Run call (loop.go) executes builtin tools
// against. Nothing here knows about a session, a Console, or a Control Plane —
// callers in later phases (P2) fill Approve/AskUser with whatever UI they have.
type Runtime struct {
	// Cwd bounds every filesystem tool (read/write/edit/glob/grep/ls) and the
	// bash tool's working directory. Required: an empty Cwd fails every
	// filesystem tool call (resolvePath) rather than silently defaulting to the
	// host process's own directory.
	Cwd string
	// Plan, when true, removes Mutates tools from Registry.Defs and refuses to
	// execute one even if the model calls it anyway (ADR 0093 decision 5's plan
	// mode — a stale advertised list or a model that ignores its own tool list
	// must not be able to write).
	Plan bool
	// Approve gates a Mutates tool call. nil auto-approves every one — used by
	// tests and any deliberate "skip permissions" opt-in a caller makes
	// explicitly; approval is a POLICY this package accepts from its caller, not
	// one it invents (decision 5: what makes approval real here is that this
	// process is the one actually about to run the tool, so blocking here
	// really does block it — unlike a CLI-driven kind, where the CLI decides).
	Approve ApproveFunc
	// AskUser answers the ask_user builtin. nil makes ask_user report that no
	// interactive channel is available, rather than hanging the loop.
	AskUser AskUserFunc
	// Todos is this session's own to-do list, read and replaced in place by
	// todo_write (tools_todo.go). A higher layer reads it back for
	// TranscriptData.Tasks; this package only keeps it current.
	Todos []TodoItem
	// MaxOutputBytes caps one tool result's size (truncateOutput). <= 0 uses
	// defaultMaxToolOutput.
	MaxOutputBytes int
}

func (rt *Runtime) outputLimit() int {
	if rt == nil || rt.MaxOutputBytes <= 0 {
		return defaultMaxToolOutput
	}
	return rt.MaxOutputBytes
}

// Registry resolves a ToolCall's Name to the Tool that executes it.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry builds a Registry from tools, keyed by Def.Name. A later duplicate
// name overwrites an earlier one (last write wins), matching how a caller merging
// builtins with resolved MCP tools would want an explicit override to behave.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.byName[t.Def.Name] = t
	}
	return r
}

// Add registers or replaces one tool.
func (r *Registry) Add(t Tool) { r.byName[t.Def.Name] = t }

// Defs returns the []ToolDef to offer the model, sorted by name for a
// deterministic request body (stable across runs, easier to diff in tests/logs).
// When plan is true, every Mutates tool is dropped.
func (r *Registry) Defs(plan bool) []ToolDef {
	out := make([]ToolDef, 0, len(r.byName))
	for _, t := range r.byName {
		if plan && t.Mutates {
			continue
		}
		out = append(out, t.Def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) lookup(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}
