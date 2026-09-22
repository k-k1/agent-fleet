// mcp.go is lcpp's MCP wiring (ADR 0093 decision 6's "known だが materialize しない kind",
// 段2): lcpp resolves and calls external MCP tools itself, in-process, through internal/mcpc's
// client, rather than getting servers written into (or, for muse, streamed into) a vendor CLI's
// own config — there is no vendor CLI here at all (driver.go's own package doc: "in-process, no
// child process and no daemon"). This is exactly the glue harness/tools.go's own doc comment
// named as deferred: "wrapping an MCP tools/call into a Tool.Run so it can sit in the same
// Registry as E's builtins is glue code for a later phase (P2, when a caller wires E+F
// together)".
package lcpp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpc"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// mcpServersForSession is mcpreg.ForSession behind a var, the same func-var seam shape as
// newHarnessClient/harness.EngineToken (driver.go): a test can substitute it wholesale rather
// than relying on environment isolation alone. This package's own TestMain does exactly that —
// see its doc comment for why: the real mcpreg.ForSession(session.KindLcpp) always includes the
// "af" builtin, and even a SAFELY neutralized af (AF_AGENT_INSTALLED_BIN pointed at a no-op
// binary) still fails to connect in a test run, which would otherwise leave a stray
// NoteMCPError record in every unrelated driver_test.go test that runs a turn.
var mcpServersForSession = mcpreg.ForSession

// sessionNameEnvVar is the variable a session-side af MCP server reads to learn its owner
// (mcpx.mcpOwningSession's AF_SESSION_NAME contract). Named here rather than imported: it is
// mcpreg's own unexported sessionNameVar (thread_codex.go), duplicated as a literal the same way
// every other reader of this contract already does (mcpx.RunStdio, session_tmux.go).
const sessionNameEnvVar = "AF_SESSION_NAME"

// injectSessionName returns defs with this session's own name added to the builtin af
// definition's Env, so the stdio child dialStdio (mcpc/stdio.go) spawns can resolve its owning
// session (mcpOwningSession) the way generate_image and every other session-bound af tool
// require.
//
// Why this is needed at all: dialStdio starts a child's environment from os.Environ() — the
// Agent DAEMON's own process environment, not any one session's — and appends only def.Env on
// top. Unlike a TERMINAL session's CLI (which gets AF_SESSION_NAME from its tmux launch env and
// simply inherits it down to whatever it execs) or codex/muse (which ride a per-session THREAD
// or WIRE config the vendor host applies before spawning its own children), lcpp calls
// mcpc.Manager.Sync directly from the Agent daemon's own goroutine — there is no per-session
// process boundary for AF_SESSION_NAME to already be sitting in when dialStdio reads
// os.Environ(). So the daemon must hand it down explicitly, the same way muse's mcpServerConfig
// (internal/agents/muse/mcp.go) and codex's codexAFThreadEntry (mcpreg/thread_codex.go) each
// already do for their own transport.
//
// defs is whatever mcpServersForSession(session.KindLcpp) returned — mcpreg's own registry
// rows, potentially read again unchanged on a later turn or by a different session. This MUST
// NOT mutate any ServerDef or its Env map in place: doing so would write one session's name into
// a struct/map another call site might still be holding (mcpreg.Load's builtinDefs builds a
// fresh slice per call today, but nothing about this function may depend on that staying true).
// Only the one def identified as the builtin af server gets a copy; every other def — including
// a user's own external MCP server, even one also named "af" by coincidence — passes through by
// value, unmodified, exactly as ForSession returned it.
//
// Identification is by Origin+ID (mcpreg.OriginBuiltin + mcpreg.BuiltinAF), the same pair
// attach.go's own extraEnvVars and thread_codex.go's CodexThreadServers key on — never by Name,
// which a repository can shadow (docs/log/48 §8.4's AFServerName renames the row, not the ID).
func injectSessionName(defs []mcpreg.ServerDef, name string) []mcpreg.ServerDef {
	if name == "" || len(defs) == 0 {
		return defs
	}
	out := make([]mcpreg.ServerDef, len(defs))
	for i, d := range defs {
		if d.Origin == mcpreg.OriginBuiltin && d.ID == mcpreg.BuiltinAF {
			env := make(map[string]string, len(d.Env)+1)
			for k, v := range d.Env {
				env[k] = v
			}
			env[sessionNameEnvVar] = name
			d.Env = env
		}
		out[i] = d
	}
	return out
}

// mcpToolCallTimeout bounds one MCP tools/call. mcpc.Server.CallTool applies no timeout of its
// own by design (server.go's own doc comment: segment E is the one that knows the session kind
// driving the tool loop, so giving CallTool a ctx with the deadline it wants is E's job). No
// existing kind-specific convention covers this harness build (grep under internal/ finds no
// per-kind MCP call-timeout table anywhere), so this defines lcpp's own single default here:
// generous next to an ordinary external tool's network round trip, short enough that a
// genuinely hung server costs one tool result (runMCPTool below) rather than the whole turn.
const mcpToolCallTimeout = 60 * time.Second

