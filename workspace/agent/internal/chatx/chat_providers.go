package chatx

// The assistant chat's provider implementations (claude/codex/opencode/agy/cursor driven
// through their headless CLIs) and the plumbing around launching those CLIs.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// --- providers ---

// ChatProvider drives one agent's CLI in non-interactive mode. Send appends the
// assistant reply's text as its return value and may mutate c's resume handles.
type ChatProvider interface {
	Send(ctx context.Context, c *ChatConversation, prompt string) (string, error)
}

var ChatProviders = map[string]ChatProvider{
	session.KindClaude:   ClaudeChat{},
	session.KindCodex:    codexChat{},
	session.KindOpencode: opencodeChat{},
	session.KindAgy:      agyChat{},
	session.KindCursor:   cursorChat{},
	// lcppKind is NOT a session.Kind* constant (ADR 0093 phase 1 §2 — the kind is not
	// registered yet; see chat_providers_lcpp.go's header comment for where and why).
	lcppKind:         lcppChat{},
	session.KindMuse: museChat{},
}

// --- backend availability (claude-less workspaces, docs/log/19) ----------------------
//
// A workspace may have ONLY codex or ONLY opencode connected. Every headless-LLM
// feature (assistant chat, ask_assistant, title/branch suggestion) must then run on
// what IS available, so:
//   - builtin assistants take the preferred available backend at creation,
//   - a conversation pinned to an unavailable backend falls back at send time,
//   - one-shot calls (titles) pick the preferred backend directly.
// Availability checks shell out (`claude auth status` / `codex login status`), so
// results are cached for a minute.

var (
	headlessAvailMu sync.Mutex
	headlessAvailAt = map[string]time.Time{}
	headlessAvail   = map[string]bool{}
	// headlessAvailInFlight coalesces concurrent cold checks for the same kind into ONE exec —
	// the postmortem for this: WarmOneShotKind (added for docs/log/103's /ai-assist/resolution)
	// gave headlessAgentAvailable concurrent callers it never had before, and this function
	// used to have no guard against that at all. A test that polled the endpoint every 20ms
	// while the cache was cold spawned a fresh `claude auth status` PER concurrent caller with
	// no cap — measured: ~490 `claude` processes, ~25GiB RSS, OOM-killed the container. The
	// cache write only happens after an exec returns, so with no guard, every caller in the
	// window before the first exec finishes starts its OWN exec, and the more that pile up the
	// slower each one gets — a positive feedback loop with no natural ceiling. This map is that
	// ceiling: a cold kind has exactly one exec in flight at a time; every other caller for the
	// same kind waits on it and shares its answer, no matter how many callers or how tight a
	// polling loop asks (chat_providers_avail_test.go proves the exec count with an
	// instrumented, deliberately slow fake in place of headlessAvailCheck — never the real CLI).
	headlessAvailInFlight = map[string]chan struct{}{}
)

// headlessAvailCheck is the real per-kind check, behind a seam so a test can verify
// headlessAgentAvailable's in-flight dedup with an instrumented fake instead of ever starting a
// real CLI in a loop (see headlessAvailInFlight's comment for why that matters here
// specifically).
var headlessAvailCheck = func(kind string) bool {
	switch kind {
	case session.KindClaude:
		return claude.LoggedIn()
	case session.KindCodex:
		return codex.LoggedIn()
	case session.KindOpencode:
		// binary present AND actually usable (stored key / oauth / explicit free-tier
		// opt-in) — opencode.Available() alone used to let assistant chat silently fall
		// back to opencode's zero-auth free tier for any unconfigured workspace, which
		// some tenants' security policy forbids. Default OFF, opt-in only.
		return opencode.Available() && opencode.Connected()
	case session.KindAgy:
		return agy.SignedIn()
	case session.KindCursor:
		return cursor.LoggedIn()
	case lcppKind:
		// Not a CLI login check — there is no CLI (ADR 0093 phase 1 §2). "available" here
		// means this deployment's self-hosted chat engine exists AND has at least one
		// enabled model right now; see lcppEngineAvailable's own comment for why that is
		// the whole check (the Control Plane's catalogue already drops an engine with none).
		return lcppEngineAvailable()
	case session.KindMuse:
		return museAvailable()
	}
	return false
}

// headlessAgentAvailable reports (cached) whether a backend's CLI is authenticated. At most one
// headlessAvailCheck call is ever in flight per kind at a time — see headlessAvailInFlight.
func headlessAgentAvailable(kind string) bool {
	headlessAvailMu.Lock()
	if t, ok := headlessAvailAt[kind]; ok && time.Since(t) < time.Minute {
		v := headlessAvail[kind]
		headlessAvailMu.Unlock()
		return v
	}
	if wait, inFlight := headlessAvailInFlight[kind]; inFlight {
		headlessAvailMu.Unlock()
		<-wait // the leader's exec is already running; take its answer, start none of our own
		headlessAvailMu.Lock()
		// Re-check freshness rather than reading headlessAvail[kind] straight: a leader that
		// panicked clears the in-flight slot (the defer below) WITHOUT writing a fresh cache
		// entry, so what is sitting in the map can be a much older result from before this
		// call's own minute-freshness check ran. Reading it unconditionally would hand the
		// waiter an expired answer instead of the "false" a dead leader is documented to give.
		t, ok := headlessAvailAt[kind]
		v := ok && time.Since(t) < time.Minute && headlessAvail[kind]
		headlessAvailMu.Unlock()
		return v
	}
	done := make(chan struct{})
	headlessAvailInFlight[kind] = done
	// Read the seam under the lock and call the COPY below. A plain read here races with
	// SetHeadlessAvailCheckForTest: warming goroutines outlive the test that started them
	// (they are not tied to its lifetime), so the next test's swap lands while a straggler is
	// still reading this var — measured as a -race failure in `go test -race -count=20`.
	check := headlessAvailCheck
	headlessAvailMu.Unlock()

	// Deferred, not inline: headlessAvailCheck shells out to a vendor CLI, and a panic below
	// would otherwise leave this kind's in-flight entry in the map with its channel never
	// closed — every later caller would then block forever on the wait above, permanently
	// wedging assistant chat and every one-shot, not just the warming that introduced the
	// concurrency. A leader that dies must still hand the waiters an answer (false) and clear
	// the slot, so the next caller can retry.
	var v, completed bool
	defer func() {
		headlessAvailMu.Lock()
		// Only a check that RETURNED gets cached: a panicking one has no answer, and writing
		// its zero value would pin "unavailable" for the whole minute. Clearing the slot alone
		// lets the very next caller retry.
		if completed {
			headlessAvailAt[kind], headlessAvail[kind] = time.Now(), v
		}
		delete(headlessAvailInFlight, kind)
		headlessAvailMu.Unlock()
		close(done) // wake every caller that joined the wait channel above
	}()
	v = check(kind)
	completed = true
	return v
}

// SetHeadlessAvailableForTest pins headlessAgentAvailable's 1-minute cache for kind to
// available, without touching real credentials or shelling out to a CLI, and returns a restore
// function. Exported (not a `_test.go` helper) because callers outside chatx need it too — a
// pin-path test in package main (session_translate_test.go) cannot otherwise make
// headlessAgentAvailable("claude") true inside a test binary where nothing is authenticated,
// which is exactly the gap 103-impl-review 中7 found: without it, `preferredFrom` falls
// through every kind to `order[0]` regardless of any pin, and a test that believes it is
// exercising the pin path is actually exercising the fallback (chat_resolve_test.go's own
// forceHeadlessAvailable is the same trick, kept unexported there because chatx's own tests are
// in-package).
func SetHeadlessAvailableForTest(kind string, available bool) (restore func()) {
	headlessAvailMu.Lock()
	prevAt, hadAt := headlessAvailAt[kind]
	prevV, hadV := headlessAvail[kind]
	headlessAvailAt[kind], headlessAvail[kind] = time.Now(), available
	headlessAvailMu.Unlock()
	return func() {
		headlessAvailMu.Lock()
		defer headlessAvailMu.Unlock()
		if hadAt {
			headlessAvailAt[kind] = prevAt
		} else {
			delete(headlessAvailAt, kind)
		}
		if hadV {
			headlessAvail[kind] = prevV
		} else {
			delete(headlessAvail, kind)
		}
	}
}

// ClearHeadlessAvailableForTest deletes kind's cache entry outright — unlike
// SetHeadlessAvailableForTest, which pins a KNOWN value, this makes the kind genuinely
// UNKNOWN again, so the next headlessAgentAvailable/oneShotKindCached call treats it as cold.
// Needed for testing the warming side of 103-impl-review 重大3 (chatx.WarmOneShotKind): pinning
// a kind's cache to a known false with SetHeadlessAvailableForTest still counts as "known" for
// a full minute, so a warm call would just read that back rather than actually running the
// exec being tested.
func ClearHeadlessAvailableForTest(kind string) (restore func()) {
	headlessAvailMu.Lock()
	prevAt, hadAt := headlessAvailAt[kind]
	prevV, hadV := headlessAvail[kind]
	delete(headlessAvailAt, kind)
	delete(headlessAvail, kind)
	headlessAvailMu.Unlock()
	return func() {
		headlessAvailMu.Lock()
		defer headlessAvailMu.Unlock()
		if hadAt {
			headlessAvailAt[kind] = prevAt
		} else {
			delete(headlessAvailAt, kind)
		}
		if hadV {
			headlessAvail[kind] = prevV
		} else {
			delete(headlessAvail, kind)
		}
	}
}

// SetHeadlessAvailCheckForTest replaces headlessAvailCheck — the ONLY point that ever starts a
// real CLI for an availability check — with fn, and returns a restore function. Exported so a
// test that needs a cold entry to actually resolve (proving WarmOneShotKind's warming, not just
// that it fires) never has to depend on a real CLI's presence, auth state or exec latency: see
// headlessAvailInFlight's doc comment for the incident that makes "never touch the real CLI in
// a test that can loop" a hard rule here, not a style preference.
// Both the swap and the restore take headlessAvailMu, because the reader takes it too: the
// straggler warming goroutines are what made this an actual data race rather than a
// theoretical one.
func SetHeadlessAvailCheckForTest(fn func(kind string) bool) (restore func()) {
	headlessAvailMu.Lock()
	prev := headlessAvailCheck
	headlessAvailCheck = fn
	headlessAvailMu.Unlock()
	return func() {
		headlessAvailMu.Lock()
		headlessAvailCheck = prev
		headlessAvailMu.Unlock()
	}
}

// DefaultHeadlessOrder is the built-in auto-selection order for assistant-chat
// backends. agy sits last on purpose: its Starter/free quota is tiny (docs/log/32
// Track D), so out of the box it is only reached in an agy-only workspace. The
// user can rank the backends themselves in Settings > Agents (ui-prefs
// assistantAgentOrder — assistantAgentOrderPref normalizes against this list).
//
// lcpp is deliberately absent (ADR 0093 phase 1): it is reachable only when a
// conversation explicitly pins Agent==lcppKind (ChatProviderFor's own c.Agent lookup
// already handles that with no change needed here) — auto-selecting into a self-hosted
// engine nobody asked for is a phase 2 product decision, not something phase 1's P0
// provider should default a workspace into.
var DefaultHeadlessOrder = []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCursor, session.KindAgy}

// preferredFrom picks the first AUTHENTICATED backend in a priority order. When
// nothing is connected it returns the order's top choice, so the resulting error
// points at the CLI the user cares about most.
func preferredFrom(order []string) string {
	for _, k := range order {
		if headlessAgentAvailable(k) {
			return k
		}
	}
	return order[0]
}

// PreferredHeadlessAgent picks the backend for new builtin-assistant CONVERSATIONS
// (Settings > Assistant, "agent priority").
func PreferredHeadlessAgent() string { return preferredFrom(assistantAgentOrderPref()) }

// PreferredAssistAgent picks the backend for the AI-assisted generation one-shots
// (Settings > AI assist, "agent priority"). Ranked separately from the chat on purpose:
// the chat wants the strongest CLI, these run constantly and want the cheapest that
// works (docs/log/84).
func PreferredAssistAgent() string { return preferredFrom(oneShotOrder()) }

// oneShotKinds are exactly the kinds OneShotHeadlessRun has a case for. A kind outside this list
// is never selected for a one-shot — neither by a feature pin nor from the priority order — even
// when ChatProviders knows it: the run used to fall through to the claude path for any kind
// without a case, so a Muse pick ran and was billed on claude (#1020). lcpp is left out on
// purpose (ADR 0093: auto-selecting into a self-hosted engine is a phase 2 decision).
// TestOneShotKindsMatchTheRunnerSwitch keeps this list and the switch in step.
var oneShotKinds = []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCursor, session.KindAgy, session.KindMuse}

