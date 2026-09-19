package chatx

// chat_providers_lcpp.go is ADR 0093 phase 1's P0 provider: a single-turn assistant-chat
// backend for the fleet's own llama-server engine, built on internal/harness's LLM client
// instead of driving a vendor CLI (there is none to drive).
//
// Scope (docs/log/99 §9.2, the ADR's phase 1 plan): Send only. No tool loop, no MCP tools,
// no system-prompt assembly beyond the persona every other chat provider already composes,
// no compaction — those are phases E/F/G of the SAME ADR, layered on internal/harness later.
// This provider sends whatever harness.Client.Send returns straight back as the reply.
//
// 🔴 lcppKind is NOT a session.Kind* constant. ADR 0093 decision 9 keeps kind registration
// (session.go's kind list, sessionx/agent.go, the managed driver, the transcript writer,
// Console) out of phase 1 entirely — an unregistered kind is normalized to session.KindClaude
// on every SESSION-shaped code path (sessionx/agent.go), which is exactly why this constant
// lives here instead: chatx's assistant-chat conversations are their own subsystem (chat.go's
// header comment), never touch session.go's kind switch, and "lcpp" only has to be a value
// ChatConversation.Agent can hold and this package's own maps/switches recognize — the same
// way ChatProviderKind's own `default: return c.Agent` already tolerates an unregistered
// string. Registering it as a session kind is phase 2's job.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const lcppKind = "lcpp"

// lcppEngineKey is the fixed catalogue key the chat role always uses
// (control-plane/engines.go:493's `{"llm","image","comfy"}`, engineSessionEnv's own "llm").
const lcppEngineKey = "llm"

// lcppEngineAvailable reports whether this deployment's self-hosted chat engine can be
// asked right now. This is deliberately NOT a CLI login check (there is no CLI): the
// Control Plane's /internal/engine/catalog already drops an engine with no enabled model
// entirely (control-plane/engine_gateway.go's catalog(), `row == nil { continue }`), so
// existence in harness.EngineAvailable's read of that catalogue already means "has at
// least one enabled model", not merely "is defined".
func lcppEngineAvailable() bool {
	if harness.EngineAvailable == nil {
		return false // this Agent build has no engines seam wired at all (no CP configured)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return harness.EngineAvailable(ctx, lcppEngineKey)
}

// lcppChat drives the fleet's own engine for one turn.
type lcppChat struct{}

func (lcppChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	model := strings.TrimSpace(chatModelFor(c, lcppKind))
	call := usagex.Call{Kind: lcppKind, ModelReq: model} // usage ledger (ADR 0029 §3)
	defer usagex.RecordCall(ctx, &call, time.Now())

	if model == "" {
		// Unlike claude/codex/opencode, lcpp has no deployment-wide default to fall back to
		// (recommendedAssistantModel has no lcpp case, matching cursor's "auto" — but
		// llama-server's router has no "auto": every request names a model). The conversation
		// (or its assistant) has to pin one from the catalogue.
		return "", errors.New("lcpp: no model is configured for this conversation")
	}
	if harness.EngineToken == nil {
		return "", errors.New("lcpp: this Agent build has no self-hosted engines configured")
	}
	conn, ok := harness.EngineToken(ctx, lcppEngineKey, c.ID)
	if !ok {
		return "", errors.New("lcpp: no self-hosted chat engine is reachable")
	}

	turn, err := harness.NewClient(conn, model).Send(ctx, lcppMessages(c, prompt), nil)
	if err != nil {
		return "", fmt.Errorf("lcpp: %w", err)
	}
	call.SetTotals(turn.Usage.PromptTokens, turn.Usage.CompletionTokens, 0, 0)
	reply := strings.TrimSpace(turn.Content)
	if reply == "" {
		return "", errors.New("no response from lcpp")
	}
	call.OK = true
	turnModel := turn.Model
	if turnModel == "" {
		turnModel = model
	}
	c.NoteTurnModel(turnModel)
	window := 0
	if harness.EngineWindow != nil {
		window = harness.EngineWindow(ctx, lcppEngineKey)
	}
	// fresh=prompt tokens, no cache split (llama-server's usage object carries none) — the
	// same shape codex's context snapshot uses (chat_usage.go's setChatContext).
	setChatContext(c, turn.Usage.PromptTokens, 0, 0, window, turnModel)
	return reply, nil
}

// lcppMessages builds the request llama-server sees for this turn out of the conversation's
// own stored history (ADR 0093 phase 1: "the harness composes continuity from the history it
// is handed" — there is no native provider session to resume, unlike claude/codex/opencode's
// resume ids, because the gateway's chat-completions route is stateless).
//
// 🔴 Only TWO of prov.Send's six call sites append this turn's raw user text to c.Messages
// right before calling Send (chat_handlers.go:452,536 — the ordinary user-turn path).
// Compaction (chat_compact.go:127) and a report auto-turn (chat_report.go:609) call Send
// with a `prompt` that has NOTHING to do with c.Messages' last entry (CompactPrompt /
// reportsPrompt build it from scratch) — for those, c.Messages' last entry is an ordinary
// PAST turn that must stay in history, and dropping it unconditionally silently erased
// exactly the turn being summarized or reported on.
//
// So the last stored entry is dropped only when it can be PROVEN to be this same turn's own
// raw text: it is a non-empty user row, and prompt ENDS WITH it — every injector that folds a
// preamble in front (InjectPendingReports:317, InjectPlan:244, and chat_plan.go:251's own
// "summary -> plan -> the actual prompt" ordering) puts the caller's raw text LAST, never
// merely somewhere inside. HasSuffix rather than Contains on purpose: a mid-string match would
// also fire when compaction's own prompt (CompactPrompt, built from c.Plan) happens to contain
// the last turn's short text verbatim — a plan can quote an earlier "OK" or "はい" — which
// would silently resurrect the very bug this guard exists to prevent. Any other shape —
// compaction, a report auto-turn, or a last entry that isn't a plain user row — drops nothing
// and lets prompt ride as an ADDITIONAL final user message instead.
//
// report/notice rows (chatx's own presentation cards) are not replayed as chat history either:
// no other provider replays them as history — a report rides the NEXT prompt via
// InjectPendingReports, exactly like here.
func lcppMessages(c *ChatConversation, prompt string) []harness.Message {
	hist := c.Messages
	if n := len(hist); n > 0 {
		if last := hist[n-1]; last.Role == "user" && last.Content != "" && strings.HasSuffix(prompt, last.Content) {
			hist = hist[:n-1]
		}
	}
	msgs := make([]harness.Message, 0, len(hist)+2)
	msgs = append(msgs, harness.Message{Role: harness.RoleSystem, Content: c.personaOf()})
	for _, m := range hist {
		switch m.Role {
		case "user":
			msgs = append(msgs, harness.Message{Role: harness.RoleUser, Content: m.Content})
		case "assistant":
			msgs = append(msgs, harness.Message{Role: harness.RoleAssistant, Content: m.Content})
		}
	}
	return append(msgs, harness.Message{Role: harness.RoleUser, Content: prompt})
}