// mcpSyncBudget bounds the CONNECT/handshake wait syncMCPServers pays for servers not already
// held, on top of whatever handshake timeout each individual server def carries on its own
// (server.go's own default: defaultHandshakeTimeout, 10s — applied SEQUENTIALLY across every
// not-yet-connected def in Sync's own toConnect loop, so N broken servers cost up to N*10s with
// no cap of this package's own). Left unbounded, a turn against an unreachable server blocks
// before generation even starts — measured live: PR #869's first CI run had no
// /usr/local/bin/workspace-agent installed, so every one of driver_test.go's pre-existing tests
// (which know nothing about MCP) blocked on connecting the "af" builtin until this exact class
// of timeout fired. 3s overrides ANY def's own longer TimeoutMS deliberately — this is the
// session's own ceiling on how long a broken attach's HANDSHAKE may cost a turn, not a
// per-server setting — and is short enough that a broken server never meaningfully delays a
// turn, generous enough for a real handshake on a healthy connection (well under 1s in this
// package's own tests and in a live af measurement).
//
// 🔴 This does NOT bound mgr.Sync's total wall time for every failure shape: a stdio server that
// ACCEPTS the connection but then hangs (never responds, never exits on stdin close) still pays
// mcpc's own stdioKillGrace (stdio.go, unexported, currently 3s, not part of this ctx) inside
// Connect's cleanup — handshake(ctx) itself does respect this deadline and returns promptly, but
// the Close() call that follows a failed handshake takes its own fixed time to escalate to Kill.
// That tail is itself bounded and paid at most once per mcpSyncBackoff window, never every turn.
//
// A var, not a const, so a test can shrink it rather than spend real wall-clock time waiting the
// budget out — mcpc's own stdioKillGrace documents the identical reason for its own var.
var mcpSyncBudget = 3 * time.Second

// mcpSyncBackoff is how long syncMCPServers leaves a server that just failed alone before
// retrying it. mcpSyncBudget alone still pays SOME cost every single turn forever for a server
// that stays down (a wasted connect attempt, bounded but not free); this is what makes a
// persistently broken server cost close to nothing after its first failed turn. 30s is short
// enough to notice a server coming back (or an admin's fix landing) within a couple of turns of
// ordinary interactive use, long enough that a burst of turns in the same broken-server session
// — an automated pipeline, a scripted retry — does not each re-pay the connect attempt.
//
// A var for the same reason mcpSyncBudget is: a test proving the backoff actually skips a retry
// (rather than merely bounding one) needs a short window it can wait out, or compare against,
// without spending real seconds doing it.
var mcpSyncBackoff = 30 * time.Second

// mcpFailure is one server's most recent Sync failure: the error text (folded into the
// deduplicated store note) and when syncMCPServers is next allowed to retry it.
type mcpFailure struct {
	err  string
	next time.Time
}