func oneShotRunnable(kind string) bool { return slices.Contains(oneShotKinds, kind) }

// oneShotOrder is the AI assist priority order minus the kinds no one-shot can run on. Never
// empty: agentOrderPref appends every DefaultHeadlessOrder kind, and all of them are runnable.
func oneShotOrder() []string {
	return slices.DeleteFunc(slices.Clone(aiAssistOrderPref()), func(k string) bool { return !oneShotRunnable(k) })
}

// ChatProviderFor resolves the provider driving this conversation: the pinned agent
// while its CLI is authenticated, else the preferred available backend — so a
// claude-less (codex-only / opencode-only) workspace still gets working assistants.
// Each provider keeps its own resume handle on the conversation. The canonical
// message cursor synchronizes turns handled by another backend before that handle is
// resumed (chat_provider_context.go).
func ChatProviderFor(c *ChatConversation) ChatProvider {
	if prov, ok := ChatProviders[c.Agent]; ok && headlessAgentAvailable(c.Agent) {
		return prov
	}
	if prov, ok := ChatProviders[PreferredHeadlessAgent()]; ok {
		return prov
	}
	return ChatProviders[session.KindClaude]
}

// ChatProviderKind returns the concrete backend selected by ChatProviderFor. Keeping
// this out of ChatProvider avoids widening every test stub merely for presentation
// metadata. Production providers are the value types below.
func ChatProviderKind(c *ChatConversation, prov ChatProvider) string {
	switch prov.(type) {
	case ClaudeChat:
		return session.KindClaude
	case codexChat:
		return session.KindCodex
	case opencodeChat:
		return session.KindOpencode
	case agyChat:
		return session.KindAgy
	case cursorChat:
		return session.KindCursor
	case lcppChat:
		return lcppKind
	case museChat:
		return session.KindMuse
	default:
		return c.Agent // test/custom provider: best truthful fallback available
	}
}

// ClaudeChat runs `claude -p` (headless), pinning a session id on the first turn
// and resuming it thereafter so context carries across turns. Auth is the
// container's existing CLAUDE_CODE_OAUTH_TOKEN / CLAUDE_CONFIG_DIR (subscription).
type ClaudeChat struct{}

type claudeResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	// Usage/ModelUsage feed the conversation's context-fill snapshot (chat_usage.go):
	// usage.iterations' last entry is the final per-call snapshot, and modelUsage
	// carries the model's real contextWindow.
	Usage      ClaudeUsage                 `json:"usage"`
	ModelUsage map[string]ClaudeModelUsage `json:"modelUsage"`
	// TotalCostUSD is claude's OWN measured cost for this call — the one backend that
	// doesn't need a price table (docs/log/46 §0). Recorded as the ledger's cost_usd.
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func (ClaudeChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	// Usage ledger (ADR 0029 §3): the record is deferred at the start of the call, so
	// exactly one row is left whichever path is taken — success, an error result, a failed
	// exec or a parse failure.
	call := usagex.Call{Kind: session.KindClaude, ModelReq: chatModelFor(c, session.KindClaude)}
	defer usagex.RecordCall(ctx, &call, time.Now())
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModelFor(c, session.KindClaude))
	args = append(args, chatToolLimits()...) // no subagents (OOM) / no file+shell tools
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = RandUUID()
		args = append(args, "--session-id", c.ClaudeSessionID)
	}
	args = append(args, c.knowledgeArgs()...)
	args = append(args, c.mcpConfigArgs()...)
	cmd := chatClaudeCmd(ctx, args...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	// On an error result (e.g. context overflow) claude prints its JSON result AND exits
	// non-zero, so cmd.Output() returns an ExitError while `out` still holds the structured
	// message. Parse it first: a bare "exit status 1" would hide "Prompt is too long …" and
	// defeat the overflow self-heal (chat_recover.go). Fall back to the exec error only when
	// there's no parseable result.
	var r claudeResult
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		if err != nil {
			return "", fmt.Errorf("claude execution failed: %s", cliErr(err))
		}
		return "", fmt.Errorf("failed to parse claude response: %v", jerr)
	}
	// A failed result carries usage too (an overflow error is especially expensive), so take
	// it before deciding OK.
	call.Models, call.CostUSD = UsageModelRows(r.ModelUsage), r.TotalCostUSD
	// Keep the totals even for a result with no modelUsage (an old CLI, an abnormal exit).
	call.FallbackTotals(r.Usage.LedgerTokens(), "")
	if r.SessionID != "" {
		c.ClaudeSessionID = r.SessionID
	}
	if r.IsError {
		return "", fmt.Errorf("claude returned an error: %s", r.Result)
	}
	if err != nil {
		return "", fmt.Errorf("claude execution failed: %s", cliErr(err))
	}
	call.OK = true
	t := claudeCtx{model: chatModelFor(c, session.KindClaude)}
	t.observeResult(r.Usage, r.ModelUsage)
	t.apply(c) // context-fill snapshot (chat_usage.go)
	return strings.TrimRight(r.Result, "\n"), nil
}

// ChatStreamEvent is one incremental event a streamingProvider emits: either a text Delta
// for the current (tentative) answer, or a completed Step (the model finished a working
// message that ended in a tool call). Exactly one field is set per emit.
type ChatStreamEvent struct {
	Delta string    // incremental text of the current answer
	Step  *chatStep // a just-completed working step (narration + tool names)
}

// streamingProvider is the optional token-streaming variant of chatProvider. emit is called
// per incremental event; the returned string is the final answer and the []chatStep are the
// working steps (docs/log/19). A provider that doesn't implement it falls back to send() (one
// emit of the whole result) in handleChatStream, so every agent works through the stream.
type streamingProvider interface {
	SendStream(ctx context.Context, c *ChatConversation, prompt string, emit func(ChatStreamEvent)) (string, []chatStep, error)
}

// streamLine is one JSONL event from `claude --output-format stream-json`. We read the
// incremental text_delta events (with --include-partial-messages) for live display, watch
// content_block_start/message_delta to split working steps (tool_use) from the final answer
// (end_turn), capture the session id for resume, and take `result` as the authoritative
// fallback text. (Verified live against claude-code 2.1.207.)
type streamLine struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	// Message rides "assistant" lines: each carries the API message's model + usage —
	// the per-message context snapshot claudeCtx tracks (chat_usage.go).
	Message struct {
		Model string      `json:"model"`
		Usage ClaudeUsage `json:"usage"`
	} `json:"message"`
	// Usage/ModelUsage/TotalCostUSD ride the final "result" line (same shape as claudeResult).
	Usage        ClaudeUsage                 `json:"usage"`
	ModelUsage   map[string]ClaudeModelUsage `json:"modelUsage"`
	TotalCostUSD float64                     `json:"total_cost_usd"`
	Event        struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"` // "text" | "thinking" | "tool_use"
			Name string `json:"name"` // tool name when Type=="tool_use"
		} `json:"content_block"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"` // on message_delta: "tool_use" | "end_turn" | …
		} `json:"delta"`
	} `json:"event"`
}

func (ClaudeChat) SendStream(ctx context.Context, c *ChatConversation, prompt string, emit func(ChatStreamEvent)) (string, []chatStep, error) {
	c.StartTurn()
	// Usage ledger (ADR 0029 §3) — as in Send, exactly one record on every path.
	call := usagex.Call{Kind: session.KindClaude, ModelReq: chatModelFor(c, session.KindClaude)}
	defer usagex.RecordCall(ctx, &call, time.Now())
	// stream-json requires --verbose with -p; --include-partial-messages adds the
	// per-token text_delta events we forward for live display.
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--dangerously-skip-permissions", "--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModelFor(c, session.KindClaude))
	args = append(args, chatToolLimits()...) // no subagents (OOM) / no file+shell tools
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = RandUUID()
		args = append(args, "--session-id", c.ClaudeSessionID)
	}
	args = append(args, c.knowledgeArgs()...)
	args = append(args, c.mcpConfigArgs()...)
	cmd := chatClaudeCmd(ctx, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("failed to start claude: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("failed to start claude: %v", err)
	}

	// Split the run into working steps and a final answer (docs/log/19). claude emits one
	// assistant message per turn: a message that ends in stop_reason=tool_use was narration
	// before a tool call (a working step); the message that ends in end_turn is the final
	// answer. We accumulate the current message's text_delta into `cur`; on a tool_use turn
	// boundary we flush `cur` (+ the tool names seen) as a step and reset. Whatever remains in
	// `cur` at the end is the final answer.
	var cur strings.Builder // current message's text (tentative answer)
	var curTools []string   // tool names invoked in the current message
	var steps []chatStep    // completed working steps
	var result string       // authoritative final text (fallback / error text)
	var resultErr bool
	ctxTrack := claudeCtx{model: chatModelFor(c, session.KindClaude)} // context-fill tracker (chat_usage.go)
	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var sl streamLine
			if json.Unmarshal(line, &sl) == nil {
				if sl.SessionID != "" {
					c.ClaudeSessionID = sl.SessionID
				}
				switch sl.Type {
				case "stream_event":
					switch sl.Event.Type {
					case "content_block_start":
						if sl.Event.ContentBlock.Type == "tool_use" && sl.Event.ContentBlock.Name != "" {
							curTools = append(curTools, sl.Event.ContentBlock.Name)
						}
					case "content_block_delta":
						if sl.Event.Delta.Type == "text_delta" && sl.Event.Delta.Text != "" {
							cur.WriteString(sl.Event.Delta.Text)
							emit(ChatStreamEvent{Delta: sl.Event.Delta.Text})
						}
					case "message_delta":
						// A message that stops to call a tool is a working step; flush it and
						// reset so the next message accumulates as a fresh (tentative) answer.
						if sl.Event.Delta.StopReason == "tool_use" {
							step := chatStep{Text: strings.TrimSpace(cur.String()), Tools: curTools}
							if step.Text != "" || len(step.Tools) > 0 {
								steps = append(steps, step)
								emit(ChatStreamEvent{Step: &step})
							}
							cur.Reset()
							curTools = nil
						}
					}
				case "assistant":
					ctxTrack.observeAssistant(sl.Message.Model, sl.Message.Usage)
				case "result":
					result = sl.Result
					resultErr = sl.IsError
					ctxTrack.observeResult(sl.Usage, sl.ModelUsage)
					call.Models, call.CostUSD = UsageModelRows(sl.ModelUsage), sl.TotalCostUSD
					call.FallbackTotals(sl.Usage.LedgerTokens(), "")
				}
			}
		}
		if rerr != nil {
			break // EOF or read error — the process is done streaming
		}
	}
	waitErr := cmd.Wait()
	// A stop by the user, or an abnormal exit before the result event, brings no modelUsage.
	// Degrade to the last snapshot seen on an assistant event (partial, since the output side
	// was still in flight). Without it the row would read "0 tokens / measured=none" and the
	// consumption of exactly the stopped runs would vanish from the ledger — heavy turns are
	// the ones that get stopped, so losing them distorts the whole breakdown.
	call.FallbackTotals(ctxTrack.snap.LedgerTokens(), usagex.MeasuredPartial)
	// An error result (e.g. context overflow) rides the stream's `result` event AND makes
	// claude exit non-zero. Prefer the parsed message so the overflow self-heal can see
	// "Prompt is too long …" (chat_recover.go); a bare "exit status 1" from waitErr would
	// mask it. Fall back to the exec error only when no result event carried an error.
	if resultErr {
		return "", nil, fmt.Errorf("claude returned an error: %s", result)
	}
	if waitErr != nil {
		return "", nil, fmt.Errorf("claude execution failed: %s", stderrOr(waitErr, &stderr))
	}
	call.OK = true
	ctxTrack.apply(c) // context-fill snapshot (chat_usage.go)
	// The final answer is what streamed into the last (end_turn) message — this is exactly
	// what the answer bubble displayed live, so the saved/`done` content matches it (no
	// end-of-turn swap). Fall back to `result` only if nothing streamed there.
	final := strings.TrimSpace(cur.String())
	if final == "" {
		final = strings.TrimSpace(result)
	}
	return strings.TrimRight(final, "\n"), steps, nil
}

// stderrOr renders an exec error, preferring captured stderr.
func stderrOr(err error, stderr *bytes.Buffer) string {
	if s := strings.TrimSpace(stderr.String()); s != "" {
		if len(s) > 500 {
			s = s[:500] + "…"
		}
		return s
	}
	return err.Error()
}

// codexChat runs `codex exec --json` (non-interactive), capturing the thread id from
// thread.started on the first turn and resuming it (`codex exec resume <id>`)
// thereafter. codex exec has no system-prompt flag, so the persona/knowledge ride a
// prompt preamble (headlessPrompt). Auth is codex's own ~/.codex/auth.json. Event
// schema (verified live, codex-cli 0.144):
//
//	{"type":"thread.started","thread_id":"…"}
//	{"type":"item.completed","item":{"type":"agent_message","text":"…"}}
//	{"type":"turn.completed","usage":{…}} / {"type":"turn.failed","error":{…}}
type codexChat struct{}

