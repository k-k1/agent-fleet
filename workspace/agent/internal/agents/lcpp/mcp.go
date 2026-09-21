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

// mcpToolCallTimeout bounds one MCP tools/call. mcpc.Server.CallTool applies no timeout of its
// own by design (server.go's own doc comment: segment E is the one that knows the session kind
// driving the tool loop, so giving CallTool a ctx with the deadline it wants is E's job). No
// existing kind-specific convention covers this harness build (grep under internal/ finds no
// per-kind MCP call-timeout table anywhere), so this defines lcpp's own single default here:
// generous next to an ordinary external tool's network round trip, short enough that a
// genuinely hung server costs one tool result (runMCPTool below) rather than the whole turn.
const mcpToolCallTimeout = 60 * time.Second

// syncMCPServers reconciles h.mcpMgr against defs for this turn and records anything that
// failed to connect — never fatally: a broken server costs only that server's own tools, not
// the turn (the acceptance condition "Sync が返す map[string]error はセッションを落とさない").
// A failure is logged every time it recurs (operator-visible immediately, matching every other
// non-fatal failure this driver already logs) but only appended to the store — and so shown to
// whoever later reads the record log — the first time a given server's failure signature
// actually changes, so a server stuck offline for hours does not grow the store by one line per
// turn forever.
func (h *threadHandle) syncMCPServers(ctx context.Context, defs []mcpreg.ServerDef) {
	errs := h.mcpMgr.Sync(ctx, defs)
	sig := make(map[string]string, len(errs))
	for name, err := range errs {
		log.Printf("lcpp: %s: mcp server %q: %v", h.name, name, err)
		sig[name] = err.Error()
	}

	h.mu.Lock()
	changed := !reflect.DeepEqual(h.mcpLastSyncErr, sig)
	h.mcpLastSyncErr = sig
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
