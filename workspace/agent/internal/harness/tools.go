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
	"sync"
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
	// Approve gates a Mutates tool call. nil is FAIL-CLOSED: it declines every
	// one rather than running it unattended (approval.go) — a caller that wants
	// every Mutates call to run without asking passes AutoApprove explicitly.
	// Approval is a POLICY this package accepts from its caller, not one it
	// invents (decision 5: what makes approval real here is that this process
	// is the one actually about to run the tool, so blocking here really does
	// block it — unlike a CLI-driven kind, where the CLI decides).
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
	// RepeatWarnAfter and RepeatAbortAfter configure the repeated-tool-call gate
	// (repeat.go): once the SAME call (tool name + JSON-normalized arguments,
	// canonicalCall) has come back this many times in an unbroken row, the gate
	// intervenes instead of letting the loop run it again — a real failure mode
	// measured live (ADR 0093 lcpp harness trial against qwen3-coder-30b-a3b:
	// todo_write's tool NAME repeated 72 assistant turns straight — turns 50
	// through 121 of 162, verified by counting the log — with nothing else run
	// in between, never self-correcting; whether the ARGUMENTS were themselves
	// identical each time is unverified, since the log records tool names and
	// prose, not raw argument JSON — see repeat.go's own top comment for the
	// risk that leaves open). Each <= 0 (including a zero Runtime{}) uses the
	// package default (defaultRepeatWarnAfter / defaultRepeatAbortAfter) for
	// THAT field — the zero value keeps the gate ON, the same
	// fail-closed-by-default posture as Approve's nil check above, not silently
	// off. Set RepeatGateDisabled to turn the whole gate off instead.
	RepeatWarnAfter  int
	RepeatAbortAfter int
	// RepeatGateDisabled turns the repeated-tool-call gate off entirely. The
	// zero value (false) leaves it on — see RepeatWarnAfter's doc comment for
	// why that default was chosen deliberately rather than left to fall out of
	// Go's normal zero-value behaviour.
	RepeatGateDisabled bool
	// SystemPrompt is folded in front of every Send loop.go's Run makes (via
	// compact.go's BuildSendMessages, the same folding PrepareTurn already does at a
	// top-level turn boundary — see loop.go's own doc comment for why Run now does this
	// on EVERY iteration, not just once). "" is a legitimate value (no system prompt at
	// all), not "not configured" — SystemPrompt itself never gates whether the in-loop
	// compaction check below runs; Window does.
	SystemPrompt string
	// Window is the engine's real context window in tokens (typically EngineWindow's own
	// answer, threaded through by whatever caller builds this Runtime) — loop.go's Run
	// passes it straight to compact.go's NeedsCompaction/Compact on every iteration of its
	// tool loop, which is the gap ADR 0093's own live trial exposed: PrepareTurn's
	// compaction judgement previously only ran once per top-level turn, so a single task
	// whose tool loop ran long enough grew hist past the real window with nothing
	// checking it (a live qwen3-coder-30b-a3b run: 32772 tokens sent against a 32768
	// window, mid-task, no compaction ever attempted).
	//
	// <=0 (including a zero Runtime{}) is deliberately NOT treated as "no ceiling" here,
	// even though that is exactly what compact.go's own NeedsCompaction does with a <=0
	// window (decision 8: never invent a token ESTIMATE to compact against). Window is a
	// different quantity than the thing decision 8 forbids guessing — the actual input
	// token count Run's loop compares against it is still read exactly, from
	// client.InputTokens, every single iteration, never estimated. What Run defaults here
	// is the CEILING to compare that exact count against, when the caller never told it
	// one. Passing rt's <=0 straight through to NeedsCompaction would make "forgot to set
	// Window" silently reproduce the exact incident above — the same failure shape
	// Approve's nil check (approval.go) and RepeatWarnAfter/RepeatAbortAfter's <=0
	// fallback (repeat.go) both exist to avoid for their own knobs. So Run instead falls
	// back to defaultWindowFallback (loop.go), a number chosen deliberately LOW relative
	// to every real window this fleet has actually run (32768 in the incident above,
	// 262144 for another model seen live — see live_manual_test.go): a forgotten Window
	// compacts too EAGERLY rather than not at all, which just costs an extra
	// summarization turn, not a mid-task engine refusal. Set WindowDisabled to skip the
	// in-loop check entirely instead (e.g. a caller already certain its own tool loop
	// cannot run long enough to matter, that would rather not pay one extra
	// client.InputTokens round trip per tool-loop iteration).
	Window int
	// ReservedOutput is the output-token headroom compact.go's threshold formula adds on
	// top of the measured input tokens (PrepareTurn's own reservedOutput parameter, same
	// meaning here). Unlike Window, <=0 needs no special fallback: reserving nothing extra
	// only makes the threshold check LESS conservative, never unbounded — the exact
	// input-token count alone is still compared against Window's own fallback-protected
	// ceiling above.
	ReservedOutput int
	// WindowDisabled turns loop.go's in-loop compaction check off entirely, independently
	// of RepeatGateDisabled. The zero value (false) leaves the check on, at Window's own
	// value or defaultWindowFallback — see Window's own doc comment for why that default
	// was chosen deliberately rather than left to mean "no ceiling".
	WindowDisabled bool
	// MaxConsecutiveCompactions caps how many loop.go Run iterations in a row are allowed
	// to each need maybeCompact to fire, with no intervening iteration that got under
	// budget without compacting, before Run gives up with ErrCompactionThrashing instead of
	// continuing to spend Send calls. A live A/B (loop.go's own header comment) hit 86
	// compactions in a single Run call once mid-loop compaction started actually running —
	// each one folding away the model's own most recent work, which made it re-do that work
	// rather than converge. <=0 (including a zero Runtime{}) uses defaultMaxConsecutiveCompactions
	// (loop.go) — the same "zero value stays a real, protective number, never off" posture
	// as Window's own fallback and RepeatWarnAfter/RepeatAbortAfter's (repeat.go). There is
	// no "disabled" escape hatch for this one (unlike WindowDisabled/RepeatGateDisabled):
	// unbounded thrashing is exactly the failure mode this field exists to make loud instead
	// of silent, so there is no legitimate reason to want it off.
	MaxConsecutiveCompactions int
	// fileLocks serializes a single file's read-modify-write (runWrite/runEdit in
	// tools_fs.go) across the concurrent goroutines runToolCalls (loop.go) fans a
	// single turn's parallel tool_calls out into — see lockPath. Zero value is
	// ready to use (sync.Map needs no init), so every existing &Runtime{...}
	// literal in this package's tests keeps working unchanged.
	fileLocks sync.Map // map[string]*sync.Mutex, keyed by resolved absolute path
}

// lockPath returns the mutex that guards full (an already pathguard-resolved
// absolute path — see cwd.go's resolvePath) against a concurrent read-modify-write
// on the same file, creating one on first use. Keying by the RESOLVED path (not the
// tool call's raw argument) is what makes "a/../b.go" and "b.go" share a lock; two
// different files always get two different mutexes, so parallel tool_calls that
// touch different files still run concurrently (the point of ADR 0093 decision 5's
// parallel tool_calls — a single global lock would defeat it).
func (rt *Runtime) lockPath(full string) *sync.Mutex {
	v, _ := rt.fileLocks.LoadOrStore(full, &sync.Mutex{})
	return v.(*sync.Mutex)
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