func (codexChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	// Usage ledger (ADR 0029 §3). Measured: codex puts the model on no event at all, so it is
	// requested only when -m was passed and degrades to default_unknown otherwise.
	model := chatModelFor(c, session.KindCodex) // a pin on another kind resolves via this CLI's config
	call := usagex.Call{Kind: session.KindCodex, ModelReq: model}
	defer usagex.RecordCall(ctx, &call, time.Now())
	// The default read-only sandbox is exactly the chat contract (no file writes, no
	// state mutation) — the claude side enforces the same via --disallowedTools. Global
	// exec flags must precede the resume subcommand (verified live: resume rejects
	// --color/-C placed after it).
	// Headless chat has no approval UI. Explicitly decline escalation while keeping
	// shell commands in the read-only sandbox: MCP calls (including af_write) can run,
	// but the model still cannot mutate the workspace through shell/file tools.
	args := codexChatBaseArgs()
	if model != "" {
		args = append(args, "-m", model)
	}
	mcpArgs, mcpEnv := codexMCPArgs(c)
	args = append(args, mcpArgs...)
	if c.CodexSessionID != "" {
		args = append(args, "resume", c.CodexSessionID)
	}
	args = append(args, "-") // read the prompt from stdin (personas can exceed argv comfort)
	cmd := chatCodexCmd(ctx, mcpEnv, args...)
	defer func() { _, _ = chatCodexHome() }() // fold a rotated token back to shared (see chatCodexHome)
	cmd.Stdin = strings.NewReader(headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex execution failed: %s", cliErr(err))
	}
	reply, threadID, execErr, usage := parseCodexExecEvents(out)
	call.Totals = usage.LedgerTokens()
	if threadID != "" {
		c.CodexSessionID = threadID
	}
	if execErr != "" {
		return "", fmt.Errorf("codex returned an error: %s", execErr)
	}
	if reply == "" {
		return "", errors.New("no response from codex")
	}
	call.OK = true
	// codex never names its model, so the only thing recordable is what -m carried (with no
	// -m it runs codex's own default, which is unknown from here, so this stays empty).
	c.NoteTurnModel(model)
	// codex's input_tokens include the cached ones (chat_usage.go): fresh = input - cached.
	setChatContext(c, usage.InputTokens-usage.CachedInputTokens, usage.CachedInputTokens,
		0, 0, chatCtxModelFor(c, session.KindCodex))
	return reply, nil
}

func codexChatBaseArgs() []string {
	return []string{"-a", "never", "-s", "read-only", "exec", "--json", "--skip-git-repo-check", "--color", "never", "-C", chatWorkdir()}
}