// syncMCPServers reconciles h.mcpMgr against defs for this turn and records anything that
// failed to connect — never fatally: a broken server costs only that server's own tools, not
// the turn (the acceptance condition "Sync が返す map[string]error はセッションを落とさない").
//
// Two things keep a broken server from costing every future turn in full:
//   - mcpSyncBudget bounds the Sync call itself, regardless of how many servers are configured
//     or what timeout any one of them declares.
//   - a server that just failed is skipped entirely (never even handed to mcpMgr.Sync) until
//     h.mcpFailures' own mcpSyncBackoff window elapses — see that constant's own doc comment.
//
// A failure is logged every time it is actually attempted (operator-visible immediately,
// matching every other non-fatal failure this driver already logs) but only appended to the
// store — and so shown to whoever later reads the record log — the first time the CURRENT set
// of known failures (h.mcpFailures, which persists across the backed-off turns too, unlike a
// signature built from this round's errs alone would) actually changes, so a server stuck
// offline for hours does not grow the store by one line per turn forever.
func (h *threadHandle) syncMCPServers(ctx context.Context, defs []mcpreg.ServerDef) {
	now := time.Now()
	h.mu.Lock()
	attempt := make([]mcpreg.ServerDef, 0, len(defs))
	for _, d := range defs {
		if f, cooling := h.mcpFailures[d.Name]; cooling && now.Before(f.next) {
			continue
		}
		attempt = append(attempt, d)
	}
	h.mu.Unlock()

	syncCtx, cancel := context.WithTimeout(ctx, mcpSyncBudget)
	defer cancel()
	errs := h.mcpMgr.Sync(syncCtx, attempt)

	h.mu.Lock()
	if h.mcpFailures == nil {
		h.mcpFailures = map[string]mcpFailure{}
	}
	for _, d := range attempt {
		if err, failed := errs[d.Name]; failed {
			log.Printf("lcpp: %s: mcp server %q: %v", h.name, d.Name, err)
			h.mcpFailures[d.Name] = mcpFailure{err: err.Error(), next: now.Add(mcpSyncBackoff)}
		} else {
			delete(h.mcpFailures, d.Name)
		}
	}
	// A server no longer in defs at all (removed or disabled) must not linger in backoff state
	// forever — it will never be attempted again to clear itself out otherwise.
	want := make(map[string]bool, len(defs))
	for _, d := range defs {
		want[d.Name] = true
	}
	for name := range h.mcpFailures {
		if !want[name] {
			delete(h.mcpFailures, name)
		}
	}

	sig := make(map[string]string, len(h.mcpFailures))
	for name, f := range h.mcpFailures {
		sig[name] = f.err
	}
	changed := !reflect.DeepEqual(h.mcpLastNotedErr, sig)
	h.mcpLastNotedErr = sig
	h.mu.Unlock()
	if !changed || len(sig) == 0 {
		return
	}

	names := make([]string, 0, len(sig))
	for name := range sig {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("MCP サーバへの接続に失敗しました（該当ツールは今ターン使えません）:")
	for _, name := range names {
		fmt.Fprintf(&sb, "\n- %s: %s", name, sig[name])
	}
	if _, aerr := h.store.AppendMCPSyncErrorNote(sb.String()); aerr != nil {
		log.Printf("lcpp: %s: recording mcp sync error note: %v", h.name, aerr)
	}
}

// mcpTools wraps h.mcpMgr's current tool set (whatever the syncMCPServers call just before it
// in runTurn left connected) as []harness.Tool, ready to fold into the same Registry as
// harness.BuiltinTools().
func (h *threadHandle) mcpTools() []harness.Tool {
	defs := h.mcpMgr.ToolDefs()
	out := make([]harness.Tool, 0, len(defs))
	for _, def := range defs {
		def := def // capture per iteration — each Run closure must call its OWN tool
		out = append(out, harness.Tool{
			Def:     def,
			Mutates: mcpToolMutates(def),
			Run: func(ctx context.Context, rt *harness.Runtime, argsJSON string) (string, error) {
				return runMCPTool(ctx, h.mcpMgr, def, argsJSON)
			},
		})
	}
	return out
}

// mcpToolMutates reports whether an external MCP tool must be gated behind approval before it
// runs. mcpc reads no MCP tool annotation (readOnlyHint or otherwise — grep confirms nothing
// under internal/mcpc decodes one from a server's tools/list), so there is no machine-checkable
// way to tell a read-only external tool from one that writes. ADR 0093 decision 5's fail-closed
// default (Runtime.Approve == nil refuses every Mutates call) already assumes "unknown means
// stop", and this keeps that same posture for every tool an external server advertises,
// deliberately wrong-side-safe rather than guessing which ones are harmless.
//
// This is the ONE place that judgement is made (the spawn instruction's own decision 1: "判定を
// 名前の付いた1か所に閉じ込める"). A later change that reads readOnlyHint, or adds an allowlist,
// replaces only this function's body — nothing else in this package inspects a def to decide
// Mutates.
func mcpToolMutates(def harness.ToolDef) bool {
	_ = def
	return true
}

// runMCPTool executes one MCP tools/call behind its own per-call timeout, applying the same
// three-way split tools_bash.go's runBash already applies to ITS per-call timeout:
//
//   - our own timeout firing (callCtx.Err() set, the caller's own ctx NOT yet done) is folded
//     into the tool result text, not returned as an error — the model sees its call timed out
//     and can retry or move on, and the rest of the turn keeps running.
//   - the caller's own ctx dying (Interrupt, or the whole turn's ctx cancelled) IS returned as
//     an error — loop.go's executeOne treats a context.Canceled/DeadlineExceeded error as
//     cancelling the whole batch, which is exactly right here too.
//   - an ordinary transport/protocol failure (server crashed, refused the call, etc.) is folded
//     into the result text as well, the same as bash's own "[exit error: ...]" convention,
//     rather than aborting the turn over one failed tool call.
func runMCPTool(ctx context.Context, mgr *mcpc.Manager, def harness.ToolDef, argsJSON string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, mcpToolCallTimeout)
	defer cancel()
	text, _, err := mgr.CallTool(callCtx, def.Name, json.RawMessage(argsJSON))
	switch {
	case callCtx.Err() != nil && ctx.Err() == nil:
		if text != "" {
			text += "\n"
		}
		return text + fmt.Sprintf("[mcp tool call timed out after %s]", mcpToolCallTimeout), nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil:
		return fmt.Sprintf("[mcp tool call failed: %s]", err), nil
	default:
		// isError (mgr.CallTool's second return) is already folded into text by mcpc's own
		// decodeToolResult — the model sees the server's own error content the same way it
		// would see any other tool's failure message.
		return text, nil
	}
}