// chatCodexHome prepares the chat-only CODEX_HOME — the codex analog of claude's
// chat-only CLAUDE_CONFIG_DIR (docs/log/19 Q3): an isolated sessions/config tree so the
// headless chat's `codex exec` neither pollutes ~/.codex (its threads would appear in
// the interactive `codex resume` picker / history) nor loads the user's config.toml
// (their own MCP servers must not spawn on every chat turn; the chat attaches only
// the af server via -c). ONLY the login is shared: auth.json is symlinked to
// ~/.codex/auth.json with a copy-back reconcile dedicated to Codex
// (codex also rewrites auth.json via tmp+rename on a ChatGPT token refresh, which
// would replace the symlink with a diverging real file). Call again after each exec
// to fold a rotated token back into the shared file.
func chatCodexHome() (string, error) {
	dir := filepath.Join(paths.AgentStateDir(), "chat-codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	reconcileChatCreds(filepath.Join(homeDir(), ".codex", "auth.json"), filepath.Join(dir, "auth.json"))
	return dir, nil
}

// chatCodexCmd builds a codex exec configured for the chat: run in chatWorkdir with
// the chat-only CODEX_HOME (shared login). extraEnv carries the MCP credentials the
// -c overrides refer to by name (codexMCPArgs), so it must be applied even on the
// fallback path where the isolated home can't be prepared — otherwise an attached
// server comes up unauthenticated instead of not at all.
func chatCodexCmd(ctx context.Context, extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = chatWorkdir()
	over := extraEnv
	if home, err := chatCodexHome(); err == nil {
		over = append(append([]string(nil), extraEnv...), "CODEX_HOME="+home)
	}
	if len(over) > 0 {
		cmd.Env = envWith(over...)
	}
	return cmd
}

// codexMCPArgs attaches this conversation's MCP servers to a codex exec via -c config
// overrides (codex's mcp_servers table) — the codex analog of mcpConfigArgs: the local
// Agent Fleet server per the tool grant, plus the attached registry servers (docs/log/48
// §7). convID rides along for write grants (docs/log/30 report_to auto-attach).
//
// The returned env MUST be put on the codex process: codex's remote headers and any
// stdio server's own variables are passed by NAME in argv and read from codex's
// environment, which is what keeps registered credentials out of argv.
func codexMCPArgs(c *ChatConversation) (args, env []string) {
	if sargs, ok := c.afServerArgs(); ok {
		args = append(args,
			"-c", "mcp_servers.af.command="+tomlString(paths.ExePath()),
			"-c", "mcp_servers.af.args="+tomlStringArray(sargs),
			// Codex only forwards explicitly allowlisted variables to stdio MCP children.
			// Without these, the isolated chat process starts the Agent Fleet server but
			// every Agent call is unauthenticated and memo calls lose their CP bridge.
			"-c", `mcp_servers.af.env_vars=["AGENT_TOKEN","AGENT_ADDR","AF_CP_BASE_URL","AF_MEMO_TOKEN","AF_SCHEDULE_TOKEN"]`,
			// Codex has a distinct MCP approval layer. Headless exec has no UI to answer
			// it, so the default policy reports "user cancelled MCP tool call" unless the
			// explicitly granted Agent Fleet server is pre-approved.
			"-c", "mcp_servers.af.default_tools_approval_mode=\"approve\"",
		)
	}
	regArgs, regEnv := mcpreg.CodexOverrides(c.mcpServersFor(session.KindCodex), mcpreg.CodexOpts{Approve: true})
	return append(args, regArgs...), regEnv
}

// tomlStringArray renders a TOML array of basic strings for a codex -c override.
func tomlStringArray(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = tomlString(v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// parseCodexExecEvents walks a codex exec --json JSONL stream: the reply is the
// agent_message items joined (normally one), the thread id comes from thread.started,
// a turn.failed / error event surfaces as execErr, and turn.completed's usage feeds
// the context-fill snapshot (chat_usage.go; last one wins).
func parseCodexExecEvents(out []byte) (reply, threadID, execErr string, usage CodexUsage) {
	var texts []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Type     string     `json:"type"`
			ThreadID string     `json:"thread_id"`
			Message  string     `json:"message"`
			Usage    CodexUsage `json:"usage"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			threadID = ev.ThreadID
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				texts = append(texts, ev.Item.Text)
			}
		case "turn.completed":
			usage = ev.Usage
		case "turn.failed", "error":
			if ev.Error.Message != "" {
				execErr = ev.Error.Message
			} else if ev.Message != "" {
				execErr = ev.Message
			} else {
				execErr = "turn failed"
			}
		}
	}
	return strings.TrimRight(strings.Join(texts, "\n\n"), "\n"), threadID, execErr, usage
}

// tomlString renders s as a TOML basic string for a codex -c override (same as the
// codex launcher's helper; tiny, so duplicated rather than exported).
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// opencodeChat runs `opencode run --format json`, capturing the session id from the
// events on the first turn and resuming it (--session) thereafter. Provider keys from
// the Connections store are injected as env (like the interactive launcher); opencode's
// own logged-in auth works as-is. Persona/knowledge ride the prompt preamble. Event
// schema (verified live, opencode 1.17):
//
//	{"type":"text","sessionID":"ses_…","part":{"id":"prt_…","type":"text","text":"…"}}
//	(step_start / step_finish / tool events interleave; every event carries sessionID)
type opencodeChat struct{}

func (opencodeChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	pinned := chatModelFor(c, session.KindOpencode)                   // a pin on another kind resolves via this CLI's config
	call := usagex.Call{Kind: session.KindOpencode, ModelReq: pinned} // usage ledger (ADR 0029 §3)
	defer usagex.RecordCall(ctx, &call, time.Now())
	dir := opencodeChatDir(c)
	args := []string{"run", "--format", "json", "--dir", dir}
	if c.OpencodeSessionID != "" {
		args = append(args, "--session", c.OpencodeSessionID)
	}
	if pinned != "" {
		args = append(args, "--model", pinned)
	}
	args = append(args, headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	cmd := exec.CommandContext(ctx, "opencode", args...)
	cmd.Dir = dir
	env := opencode.Env()
	if cfg := opencodeChatConfig(c); cfg != "" {
		env = append(env, "OPENCODE_CONFIG="+cfg)
	}
	cmd.Env = envWith(env...)
	out, err := cmd.Output()
	// Parse BEFORE the error branch: a failed run still prints its events, and both the
	// reason (turnErr) and whatever tokens it burned are in there.
	reply, sesID, model, turnErr, usage := parseOpencodeRunEvents(out)
	call.Totals = usage.LedgerTokens()
	if model != "" {
		call.Models = []usagex.ModelRow{{Model: model, ModelRaw: model, Tokens: call.Totals}}
	}
	if err != nil {
		if turnErr != "" {
			return "", fmt.Errorf("opencode turn failed: %s", turnErr)
		}
		return "", fmt.Errorf("opencode execution failed: %s", cliErr(err))
	}
	// Only a turn that answered adopts its session id: a failed run mints a session too,
	// and switching to it would strand the conversation's context on the retry.
	if sesID != "" {
		c.OpencodeSessionID = sesID
	}
	if reply == "" {
		if turnErr != "" {
			return "", fmt.Errorf("opencode turn failed: %s", turnErr)
		}
		return "", errors.New("no response from opencode")
	}
	call.OK = true
	// opencode does put the real model on its events (parseOpencodeRunEvents). On a version
	// where it cannot be read, fall back to what --model carried (empty = not displayed).
	if model != "" {
		c.NoteTurnModel(model)
	} else {
		c.NoteTurnModel(pinned)
	}
	setChatContext(c, usage.Input, usage.Cache.Read, usage.Cache.Write, 0, chatCtxModelFor(c, session.KindOpencode))
	return reply, nil
}

// opencodeChatPolicy is the chat contract every opencode chat run carries: file edits
// and shell denied — the opencode analog of chatToolLimits. It rides BOTH config files
// below because either one can be the only config a run gets: opencodeChatDir falls
// back to the shared workdir (no project config) and opencodeChatConfig returns ""
// without an af grant.
//
// Measured on 1.18.7 (`opencode debug config`, pinned by opencode_contract_test.go): opencode
// MERGES every config source rather than letting one replace the others, and on a key
// collision the nearest project config wins — precedence is
// project(opencode.json) > OPENCODE_CONFIG > global(~/.config/opencode). Carrying the
// same posture in both files is therefore consistent, never contradictory: a chat's
// deny also beats whatever the user's global opencode config says.
func opencodeChatPolicy() map[string]any {
	return map[string]any{"edit": "deny", "bash": "deny"}
}

// opencodeChatDir prepares the working dir for a chat's opencode run, with an
// opencode.json carrying the chat policy and the attached registry servers
// (docs/log/48 §7). The dir is opencode's PROJECT: its session store is scoped to it, so
// the path a conversation runs in is part of its resume identity (measured on 1.18.5:
// `run --session <id>` from another dir hangs outright rather than erroring). That is
// why the grant split (none/read/write) stays even though the file itself no longer
// differs per grant — collapsing it would strand every existing conversation's
// session.
//
// opencode takes this config from the DIR, not per invocation, so conversations that
// resolve to different server sets must not share one — otherwise a concurrent turn's
// rewrite decides which servers this turn gets. The dir key is therefore the grant
// plus a digest of the resolved registry set (docs/log/48 §7 constraints), not the grant
// alone.
//
// The af MCP server is NOT here, and that is load-bearing rather than tidy: it needs
// the conversation id (docs/log/30 report_to), which is per conversation, not per grant.
// opencode merges the config sources and the project config wins the collision (measured on
// 1.18.7), so an af entry here would not merely "resurface" — it would BEAT the
// per-conversation one and strip --conv from every chat, silently killing the session report
// (【セッション報告】) for good. Registry servers are safe to carry here because both files
// derive them from the same conversation, so the copies never disagree. See
// opencodeChatConfig.
//
// Falls back to the shared chat workdir when the dir can't be prepared (opencode then
// runs with its defaults).
func opencodeChatDir(c *ChatConversation) string {
	grant := "none"
	if c.afToolsEnabled() {
		grant = "read"
		if c.AFWriteEnabled() {
			grant = "write"
		}
	}
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": opencodeChatPolicy(),
	}
	servers := mcpreg.OpencodeServers(c.mcpServersFor(session.KindOpencode))
	if len(servers) > 0 {
		cfg["mcp"] = servers
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return chatWorkdir()
	}
	dir := filepath.Join(paths.AgentStateDir(), "chat-wd", "opencode-"+grant+serverSetKey(servers))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return chatWorkdir()
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), append(b, '\n'), 0o600); err != nil {
		return chatWorkdir()
	}
	return dir
}

// opencodeChatConfig writes this conversation's own opencode config and returns its
// path for OPENCODE_CONFIG (honored by opencode 1.18.5 — verified live: a server
// defined only in the env-pointed file shows up connected in `opencode mcp list`).
//
// It exists because opencode's config is per FILE and the af MCP server must carry
// `--conv <id>`: without it mcp-stdio has no conversation to attach report_to to, so
// create_session / send_to_session arm no report and the operator never gets the
// session report (【セッション報告】) it is told to wait for (docs/log/30). claude/codex/agy
// pass --conv in their own per-conversation config; opencode has nowhere else to put it,
// because its only other config is the per-GRANT project dir shared by every conversation.
//
// Pointing OPENCODE_CONFIG at a per-conversation file keeps --dir (the session's
// project identity) untouched, so existing conversations resume normally. Returns ""
// when the conversation holds no af grant, or when the file can't be written — the
// run then falls back to the project config (policy intact, registry servers intact,
// no af tools).
//
// The attached registry servers (docs/log/48 §7) ride here as well as in the project
// config, for the same reason the policy does: opencodeChatDir degrades to the shared
// chat workdir when it can't prepare its dir, and that fallback has no project config
// at all — this file is then the only thing standing between the conversation and a
// turn with none of its servers. Under the measured merge the project copy wins the
// collision, but both are built from the same conversation, so they agree.
func opencodeChatConfig(c *ChatConversation) string {
	if !c.afToolsEnabled() || !paths.ValidIDSegment(c.ID) {
		return ""
	}
	dir := filepath.Join(paths.AgentStateDir(), "chat-wd", "opencode-conv")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("opencode chat config: %v (af tools unavailable this turn)", err)
		return ""
	}
	mcpArgs := []string{paths.ExePath(), "mcp-stdio"}
	if c.AFWriteEnabled() {
		// --conv rides the write grant only: it exists for report_to / origin_conv,
		// which are write-side plumbing (mcp_stdio.go).
		mcpArgs = append(mcpArgs, "--write", "--conv", c.ID)
	}
	servers := mcpreg.OpencodeServers(c.mcpServersFor(session.KindOpencode))
	servers["af"] = map[string]any{"type": "local", "command": mcpArgs, "enabled": true}
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": opencodeChatPolicy(),
		"mcp":        servers,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, c.ID+".json")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		log.Printf("opencode chat config: %v (af tools unavailable this turn)", err)
		return ""
	}
	return p
}

// OpencodeOneShotConfig writes the static read-only policy config for opencode
// one-shots (title / branch / reply / edit-suggestion — oneShotHeadless) and returns
// its path for OPENCODE_CONFIG. One-shots run in the bare chatWorkdir with no
// opencode.json, so without this file the run inherited the user's global config —
// the only backend of OneShotHeadless whose tool posture wasn't pinned read-only
// (claude --tools "" / codex no tool grant / cursor --mode ask). This is where the
// read-only suggestion-generation channel of docs/log/44 §1.3 is closed. Returns ""
// when the file can't be written (the caller then degrades to today's behaviour
// rather than breaking titles on a broken home).
func OpencodeOneShotConfig() string {
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": opencodeChatPolicy(),
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return ""
	}
	dir := chatWorkdir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	p := filepath.Join(dir, "opencode-oneshot.json")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		log.Printf("opencode one-shot config: %v (running without deny policy)", err)
		return ""
	}
	return p
}

// serverSetKey is the dir-key suffix distinguishing two opencode chat configs that
// share a tool grant. It digests the REGISTRY servers only (af is already implied by
// the grant), so an assistant with none keeps the long-standing
// opencode-none/read/write dir instead of migrating to a hashed one. The digest
// covers the serialized entries, not just the names, so editing a server's URL or
// credential lands in a fresh dir rather than racing a concurrent turn's rewrite.
func serverSetKey(servers map[string]any) string {
	names := make([]string, 0, len(servers))
	for n := range servers {
		if n != "af" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	h := fnv.New32a()
	for _, n := range names {
		b, _ := json.Marshal(servers[n])
		_, _ = h.Write([]byte(n))
		_, _ = h.Write(b)
	}
	return fmt.Sprintf("-%08x", h.Sum32())
}

// parseOpencodeRunEvents walks an opencode run --format json stream: the reply is the
// text parts in arrival order (deduped by part id — opencode may re-emit a part as it
// settles), every event carries the session id for resume, and step_finish parts carry
// the per-call tokens that feed the context-fill snapshot (chat_usage.go; last wins).
//
// model is best-effort for the usage ledger (ADR 0029 §4): opencode's store records a
// modelID on the message, and the run stream is expected to carry it too, but this
// workspace has no opencode login to verify against. We read it wherever it plausibly
// rides and degrade to "" (→ requested / default_unknown) rather than guessing a schema.
//
// turnErr is the reason a run produced no answer. opencode reports a provider failure
// as an EVENT on stdout and exits non-zero with an empty stderr (measured on 1.18.5):
//
//	{"type":"error","sessionID":"ses_…","error":{"name":"UnknownError",
//	 "data":{"message":"Unexpected server error. Check server logs for details.",
//	         "ref":"err_26a07104"}}}
//
// Without reading it the caller can only say "exit status 1" / "no response from
// opencode", which is what made a silently failed assistant turn undiagnosable.
func parseOpencodeRunEvents(out []byte) (reply, sesID, model, turnErr string, usage opencodeUsage) {
	texts := map[string]string{} // part id -> latest text
	var order []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			ModelID   string `json:"modelID"`
			Part      struct {
				ID      string        `json:"id"`
				Type    string        `json:"type"`
				Text    string        `json:"text"`
				ModelID string        `json:"modelID"`
				Tokens  opencodeUsage `json:"tokens"`
			} `json:"part"`
			Message struct {
				ModelID string `json:"modelID"`
			} `json:"message"`
			Error struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
					Ref     string `json:"ref"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			sesID = ev.SessionID
		}
		if ev.Type == "error" {
			if msg := opencodeErrText(ev.Error.Name, ev.Error.Data.Message, ev.Error.Data.Ref); msg != "" {
				turnErr = msg // the last error wins (later ones tend to be more specific)
			}
		}
		for _, m := range []string{ev.ModelID, ev.Part.ModelID, ev.Message.ModelID} {
			if m != "" {
				model = m // the last one seen wins (the model is not expected to change inside one run)
			}
		}
		if ev.Type == "text" && ev.Part.Type == "text" && strings.TrimSpace(ev.Part.Text) != "" {
			if _, seen := texts[ev.Part.ID]; !seen {
				order = append(order, ev.Part.ID)
			}
			texts[ev.Part.ID] = ev.Part.Text
		}
		if ev.Type == "step_finish" && ev.Part.Type == "step-finish" &&
			ev.Part.Tokens.Input+ev.Part.Tokens.Cache.Read+ev.Part.Tokens.Cache.Write > 0 {
			usage = ev.Part.Tokens
		}
	}
	var parts []string
	for _, id := range order {
		parts = append(parts, texts[id])
	}
	return strings.TrimRight(strings.Join(parts, "\n\n"), "\n"), sesID, model, turnErr, usage
}

// opencodeErrText renders an error event as one line for the user. The ref is kept:
// it is the only handle opencode support has on a server-side failure.
func opencodeErrText(name, msg, ref string) string {
	out := strings.TrimSpace(msg)
	if out == "" {
		out = strings.TrimSpace(name)
	}
	if out == "" {
		return ""
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		out += " (ref: " + ref + ")"
	}
	return out
}

// agyChat runs `agy -p` (print mode, plain-text stdout — v1.1.4 has no structured
// output), resuming via `--conversation <UUID>`. The UUID is captured from agy's
// cwd→last-conversation map (cache/last_conversations.json), which a `-p` run
// writes on process exit (docs/log/32 Track D-3 — unlike the TUI, which flushes it
// only on graceful exit). agy has no system-prompt flag, so persona/knowledge ride
// the headlessPrompt preamble.
//
// Tools: agy's MCP config is GLOBAL-only (~/.gemini/config/mcp_config.json — no
// per-invocation flag like claude --mcp-config / codex -c), so every turn runs
// under a per-conversation isolated HOME (chatAgyHome) that shares ONLY the OAuth
// token with the user's real ~/.gemini. `-p` auto-denies tool prompts (docs/log/32
// D-5); the isolated home's permissions.allow re-opens exactly the chat contract:
// the knowledge dirs, URL reads, and `mcp(<server>/*)` for each granted server (rule
// syntax reverse-engineered from the binary and live-verified 2026-07-20), and
// permissions.deny hard-denies commands and writes (agyChatDenyRules —
// on agy ≥1.2 a soft-deny kills the whole turn). No --dangerously-skip-permissions.
// No usage events, so the context gauge stays empty (Context = nil).
type agyChat struct{}

func (agyChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	// Usage ledger (ADR 0029 §3): agy returns plain text only, so measured=none — the tokens
	// are "not measured" rather than 0, and only the call count is recorded.
	call := usagex.Call{Kind: session.KindAgy, ModelReq: chatModelFor(c, session.KindAgy), Measured: usagex.MeasuredNone}
	defer usagex.RecordCall(ctx, &call, time.Now())
	home, wd, err := chatAgyHome(c)
	if err != nil {
		// Never fall back to the real HOME: the MCP/permission config is global
		// there and would leak into the user's interactive agy sessions.
		return "", fmt.Errorf("agy chat home: %v", err)
	}
	args, model := agyChatArgs(c, headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	cmd := exec.CommandContext(ctx, "agy", args...)
	cmd.Dir = wd
	cmd.Env = agy.Env(envWith("HOME=" + home))
	// agy may refresh the OAuth token via tmp+rename, replacing the symlink with a
	// diverging real file — fold a rotated token back to the shared one (as for codex).
	defer reconcileChatCreds(agy.TokenPath(), filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("agy execution failed: %s", agyStderrOr(stderr.Bytes(), err.Error()))
	}
	if c.AgyConversationID == "" {
		// First turn: adopt the conversation this run just recorded for its private
		// cwd inside the private home — doubly unambiguous.
		c.AgyConversationID = agy.LastConversationIn(
			filepath.Join(home, ".gemini", "antigravity-cli"), wd)
	}
	reply := strings.TrimRight(strings.TrimSpace(string(out)), "\n")
	if reply == "" {
		// agy ≥1.2 ends a -p turn with exit 0 and an empty stdout when a tool needed a
		// permission print mode cannot prompt for, and says which one only on stderr
		// ("jetski: no output produced — a tool required the "read_file" permission …").
		return "", errors.New(agyStderrOr(stderr.Bytes(), "no response from agy"))
	}
	call.OK = true
	c.NoteTurnModel(model) // only what --model carried (agy names no model)
	return reply, nil
}

// agyStderrOr is agy's stderr as one bounded message, or fallback when it printed
// nothing. cmd.Stderr is set, so exec.ExitError carries no stderr for cliErr to read.
func agyStderrOr(stderr []byte, fallback string) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return fallback
	}
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// agyChatArgs builds the argv for one agy chat turn: flags first, the prompt as
// `-p`'s value last (verified live v1.1.4 — `-p <prompt>` with the display-name
// --model and --conversation resume all honored). The second return is the model
// actually passed ("" when the pin was dropped or none was set, i.e. agy runs on its
// own default) — agy prints plain text and names no model, so this is the only
// truthful thing to record for the turn.
func agyChatArgs(c *ChatConversation, prompt string) ([]string, string) {
	var args []string
	var model string
	if pinned := chatModelFor(c, session.KindAgy); pinned != "" { // fetch the catalog only when there is a pin to validate
		if m := agyChatModel(pinned, agy.Models()); m != "" {
			args = append(args, "--model", m)
			model = m
		}
	}
	if c.AgyConversationID != "" {
		args = append(args, "--conversation", c.AgyConversationID)
	}
	return append(args, "-p", prompt), model
}

// agyChatModel returns the --model value for a turn, self-healing a stale pin:
// the pinned display name is passed through only while the live catalog (or its
// stale-if-error cache) still lists it — a renamed/withdrawn model degrades to
// agy's own default instead of failing every send. An empty catalog (CLI absent,
// signed out) can't validate, so the pin passes through untouched.
func agyChatModel(model string, catalog []agents.ModelChoice) string {
	if model == "" || len(catalog) == 0 {
		return model
	}
	for _, mc := range catalog {
		if mc.ID == model {
			return model
		}
	}
	return ""
}

// chatAgyHome prepares the per-CONVERSATION isolated HOME + working dir for a
// chat's agy runs, under ~/.config/agent-fleet/chat-wd/agy-<convID>/ (removed
// with the thread by handleChatDelete):
//   - home/.gemini/antigravity-cli/antigravity-oauth-token — symlink to the real
//     token (login is the ONLY shared state; agy resolves config from $HOME).
//   - settings.json / config/config.json — workspace trust for wd, telemetry off,
//     and permissions allow/deny (both files carry them: the effective location has
//     shifted between builds, docs/log/32 D-5, and an extra copy is harmless).
//   - config/mcp_config.json — the granted MCP servers. agy's spawned MCP servers
//     inherit its env, so each entry pins env.HOME back to the REAL home (the af
//     server must read the user's actual session state — live-verified).
//
// Per-conversation (not per-grant like opencode) because a write grant's --conv
// must ride mcp_config.json args — there is no per-invocation override to carry
// the conversation id. The dir doubles as the cwd→UUID capture scope.
func chatAgyHome(c *ChatConversation) (home, wd string, err error) {
	base := filepath.Join(paths.AgentStateDir(), "chat-wd", "agy-"+c.ID)
	home = filepath.Join(base, "home")
	wd = filepath.Join(base, "wd")
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	cfgDir := filepath.Join(home, ".gemini", "config")
	for _, d := range []string{wd, cliDir, cfgDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", "", err
		}
	}
	reconcileChatCreds(agy.TokenPath(), filepath.Join(cliDir, "antigravity-oauth-token"))
	perms := map[string]any{"allow": agyChatAllowRules(c), "deny": agyChatDenyRules}
	settings := map[string]any{
		"enableTelemetry":   false,
		"trustedWorkspaces": []string{wd},
		"permissions":       perms,
	}
	files := map[string]map[string]any{
		filepath.Join(cliDir, "settings.json"):   settings,
		filepath.Join(cfgDir, "config.json"):     {"permissions": perms},
		filepath.Join(cfgDir, "mcp_config.json"): {"mcpServers": agyChatServers(c)},
	}
	for p, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "", "", err
		}
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			return "", "", err
		}
	}
	return home, wd, nil
}

// agyChatAllowRules is the permissions.allow set for a chat's agy: `read_file(<dir>)`
// per knowledge dir, `read_url(*)` (parity with claude's chat, which keeps WebFetch —
// chatToolLimits), and `mcp(<server>/*)` per granted server. Rule syntax verified
// live (mcp(af) and bare tool names do NOT match — agy drops them from settings.json;
// docs/log/32 §headlessChat). Anything else a turn reaches for is either hard-denied
// (agyChatDenyRules) or, outside the knowledge dirs, soft-denied by print mode.
func agyChatAllowRules(c *ChatConversation) []string {
	var allow []string
	for _, d := range c.knowledgeDirs() {
		allow = append(allow, "read_file("+d+")")
	}
	allow = append(allow, "read_url(*)")
	n := len(allow)
	for name := range agyChatServers(c) {
		allow = append(allow, "mcp("+name+"/*)")
	}
	sort.Strings(allow[n:]) // deterministic file content across turns
	return allow
}

// agyChatDenyRules hard-denies what the chat contract never grants. They are not
// redundant with print mode's own refusal: agy ≥1.2 (measured on 1.2.9) answers a
// permission it cannot prompt for by ENDING the turn — exit 0, empty stdout, the
// reason only on stderr — so one reach for a shell command (the model does this
// unprompted, e.g. to look around before calling an MCP tool) lost the whole reply
// as "no response from agy". A deny rule is reported back to the model instead,
// and it answers in text. write_file must be listed too: it is not print-mode gated
// at all, so with commands denied the model falls back to it and does write files
// (measured: /tmp and $HOME). Targets: command takes `*`, write_file a directory
// prefix.
var agyChatDenyRules = []string{"command(*)", "write_file(/)"}

// agyChatExe resolves the binary serving mcp-stdio/mcp-run in agy's mcp_config —
// indirected so the live test can point it at the installed workspace-agent
// (under `go test`, paths.ExePath() is the test binary, which serves neither).
var agyChatExe = paths.ExePath

// agyChatServers builds the mcp_config.json server map for this conversation —
// the agy analog of claude's mcpConfigArgs: the local af server per the tool
// grant, plus the attached registry servers (docs/log/48 §7). env.HOME pins the spawned
// servers back to the real home (they inherit agy's isolated-HOME env otherwise);
// a builtin's mcp-run wrapper in particular resolves the encrypted store from HOME.
//
// agy is the one provider whose command is NOT this binary: the chat runs from an
// isolated HOME, so the exe is resolved separately (agyChatExe). Registry-registered
// servers carry their own absolute command and are unaffected — only the builtins,
// which the registry defines as `<this binary> mcp-run <id>`, need rewriting.
func agyChatServers(c *ChatConversation) map[string]any {
	exe := agyChatExe()
	env := map[string]any{"HOME": homeDir()}
	defs := c.mcpServersFor(session.KindAgy)
	for i, d := range defs {
		if runArgs, ok := mcpreg.BuiltinRunArgs(d.ID); ok {
			defs[i].Command, defs[i].Args = exe, runArgs
		}
	}
	servers := mcpreg.AgyServers(defs, map[string]string{"HOME": homeDir()})
	if sargs, ok := c.afServerArgs(); ok {
		servers["af"] = map[string]any{"command": exe, "args": anyArgs(sargs), "env": env}
	}
	return servers
}

// cursorChat runs `cursor-agent -p --output-format json` (headless print mode). The
// chat UUID is minted on the first turn — cursor's `--resume <uuid>` CREATES a chat
// under a self-minted valid v4 and resumes it thereafter (measured, docs/log/40 §-p / probe 8)
// — so context carries across turns via the same self-UUID identity the TUI/managed
// routes use (cursor.go). Auth is ambient (~/.config/cursor/auth.json), so no token
// injection. cursor has no system-prompt flag, so persona/knowledge ride the
// headlessPrompt preamble.
//
// Tool posture: --mode ask ("Q&A style … read-only", cursor's own execution mode —
// measured on v2026.07.20). This is BOTH the chat contract (no host mutation: the model can
// read but cannot edit files or run write shell commands) AND the assistant's actual
// use (conversational Q&A / translation). We do NOT pass --force. Live-verified: a
// bare `-p` (even without --force) AUTO-EXECUTES write tools in headless mode — it
// does NOT fail closed on the missing approval UI, so relying on "no approver" would
// silently mutate the shared host; --mode ask is what structurally prevents it (a
// file-creation prompt returns "I'm in Ask mode, so I can't create files …" and
// writes nothing). It is the cursor analog of claude --disallowedTools /
// opencode edit,bash:deny. --trust only skips the workspace-trust prompt. The af MCP
// tools are not wired for cursor v1: cursor's MCP config is global (~/.cursor), so
// per-conversation grants would need the isolated-HOME dance agy uses (docs/log/40 Track D).
//
// The terminal `result.usage` is the ONLY cursor route carrying tokens (docs/log/40 §usage
// — ACP/JSONL have none), so it feeds the context-fill snapshot. Fields are additive
// (fresh input + cache read + cache write), same shape as opencode.
type cursorChat struct{}

// cursorResult is `cursor-agent -p --output-format json`'s single result object
// (measured, docs/log/40 probe 9): {"type":"result","subtype":"success","is_error":…,
// "result":"…","session_id":"…","usage":{inputTokens,outputTokens,cacheReadTokens,
// cacheWriteTokens}}. --output-format stream-json would be NDJSON; we use the plain
// json form, so the whole stdout is this one object.
type cursorResult struct {
	Type      string `json:"type"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Usage     struct {
		InputTokens      int `json:"inputTokens"`
		OutputTokens     int `json:"outputTokens"`
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
	} `json:"usage"`
}

func (cursorChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	// Usage ledger (ADR 0029 §3). Measured: cursor puts no model on its result, so this stops
	// at requested. "auto" passes no --model, so the resolved model is unknown = default_unknown.
	call := usagex.Call{Kind: session.KindCursor}
	pinned := chatModelFor(c, session.KindCursor) // a pin on another kind resolves via this CLI's config
	if pinned != "" && pinned != "auto" {
		call.ModelReq = pinned
	}
	defer usagex.RecordCall(ctx, &call, time.Now())
	args := cursorChatBaseArgs()
	if m := pinned; m != "" && m != "auto" { // "auto" = cursor's default = no --model
		args = append(args, "--model", m)
	}
	if c.CursorSessionID == "" {
		c.CursorSessionID = RandUUID() // --resume with a fresh valid v4 creates the chat
	}
	args = append(args, "--resume", c.CursorSessionID)
	cmd := exec.CommandContext(ctx, cursor.Bin(), args...)
	cmd.Dir = chatWorkdir()
	cmd.Env = cursor.EnvWithoutCI(os.Environ()) // do not pass CI through (cursor/ci_env.go)
	cmd.Stdin = strings.NewReader(headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cursor execution failed: %s", cliErr(err))
	}
	r, perr := parseCursorResult(out)
	if perr != nil {
		return "", perr
	}
	call.SetTotals(r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
	if r.SessionID != "" {
		c.CursorSessionID = r.SessionID // adopt the id cursor echoes back (self-heals a drift)
	}
	if r.IsError {
		return "", fmt.Errorf("cursor returned an error: %s", strings.TrimSpace(r.Result))
	}
	reply := strings.TrimRight(strings.TrimSpace(r.Result), "\n")
	if reply == "" {
		return "", errors.New("no response from cursor")
	}
	call.OK = true
	// The same rule as the ledger's ModelReq: record only what --model carried, and leave
	// auto/unset empty (the model cursor resolved is not in the result, so never guess it).
	c.NoteTurnModel(call.ModelReq)
	setChatContext(c, r.Usage.InputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens, 0,
		chatCtxModelFor(c, session.KindCursor))
	return reply, nil
}

// cursorChatBaseArgs is the shared argv prefix for a cursor headless turn:
// --disable-auto-update (fleet pins the version via image rebuild) precedes the
// subcommand as a root option (measured: it is rejected after -p), then print-mode JSON,
// --trust (skip the workspace-trust prompt), and --mode ask (read-only Q&A — the
// tool posture that keeps a chat turn from mutating the shared host; see cursorChat).
func cursorChatBaseArgs() []string {
	return []string{"--disable-auto-update", "-p", "--output-format", "json", "--trust", "--mode", "ask"}
}

// parseCursorResult decodes the -p result object. --output-format json emits one
// object, but cursor can exit without well-formed JSON on a failure (docs/log/40) and may
// prefix stray lines, so fall back to scanning for the last line that parses as a result.
func parseCursorResult(out []byte) (cursorResult, error) {
	var r cursorResult
	if json.Unmarshal(bytes.TrimSpace(out), &r) == nil && r.Type == "result" {
		return r, nil
	}
	var found bool
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var cand cursorResult
		if json.Unmarshal(ln, &cand) == nil && cand.Type == "result" {
			r, found = cand, true
		}
	}
	if !found {
		return cursorResult{}, errors.New("failed to parse cursor response")
	}
	return r, nil
}

// headlessPrompt builds the stdin/argv prompt for backends without a system-prompt
// flag (codex exec, opencode run): the persona as a tagged instruction block, the
// knowledge dirs as a pointer line, then the user's prompt.
func headlessPrompt(persona string, knowledge []string, prompt string) string {
	var b strings.Builder
	if strings.TrimSpace(persona) != "" {
		b.WriteString("<system_instructions>\n")
		b.WriteString(persona)
		b.WriteString("\n</system_instructions>\n\n")
	}
	if len(knowledge) > 0 {
		b.WriteString("参考資料ディレクトリ（必要に応じて読み取りツールで参照）: ")
		b.WriteString(strings.Join(knowledge, ", "))
		b.WriteString("\n\n")
	}
	b.WriteString(prompt)
	return b.String()
}

// claudeOneShotArgs is the argv for a claude one-shot (title / branch name / reply
// candidate). Three deliberate departures from a chat turn (docs/log/46 §1-a-2, measured
// 2026-07-25) — a one-shot is a pure classification-and-format task, so everything that
// makes claude a coding agent is dead weight paid on every call:
//
//	--system-prompt … REPLACES the default system prompt instead of appending to it
//	                  (--append-system-prompt left the whole Claude Code persona on).
//	--tools ""      … loads no built-in tool definitions at all (they were still being
//	                  sent even though the useful ones were disallowed).
//
// plus MAX_THINKING_TOKENS=0 (claudeOneShotEnv) — an 18-character title was costing
// ~500 thinking tokens. Measured on the title prompt: input 16.0k→4.3k / out 533→13 /
// 6.2s→1.2s, same answer quality. With no tools there is nothing to permit, so
// --dangerously-skip-permissions is gone too. --no-session-persistence is claude's
// --ephemeral analog (print-mode only, no transcript written, no resume): a one-shot
// never resumes, so don't pile per-call jsonl into Claude's projects tree.
//
// --tools MUST stay last: it is variadic, so a following positional would be eaten.
func claudeOneShotArgs(persona, model string) []string {
	args := []string{"-p", "--no-session-persistence", "--output-format", "json",
		"--system-prompt", persona}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "--tools", "")
}

// claudeOneShotEnv is applied on top of chatClaudeCmd's env for one-shots only — a chat
// turn still reasons, so the thinking budget is cut here and nowhere else.
var claudeOneShotEnv = []string{"MAX_THINKING_TOKENS=0"}

// cheapOneShotMarkers are the size markers vendors put in model ids for their small
// models. Matching on the marker rather than on a hardcoded id is what keeps this
// working across catalog drift: model names churn every few weeks, "mini"/"flash"/
// "lite" do not.
var cheapOneShotMarkers = []string{"mini", "flash", "lite", "small", "nano", "haiku"}

// cheapOneShotModel picks the cheapest-looking entry of a live model catalog for a
// one-shot (title / branch name / reply candidate) — docs/log/46 §2-b. claude and agy pin a
// cheap default explicitly (haiku / Flash); codex and opencode used to pass no model at
// all unless AF_TITLE_MODEL_* was set, which silently ran these throwaway calls on the
// CLI's own default — on a real workspace that was gpt-5.6-luna at "high" effort.
//
// Returns "" when nothing in the catalog looks small (or the catalog is unavailable,
// e.g. the CLI is not logged in): the caller then passes no model flag and behaves
// exactly as before. Never guessing a name that might not exist is the point — a wrong
// -m is a hard failure, while falling back only costs what we already spend today.
func cheapOneShotModel(ids []string) string {
	for _, id := range ids {
		low := strings.ToLower(id)
		for _, marker := range cheapOneShotMarkers {
			if strings.Contains(low, marker) {
				return id
			}
		}
	}
	return ""
}

// modelChoiceIDs adapts a rich catalog (codex) to the id list cheapOneShotModel wants;
// opencode's catalog is already []string.
func modelChoiceIDs(list []agents.ModelChoice) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out
}

// OneShotTier is what a one-shot call actually NEEDS, which is not the same as where
// its setting lives (docs/log/84):
//
//	OneShotShort … a short label: titles, branch names, reply chips. A cheap fast
//	               model is enough, and these fire constantly.
//	OneShotProse … text a human reads and keeps: File pane edit suggestions, chat
//	               plan updates. Same tier as the assistant's own replies.
//
// They are separate because a single "utility" setting used to serve both, so choosing haiku
// for titles dropped the file-edit suggestions to haiku as well.
type OneShotTier int

const (
	OneShotShort OneShotTier = iota
	OneShotProse
)

// oneShotEnvModels are the operator's per-kind overrides of the one-shot recommendation. They
// used to apply only to a member with NO model setting, which the Console never leaves (its
// defaults store "recommended" for every kind, and ui_prefs.go reads a missing entry the same
// way) — so they were silently dead for everyone the settings screen could show. Folded into the
// recommendation itself, they now take effect for "recommended" and show up as its answer
// (#972 review, round 3).
var oneShotEnvModels = map[string]string{
	session.KindCodex:    "AF_TITLE_MODEL_CODEX",
	session.KindOpencode: "AF_TITLE_MODEL_OPENCODE",
	session.KindAgy:      "AF_TITLE_MODEL_AGY",
}

// oneShotEnvModel is kind's operator override, "" when none is set — or when it cannot be what
// runs: a model the member hid ("models not to use" wins over the operator's default, as it does
// at session launch), or, for agy, a name its catalog does not list (agyChatModel would drop it;
// a display name is resolved to its id). Then the computed recommendation applies instead, and
// that is what the screen names (#972 review, round 4).
func oneShotEnvModel(kind string) string {
	return oneShotEnvModelV(prefsVisibility, kind)
}

func oneShotEnvModelV(v visibility, kind string) string {
	name := oneShotEnvModels[kind]
	if name == "" {
		return ""
	}
	m := strings.TrimSpace(os.Getenv(name))
	if m == "" {
		return ""
	}
	if kind == session.KindAgy {
		return agyNamedModel(v, m)
	}
	return v.model(kind, m)
}

// recommendedOneShotModel is the "recommended" resolution for a tier — the Console shows the
// same split (Settings > AI assist, "short text" / "prose"). An operator override
// (oneShotEnvModel) wins over the computed rules.
func recommendedOneShotModel(kind string, tier OneShotTier) string {
	return recommendedOneShotModelV(prefsVisibility, kind, tier)
}

func recommendedOneShotModelV(v visibility, kind string, tier OneShotTier) string {
	if m := oneShotEnvModelV(v, kind); m != "" {
		return m
	}
	if kind == session.KindMuse {
		// Never "": a muse run with no --model lands on the contributor row (clamp 8), so the
		// recommendation is the model museOneShot would fall back to anyway.
		return museSafeDefault()
	}
	if tier == OneShotProse {
		return recommendedAssistantModelV(v, kind)
	}
	return recommendedUtilityModelV(v, kind)
}

// oneShotModelPref reads the user's per-backend choice for a tier.
func oneShotModelPref(kind string, tier OneShotTier) (string, bool) {
	if tier == OneShotProse {
		return aiProseModelPref(kind)
	}
	return aiShortModelPref(kind)
}

// OneShotSource says which layer of the precedence chain (docs/log/103 §103.5) answered a
// feature's backend kind: ① a feature-level agent pin, or ② the shared priority order
// (aiAssistOrder) falling back to it. /ai-assist/resolution surfaces this so the Settings
// screen can explain "why this backend" without recomputing the chain itself.
const (
	OneShotSourcePin     = "pin"
	OneShotSourceDefault = "default"
)

// oneShotKind resolves ① and, failing that, ②'s priority order — the kind half of
// resolveOneShot. The pin is checked FIRST: PreferredAssistAgent walks the whole priority
// order, which is wasted work whenever the pin is live (docs/log/103-review 軽12).
//
// This CAN shell out: headlessAgentAvailable's own cache is 1 minute, and a cold entry falls
// through to an actual `claude auth status` / `codex login status` / … call. That cost is
// acceptable for OneShotHeadlessRun (a real generation is about to run regardless) and for
// resolveOneShot's other caller, the mirror translation cache's pin check (session_translate.go
// — already mid-request, about to translate on a miss). It is NOT acceptable for
// /ai-assist/resolution, which must answer every poll without ever starting a CLI (docs/log/103
// §103.8-3) — that path uses oneShotKindCached below instead, never this one.
func oneShotKind(feature string) (kind, source string) {
	if pin := aiFeatureAgentPref(feature); oneShotRunnable(pin) && headlessAgentAvailable(pin) {
		return pin, OneShotSourcePin
	}
	return PreferredAssistAgent(), OneShotSourceDefault
}

// WarmOneShotKind exercises oneShotKind's real resolution (the one that CAN shell out) purely
// for its side effect on the availability cache; the answer is discarded. GET
// /ai-assist/resolution calls this in the background, AFTER writing its response, for any
// feature it could not answer from the cache (docs/log/103 §103.8-3, 103-impl-review 重大3):
// the only things that ever warm headlessAgentAvailable's cache are an actual chat turn or
// one-shot generation running (preferredFrom / ChatProviderFor / oneShotKind itself), so a
// settings tab opened before any of those has fired would otherwise poll "unknown" forever,
// even while the tab stays open and keeps asking.
//
// This IS per request, not an occasional doubled-up call: handleAIAssistResolution fires one
// `go WarmOneShotKind` per feature that came back unknown, up to 8 per request (ai_assist.go).
// What makes that safe is headlessAvailInFlight (this file, above) — it is the postmortem for
// the exact failure this comment used to wave off as not worth guarding against: ~490 real
// `claude` processes and ~25GiB RSS from concurrent callers on the same cold kind with no cap.
// Removing that guard on the strength of this paragraph would reopen it.
// The goroutine is started HERE rather than by the caller, so the bookkeeping that makes a
// warm waitable happens before this returns. A caller writing `go chatx.WarmOneShotKind(f)`
// leaves nothing to wait on, and the warms then outlive whatever started them — in production
// that is merely untidy, but in a test binary it means one test's stragglers mutate the shared
// availability cache in the middle of the next one (measured: 9 failures in 20 runs, plus a
// data race on the headlessAvailCheck seam).
func WarmOneShotKind(feature string) {
	warmWG.Add(1)
	go func() {
		defer warmWG.Done()
		oneShotKind(feature)
	}()
}

// warmWG counts the in-flight warms started by WarmOneShotKind. Production never waits on it;
// it exists so a test can (WaitForWarmsForTest).
var warmWG sync.WaitGroup

// WaitForWarmsForTest blocks until every warm started so far has finished. TEST ONLY, and the
// reason it is exported: the warming is fire-and-forget by design, so without this a test in
// package main can only sleep and hope — and a straggler that lands after the test ends
// corrupts the NEXT test instead, which is how this arrived (a flake that needed -count to
// see). Call it after each request whose warms must not escape.
func WaitForWarmsForTest() { warmWG.Wait() }

// resolveOneShot answers steps ① and ② of docs/log/103's precedence chain for one feature:
// the backend kind that would run, and the user's per-backend model choice for THAT kind
// (never the pin's kind when the pin fell through to ② — decision 4's model-follows-kind
// rule). configured mirrors oneShotModelPref's own (value, ok): ok distinguishes "nothing
// set, fall through further" from "explicitly cleared to the CLI default".
//
// OneShotHeadlessRun calls this, which is what keeps its answer identical to
// oneShotKindCached's whenever the availability cache happens to already be warm. What it does
// NOT cover is ③ (the per-backend fallback inside each OneShotHeadlessRun case), because that
// step already differs by CLI in ways only the actual call site can apply.
func resolveOneShot(feature string, tier OneShotTier) (kind, model string, configured bool, source string) {
	kind, source = oneShotKind(feature)
	model, configured = resolveOneShotModel(feature, tier, kind)
	return kind, model, configured, source
}

// resolveOneShotModel is step ② of resolveOneShot split out from the kind resolution: given a
// KNOWN kind, ① the feature's own pin or ② the shared tier default for that kind. Neither of
// those is a CLI call — only oneShotKind's own fallback (headlessAgentAvailable's cold-cache
// exec) can shell out — so this half is safe to reuse wherever the kind came from a cache peek
// instead of a live resolution (ResolveOneShotModelCached).
func resolveOneShotModel(feature string, tier OneShotTier, kind string) (model string, configured bool) {
	if v, ok := aiFeatureModelPref(feature, kind); ok {
		return v, true
	}
	return oneShotModelPref(kind, tier)
}

// headlessAvailableCached peeks the 1-minute availability cache WITHOUT ever falling through
// to an actual CLI call — the one difference from headlessAgentAvailable, and the reason this
// exists at all. known is false on a cold entry; only then may a caller honestly say "unknown"
// (docs/log/103 §103.8-3) instead of either guessing or paying to find out.
func headlessAvailableCached(kind string) (available, known bool) {
	headlessAvailMu.Lock()
	defer headlessAvailMu.Unlock()
	t, ok := headlessAvailAt[kind]
	if !ok || time.Since(t) >= time.Minute {
		return false, false
	}
	return headlessAvail[kind], true
}

// oneShotKindCached is oneShotKind's read-only twin for GET /ai-assist/resolution: same
// precedence (pin first, then the priority order), but every availability check is a cache
// peek, never a CLI call. ok is false when the cache cannot answer yet (a pin whose own state
// is unknown, or an unpinned feature where nothing in the order is confirmed reachable) — the
// caller reports that feature as source "unknown" rather than showing a value that might not
// be what actually runs.
func oneShotKindCached(feature string) (kind, source string, ok bool) {
	if pin := aiFeatureAgentPref(feature); oneShotRunnable(pin) {
		avail, known := headlessAvailableCached(pin)
		if !known {
			return "", "", false
		}
		if avail {
			return pin, OneShotSourcePin, true
		}
		// Confirmed unreachable: fall through to the order below, same as oneShotKind.
	}
	for _, k := range oneShotOrder() {
		if avail, known := headlessAvailableCached(k); known && avail {
			return k, OneShotSourceDefault, true
		}
	}
	return "", "", false
}

// ResolveOneShot exposes resolveOneShot to the mirror translation cache's pin check
// (session_translate.go — whether THIS feature is pinned to a concrete model right now).
// GET /ai-assist/resolution does NOT use this — see ResolveOneShotCached.
func ResolveOneShot(feature string, tier OneShotTier) (kind, model string, configured bool, source string) {
	return resolveOneShot(feature, tier)
}

// ResolveOneShotCached exposes oneShotKindCached to GET /ai-assist/resolution. It answers kind
// + source only: the model NAME is drawn by the Console from the catalog it already fetches
// (useModelOptions(kind)), not from here — docs/log/103 §103.8-3's fix for the 15/10/15-second
// catalog calls the original design would have paid on every poll.
func ResolveOneShotCached(feature string) (kind, source string, ok bool) {
	return oneShotKindCached(feature)
}

// ResolveOneShotModelCached exposes resolveOneShotModel for a caller that already has a kind
// from ResolveOneShotCached and needs the model half of decision 8's cache key without ever
// shelling out (session_translate.go's prefetch — 103-final-review 軽6). It is the same lookup
// resolveOneShot's model half does; only the kind step differs between the two.
func ResolveOneShotModelCached(feature string, tier OneShotTier, kind string) (model string, configured bool) {
	return resolveOneShotModel(feature, tier, kind)
}

// recommendedUtilityModel picks the cheap model shown as "recommended (currently: …)" for
// the short tier. The OpenCode Go route is pinned only when the live account catalog
// proves it is available; otherwise an empty result deliberately delegates to the
// CLI default rather than risking a metered/unentitled Zen model.
func recommendedUtilityModel(kind string) string {
	return recommendedUtilityModelV(prefsVisibility, kind)
}

func recommendedUtilityModelV(v visibility, kind string) string {
	// A candidate excluded by the hidden-models setting (model_deny.go) is not auto-selected
	// either.
	//
	// codex and agy follow the cheapest priced model their catalog lists (model_recommend.go),
	// falling back to what they did before prices were read. claude needs no ranking: the
	// "haiku" alias already moves to each new Haiku. opencode is deliberately NOT ranked: its
	// cheapest rows are the $0 "-free" promotions, which are retired every few weeks and are a
	// listing rather than an entitlement (see OneShotHeadlessRun's opencode branch).
	switch kind {
	case session.KindClaude:
		return claudeFirstVisible(v, claudeShortTiers)
	case session.KindCodex:
		ids, _ := codexRecommendIDs(v)
		if m := cheapestListedModel(kind, ids); m != "" {
			return m
		}
		return cheapOneShotModel(ids)
	case session.KindOpencode:
		const goModel = "opencode-go/deepseek-v4-flash"
		return recommendedCatalogModel(v.ids(kind, opencode.Models()), goModel, "")
	case session.KindAgy:
		if m := cheapestListedModel(kind, agyRecommendIDs(v)); m != "" {
			return m
		}
		return agyNamedModel(v, defaultAgyChatModel)
	}
	return ""
}

// codexOneShotModel is the -m a codex one-shot runs with ("" = none, the CLI default), and
// whether it is OUR pick (so a failure may retry without it — CodexOneShotWithRetry).
// selected / configured / auto are what OneShotHeadlessRun resolved from the settings, the
// "recommended" sentinel already replaced (auto marks that).
//
//   - nothing configured ⇒ the recommendation exactly as "recommended" gives it (which is
//     AF_TITLE_MODEL_CODEX when the operator set one — theirs, never retried away);
//   - configured ⇒ used as is. "" — the member's explicit CLI default, or a recommendation that
//     resolved to nothing — stays "no -m": neither the cheapest model nor the environment's
//     model may stand in for it (#972 review, rounds 1 and 2).
func codexOneShotModel(selected string, configured, auto bool, tier OneShotTier) (string, bool) {
	if !configured {
		m := recommendedOneShotModel(session.KindCodex, tier)
		return m, m != "" && oneShotEnvModel(session.KindCodex) == ""
	}
	return selected, auto && selected != ""
}

// codexOneShotArgsFor is the argv for a codex one-shot. --ephemeral: a one-shot never
// needs resume, so don't persist a thread even into the chat-only CODEX_HOME.
//
// The two savings knobs (docs/log/46 §1-a-2 / §2-b), mirroring what the claude path does:
//   - -m <model>: without it codex ran throwaway calls on whatever config.toml pins — on a
//     real workspace gpt-5.6-luna. The CALLER picks it (codexOneShotModel); "" means no -m.
//     This function no longer picks one of its own, nor reads AF_TITLE_MODEL_CODEX: it could
//     not tell "nobody chose" from "the member chose the CLI default", and replaced both
//     (#972 review) — an explicit Default ran gpt-6-luna while the screen said Default.
//   - -c model_reasoning_effort="low": the analog of MAX_THINKING_TOKENS=0. A title is
//     not a reasoning problem, and the user's configured effort (often "high") would
//     otherwise apply to every one-shot. "low" is supported by every listed model.
//
// The trailing "-" makes codex read the prompt from stdin; it must stay last.
func codexOneShotArgsFor(selected string) []string {
	args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--color", "never", "-C", chatWorkdir()}
	if selected != "" {
		args = append(args, "-m", selected)
	}
	return append(args, "-c", `model_reasoning_effort="low"`, "-")
}

// codexOneShotArgsNoModel strips OUR OWN -m pick for the one retry: a catalog entry the
// account cannot actually run must degrade to the previous behaviour, not to a broken
// feature (the opencode note below is the same trap, found by measurement).
func codexOneShotArgsNoModel(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-m" && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// runCodexOneShot executes one codex exec argv and returns the reply. codex reports a
// failure two ways — a non-zero exit AND a turn.failed/error event — so both are folded
// into err here, which is what makes the caller's single retry cover either shape.
// usage is returned as well so the one-shot lands in the ledger (ADR 0029 §3): when a call
// fails and is retried, two rows are the correct record of having fired twice.
func runCodexOneShot(ctx context.Context, args []string, prompt string) (string, CodexUsage, error) {
	cmd := chatCodexCmd(ctx, nil, args...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", CodexUsage{}, fmt.Errorf("codex execution failed: %s", cliErr(err))
	}
	reply, _, execErr, usage := parseCodexExecEvents(out)
	if execErr != "" {
		return "", usage, errors.New(execErr)
	}
	return reply, usage, nil
}

// codexOneShotRun is the shape of runCodexOneShot (tests substitute their own).
type codexOneShotRun func(ctx context.Context, args []string, prompt string) (string, CodexUsage, error)

// CodexOneShotWithRetry fires with the small model we picked ourselves and, on a failure,
// fires once more with the model flag removed. For the ledger the point is that it returns the
// tokens of BOTH attempts added together.
//
// The cheap model came from the catalog, and a catalog entry is not proof the account may
// run it (measured: opencode lists claude-haiku-4-5 and then 500s on it). Falling back to the
// configured default once — a title that costs more still beats a title feature that is broken.
//
// The first attempt's consumption happened even though it failed. Overwriting it with the
// retry's would erase what was actually spent, and "fail on the cheap model → fire again on
// the default flagship" is the most expensive path there is, so it is exactly the one that
// must not look like a single call. The requested value (model_req) is overwritten by the
// retry so that "re-fired with the model dropped" is visible (there is only one row, so the
// later value wins and names the attempt that actually answered).
func CodexOneShotWithRetry(ctx context.Context, args []string, autoPicked bool, prompt string,
	run codexOneShotRun) (reply string, tok usagex.Tokens, modelReq string, err error) {
	modelReq = argValue(args, "-m")
	reply, usage, err := run(ctx, args, prompt)
	tok = usage.LedgerTokens()
	if err != nil && autoPicked {
		reply, usage, err = run(ctx, codexOneShotArgsNoModel(args), prompt)
		tok, modelReq = tok.Add(usage.LedgerTokens()), ""
	}
	return reply, tok, modelReq, err
}

// OneShotHeadless runs one prompt through the resolved backend and returns the reply text —
// the backend-agnostic core of the title/branch suggestions. feature picks the precedence
// chain (docs/log/103 §103.5 — a feature-level agent pin, then the shared priority order);
// it MUST be one of the usagex.Feature* constants and MUST be passed by the caller rather
// than read back off ctx's usage tag: the tag is an observation axis (usagex/call.go),
// and reading it here would let a legitimate re-tag (chat_compact.go's mid-turn rewrite)
// silently change which agent/model runs (docs/log/103-review 重大1). persona is passed
// natively where possible (claude --system-prompt) and as a prompt preamble otherwise.
// claudeModel applies to the claude backend only (codex/opencode run their own configured
// defaults; override via AF_TITLE_MODEL_CODEX/_OPENCODE — docs/log/46 §2-b flags that an
// unset override means the CLI's own default, usually the flagship).
func OneShotHeadless(ctx context.Context, feature string, tier OneShotTier, persona, prompt, claudeModel string) (string, error) {
	reply, _, _, err := OneShotHeadlessRun(ctx, feature, tier, persona, prompt, claudeModel)
	return reply, err
}

// OneShotHeadlessRun is OneShotHeadless plus the backend kind and model that ACTUALLY ran.
//
// Nothing currently reads kind/model (103-final-review E-8): the mirror translation cache key
// used to (docs/log/103 decision 8's original text), but that was corrected — a cache key can
// only use a value the NEXT lookup can predict, and this call's actual kind/model is a
// prediction raced by the same 1-minute availability cache this call itself refreshes, so using
// it produced rows no future lookup could ever match (103-final-review 中1). The key now comes
// from resolveCacheModel (session_translate.go), a SEPARATE resolution the write and every read
// share.
//
// Kept anyway rather than folded back into OneShotHeadless: kind/model are already computed
// here for the usage ledger (call.Kind/call.ModelReq below) regardless of whether anyone reads
// the return, so there is no cost to keeping them — and a caller that needs "what actually ran"
// for something other than a cache key (a ledger cross-check, say) has a place to get it without
// re-deriving resolveOneShot's own logic.
func OneShotHeadlessRun(ctx context.Context, feature string, tier OneShotTier, persona, prompt, claudeModel string) (reply, kind, model string, err error) {
	// Usage ledger (ADR 0029 §3). This function takes the first usable backend out of
	// the priority order (oneShotKinds), so kind is filled in inside the branch, as a
	// result of what ran: writing the requested value instead would turn all consumption of a
	// claude-less workspace into claude's (docs/log/46 §2). Recording inside rather than
	// widening the return value keeps the four call sites untouched, since the recording point
	// is in here anyway.
	call := usagex.Call{}
	defer usagex.RecordCall(ctx, &call, time.Now())
	defer func() { kind, model = call.Kind, call.ModelReq }()
	kind, selected, configured, _ := resolveOneShot(feature, tier)
	selected = strings.TrimSpace(selected)
	// Our own pick — retried without -m if it fails — only when the recommendation was computed;
	// an operator's AF_TITLE_MODEL_* is theirs and is never dropped behind their back.
	autoRecommended := selected == AssistantRecommendedModel && oneShotEnvModel(kind) == ""
	if selected == AssistantRecommendedModel {
		selected, configured = recommendedOneShotModel(kind, tier), true
	}
	switch kind {
	case session.KindCodex:
		call.Kind = session.KindCodex
		defer func() { _, _ = chatCodexHome() }()
		full := headlessPrompt(persona, nil, prompt)
		selected, autoRecommended = codexOneShotModel(selected, configured, autoRecommended, tier)
		args := codexOneShotArgsFor(selected)
		reply, tok, modelReq, err := CodexOneShotWithRetry(ctx, args, autoRecommended, full, runCodexOneShot)
		call.ModelReq, call.Totals, call.OK = modelReq, tok, err == nil
		return reply, "", "", err
	case session.KindOpencode:
		call.Kind = session.KindOpencode
		args := []string{"run", "--format", "json", "--dir", chatWorkdir()}
		env := opencode.Env()
		// read-only posture (docs/log/44 §1.3): unlike a chat, a one-shot runs in the bare
		// chatWorkdir with no project config (opencode.json), so OPENCODE_CONFIG's deny is the
		// one that takes effect (merge precedence measured on 1.18.7: project >
		// OPENCODE_CONFIG > global — see opencodeChatDir). If it cannot be written the run
		// goes bare as before, degrading on a broken home rather than breaking title/reply.
		if cfg := OpencodeOneShotConfig(); cfg != "" {
			env = append(env, "OPENCODE_CONFIG="+cfg)
		}
		// NOTE: deliberately NOT auto-picking a cheap model here (docs/log/46 §1-a-2). opencode's
		// catalog is a LISTING, not an entitlement: `opencode/claude-haiku-4-5` is listed and
		// selectable, yet running it returns "Unexpected server error" on an account without
		// it, while the configured default answers fine (measured 2026-07-25). A hard failure of
		// title/branch/reply suggestions is worse than their token cost, so opencode keeps the
		// user's default unless AF_TITLE_MODEL_OPENCODE names something explicitly.
		m := selected
		if !configured {
			m = recommendedOneShotModel(kind, tier) // AF_TITLE_MODEL_OPENCODE first, as for "recommended"
		}
		if m != "" {
			args = append(args, "--model", m)
			call.ModelReq = m
		}
		args = append(args, headlessPrompt(persona, nil, prompt))
		cmd := exec.CommandContext(ctx, "opencode", args...)
		cmd.Dir = chatWorkdir()
		cmd.Env = envWith(env...)
		out, err := cmd.Output()
		reply, sesID, reportedModel, turnErr, usage := parseOpencodeRunEvents(out)
		if err != nil {
			if turnErr != "" {
				return "", "", "", fmt.Errorf("opencode turn failed: %s", turnErr)
			}
			return "", "", "", fmt.Errorf("opencode execution failed: %s", cliErr(err))
		}
		call.Totals, call.OK = usage.LedgerTokens(), true
		if reportedModel != "" {
			call.Models = []usagex.ModelRow{{Model: reportedModel, ModelRaw: reportedModel, Tokens: call.Totals}}
		}
		// opencode has no ephemeral mode — delete the throwaway session so one-shots
		// don't pile "New session…" rows into the shared store. Best-effort, detached
		// from ctx (the reply is already in hand; cleanup shouldn't be cancelled).
		if sesID != "" {
			go func() {
				cl := exec.Command("opencode", "session", "delete", sesID)
				cl.Dir = chatWorkdir()
				cl.Env = envWith(opencode.Env()...)
				_ = cl.Run()
			}()
		}
		return reply, "", "", nil
	case session.KindCursor:
		// cursor has no ephemeral mode; without --resume each one-shot mints a fresh
		// chat, leaving a throwaway in ~/.cursor (same trade-off as agy/opencode
		// one-shots). No tools/persona-native flag — persona rides the preamble.
		call.Kind = session.KindCursor
		args := cursorChatBaseArgs()
		m := selected
		if !configured {
			m = os.Getenv("AF_TITLE_MODEL_CURSOR")
			if m == "" {
				m = recommendedOneShotModel(kind, tier)
			}
		}
		if m != "" {
			args = append(args, "--model", m)
			call.ModelReq = m
		}
		cmd := exec.CommandContext(ctx, cursor.Bin(), args...)
		cmd.Dir = chatWorkdir()
		cmd.Env = cursor.EnvWithoutCI(os.Environ()) // do not pass CI through (cursor/ci_env.go)
		cmd.Stdin = strings.NewReader(headlessPrompt(persona, nil, prompt))
		out, err := cmd.Output()
		if err != nil {
			return "", "", "", fmt.Errorf("cursor execution failed: %s", cliErr(err))
		}
		r, perr := parseCursorResult(out)
		if perr != nil {
			return "", "", "", perr
		}
		call.SetTotals(r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
		if r.IsError {
			return "", "", "", errors.New(strings.TrimSpace(r.Result))
		}
		call.OK = true
		return strings.TrimRight(strings.TrimSpace(r.Result), "\n"), "", "", nil
	case session.KindAgy:
		// agy has no ephemeral mode, so each one-shot leaves a throwaway conversation
		// behind — contained in the shared "oneshot" isolated home rather than the
		// user's real agy state (which would also spawn their own global MCP servers
		// on every title call). Runs on the cheap Flash default (quota-scarce free
		// plan) unless overridden.
		// agy outputs plain text, so no tokens can be read at all — measured=none, count only.
		call.Kind, call.Measured = session.KindAgy, usagex.MeasuredNone
		home, wdir, err := chatAgyHome(&ChatConversation{ID: "oneshot"})
		if err != nil {
			return "", "", "", fmt.Errorf("agy chat home: %v", err)
		}
		var args []string
		m := selected
		if !configured {
			// The same answer "recommended" gives (RecommendedModels), so a member who never
			// opened the setting runs what the screen calls the recommendation.
			m = recommendedOneShotModel(kind, tier)
		}
		if m := agyChatModel(m, filterVisibleModels(session.KindAgy, agy.Models())); m != "" {
			args = append(args, "--model", m)
			call.ModelReq = m
		}
		args = append(args, "-p", headlessPrompt(persona, nil, prompt))
		cmd := exec.CommandContext(ctx, "agy", args...)
		cmd.Dir = wdir
		cmd.Env = agy.Env(envWith("HOME=" + home))
		defer reconcileChatCreds(agy.TokenPath(), filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
		out, err := cmd.Output()
		if err != nil {
			return "", "", "", fmt.Errorf("agy execution failed: %s", cliErr(err))
		}
		call.OK = true
		return strings.TrimRight(strings.TrimSpace(string(out)), "\n"), "", "", nil
	case session.KindMuse:
		call.Kind, call.Measured = session.KindMuse, usagex.MeasuredNone
		m := selected
		if !configured {
			m = recommendedOneShotModel(kind, tier)
		}
		reply, err := museOneShot(ctx, &call, persona, prompt, m)
		return reply, "", "", err
	case session.KindClaude:
		// runs below
	default:
		// oneShotKind never returns a kind outside oneShotKinds; this is the backstop that keeps
		// a new kind from silently running (and being billed) on claude.
		call.Kind = kind
		return "", "", "", fmt.Errorf("AI assist cannot run on %q", kind)
	}
	// claude: the historical path, kept native. --no-session-persistence is
	// claude's --ephemeral analog (print-mode only, no transcript written, no resume):
	// a one-shot never resumes, so don't pile per-call jsonl into Claude's projects tree.
	//
	if configured {
		claudeModel = selected
	}
	call.Kind, call.ModelReq = session.KindClaude, claudeModel
	cmd := chatClaudeCmd(ctx, claudeOneShotArgs(persona, claudeModel)...)
	cmd.Env = append(cmd.Env, claudeOneShotEnv...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	var r claudeResult
	perr := json.Unmarshal(out, &r)
	// claude alone returns a measured per-model breakdown and its cost (docs/log/46 §0). An
	// error result is billed too, so take them before deciding OK; and claude may still have
	// printed a structured result on an exec error (a stop, a non-zero exit), so parse before
	// returning the error.
	call.Models, call.CostUSD = UsageModelRows(r.ModelUsage), r.TotalCostUSD
	call.FallbackTotals(r.Usage.LedgerTokens(), "") // degrade for a response with no modelUsage
	if err != nil {
		return "", "", "", fmt.Errorf("claude execution failed: %s", cliErr(err))
	}
	if perr != nil || r.IsError {
		return "", "", "", errors.New("claude returned an invalid response/error")
	}
	call.OK = true
	return strings.TrimRight(r.Result, "\n"), "", "", nil
}

// argValue returns the value following a flag in argv ("" when not found). It puts "which
// model was requested in the end" into the ledger's model_req without duplicating the
// branching of the code that built the arguments.
func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// cliErr renders an exec error, surfacing captured stderr when present.
func cliErr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		s := strings.TrimSpace(string(ee.Stderr))
		if len(s) > 500 {
			s = s[:500] + "…"
		}
		return s
	}
	return err.Error()
}

// chatWorkdir is a dedicated empty dir the headless CLIs run in, so a chat turn
// never accidentally touches the user's repos.
func chatWorkdir() string {
	d := filepath.Join(paths.AgentStateDir(), "chat-wd")
	_ = os.MkdirAll(d, 0o700)
	return d
}

// legacyChatClaudeDir was the chat-only CLAUDE_CONFIG_DIR. New runs use Claude's
// shared config directly so every process sees one OAuth refresh-token file. The old
// directory remains only as a transcript migration source for existing conversations.
func chatClaudeDir() string {
	if v := os.Getenv("AF_CHAT_CLAUDE_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentStateDir(), "chat-claude")
}

var migrateChatClaudeOnce sync.Once

// migrateLegacyChatClaudeProjects copies provider transcripts created under the old
// isolated CLAUDE_CONFIG_DIR into the shared projects tree. Files are create-only:
// an existing shared transcript always wins. Credentials/settings are deliberately
// excluded. This preserves --resume across the storage-layout change without ever
// reintroducing a second OAuth credential file.
func migrateLegacyChatClaudeProjects() {
	migrateChatClaudeOnce.Do(func() {
		srcRoot := filepath.Join(chatClaudeDir(), "projects")
		dstRoot := filepath.Join(claude.ConfigDir(), "projects")
		copyLegacyChatClaudeProjects(srcRoot, dstRoot)
	})
}

func copyLegacyChatClaudeProjects(srcRoot, dstRoot string) {
	_ = filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // best-effort migration; a missing legacy tree is normal
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == "." {
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			_ = os.MkdirAll(dst, 0o700)
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer in.Close()
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil // already migrated (or not writable): never overwrite
		}
		_, copyErr := io.Copy(out, in)
		_ = out.Close()
		if copyErr != nil {
			_ = os.Remove(dst)
		}
		return nil
	})
}

// reconcileChatCreds ensures link is a symlink to shared, self-healing the case where
// a prior atomic-rename replaced the symlink with a real file (newer token wins → copy
// back to shared, then relink). Only links when shared exists (user authenticated).
func reconcileChatCreds(shared, link string) {
	if target, err := os.Readlink(link); err == nil && target == shared {
		return // already the right symlink
	}
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 { // a real file replaced the link
			li, _ := os.Stat(link)
			si, _ := os.Stat(shared)
			if li != nil && (si == nil || li.ModTime().After(si.ModTime())) {
				if b, rerr := os.ReadFile(link); rerr == nil {
					_ = os.MkdirAll(filepath.Dir(shared), 0o700)
					_ = os.WriteFile(shared, b, 0o600)
				}
			}
		}
		_ = os.Remove(link)
	}
	if _, err := os.Stat(shared); err == nil {
		_ = os.Symlink(shared, link)
	}
}

// envWith returns os.Environ() with the given KEY=VAL entries overriding any existing
// occurrence (Go's exec doesn't dedupe env, so we replace rather than append).
func envWith(over ...string) []string {
	set := map[string]string{}
	for _, e := range over {
		if i := strings.IndexByte(e, '='); i > 0 {
			set[e[:i]] = e[i+1:]
		}
	}
	out := make([]string, 0, len(os.Environ())+len(set))
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			if _, ok := set[e[:i]]; ok {
				continue
			}
		}
		out = append(out, e)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// chatToolLimits blocks tools the assistant chat must never use. The critical one is the
// subagent/orchestration set (Agent — the current name, Task — the legacy alias, Workflow):
// a large translation once fanned OUT into many parallel subagents and OOM-killed the
// shared, memory-constrained host (see host-oom-fleet-risk). Bash/Edit/Write/NotebookEdit
// are also blocked — the chat persona forbids file/command work anyway. --disallowedTools
// is enforced by Claude Code even under --dangerously-skip-permissions (that flag only skips
// approval prompts, not deny rules). Read/Glob/Grep/WebFetch and the MCP af tools remain.
//
// ListAgents / SendMessage are claude's own cross-session channel (docs/log/58 §58.17). A
// headless `-p` binds no socket, so it cannot receive but it CAN send: leave them open and the
// assistant can type into sessions in this workspace from outside Agent Fleet. Injection on
// the operator side belongs to the af MCP's send_to_session, which goes through the ledger.
func chatToolLimits() []string {
	return []string{"--disallowedTools",
		"Agent", "Task", "Workflow",
		"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit",
		"ListAgents", "SendMessage"}
}

// chatClaudeCmd runs chat turns against Claude's single shared config directory. This
// is intentionally different from the old credentials symlink: Claude refreshes OAuth
// state via atomic rename, which replaces a symlink and can strand concurrent sessions
// on different refresh tokens. --setting-sources "" plus --strict-mcp-config at the
// caller keeps user/project hooks and MCP configuration out of the headless chat while
// auth and provider-native resume state remain shared and race-free.
func chatClaudeCmd(ctx context.Context, args ...string) *exec.Cmd {
	migrateLegacyChatClaudeProjects()
	args = append([]string{"--setting-sources", ""}, args...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = chatWorkdir()
	cmd.Env = envWith("CLAUDE_CONFIG_DIR=" + claude.ConfigDir())
	return cmd
}
