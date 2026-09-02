package main

// アシスタントチャットのプロバイダ実装（claude/codex/opencode/agy/cursor の headless
// CLI 駆動）と CLI 起動まわりの下回り。chat.go からの機械的分割（docs/log/23 残②）。

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
)

// --- providers ---

// chatProvider drives one agent's CLI in non-interactive mode. send appends the
// assistant reply's text as its return value and may mutate c's resume handles.
type chatProvider interface {
	send(ctx context.Context, c *chatConversation, prompt string) (string, error)
}

var chatProviders = map[string]chatProvider{
	session.KindClaude:   claudeChat{},
	session.KindCodex:    codexChat{},
	session.KindOpencode: opencodeChat{},
	session.KindAgy:      agyChat{},
	session.KindCursor:   cursorChat{},
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
)

// headlessAgentAvailable reports (cached) whether a backend's CLI is authenticated.
func headlessAgentAvailable(kind string) bool {
	headlessAvailMu.Lock()
	if t, ok := headlessAvailAt[kind]; ok && time.Since(t) < time.Minute {
		v := headlessAvail[kind]
		headlessAvailMu.Unlock()
		return v
	}
	headlessAvailMu.Unlock()
	var v bool
	switch kind {
	case session.KindClaude:
		v = claude.LoggedIn()
	case session.KindCodex:
		v = codex.LoggedIn()
	case session.KindOpencode:
		// binary present AND actually usable (stored key / oauth / explicit free-tier
		// opt-in) — opencode.Available() alone used to let assistant chat silently fall
		// back to opencode's zero-auth free tier for any unconfigured workspace, which
		// some tenants' security policy forbids. Default OFF, opt-in only.
		v = opencode.Available() && opencode.Connected()
	case session.KindAgy:
		v = agy.SignedIn()
	case session.KindCursor:
		v = cursor.LoggedIn()
	}
	headlessAvailMu.Lock()
	headlessAvailAt[kind], headlessAvail[kind] = time.Now(), v
	headlessAvailMu.Unlock()
	return v
}

// defaultHeadlessOrder is the built-in auto-selection order for assistant-chat
// backends. agy sits last on purpose: its Starter/free quota is tiny (docs/log/32
// Track D), so out of the box it is only reached in an agy-only workspace. The
// user can rank the backends themselves in 設定 > エージェント (ui-prefs
// assistantAgentOrder — assistantAgentOrderPref normalizes against this list).
var defaultHeadlessOrder = []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCursor, session.KindAgy}

// preferredHeadlessAgent picks the backend for new builtin-assistant conversations
// and one-shot calls: the first AUTHENTICATED backend in the user's priority order
// (or the built-in default order). When nothing is connected it returns the order's
// top choice, so the resulting error points at the CLI the user cares about most.
func preferredHeadlessAgent() string {
	order := assistantAgentOrderPref()
	for _, k := range order {
		if headlessAgentAvailable(k) {
			return k
		}
	}
	return order[0]
}

// chatProviderFor resolves the provider driving this conversation: the pinned agent
// while its CLI is authenticated, else the preferred available backend — so a
// claude-less (codex-only / opencode-only) workspace still gets working assistants.
// Each provider keeps its own resume handle on the conversation. The canonical
// message cursor synchronizes turns handled by another backend before that handle is
// resumed (chat_provider_context.go).
func chatProviderFor(c *chatConversation) chatProvider {
	if prov, ok := chatProviders[c.Agent]; ok && headlessAgentAvailable(c.Agent) {
		return prov
	}
	if prov, ok := chatProviders[preferredHeadlessAgent()]; ok {
		return prov
	}
	return chatProviders[session.KindClaude]
}

// chatProviderKind returns the concrete backend selected by chatProviderFor. Keeping
// this out of chatProvider avoids widening every test stub merely for presentation
// metadata. Production providers are the five value types below.
func chatProviderKind(c *chatConversation, prov chatProvider) string {
	switch prov.(type) {
	case claudeChat:
		return session.KindClaude
	case codexChat:
		return session.KindCodex
	case opencodeChat:
		return session.KindOpencode
	case agyChat:
		return session.KindAgy
	case cursorChat:
		return session.KindCursor
	default:
		return c.Agent // test/custom provider: best truthful fallback available
	}
}

// claudeChat runs `claude -p` (headless), pinning a session id on the first turn
// and resuming it thereafter so context carries across turns. Auth is the
// container's existing CLAUDE_CODE_OAUTH_TOKEN / CLAUDE_CONFIG_DIR (subscription).
type claudeChat struct{}

type claudeResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	// Usage/ModelUsage feed the conversation's context-fill snapshot (chat_usage.go):
	// usage.iterations' last entry is the final per-call snapshot, and modelUsage
	// carries the model's real contextWindow.
	Usage      claudeUsage                 `json:"usage"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
	// TotalCostUSD is claude's OWN measured cost for this call — the one backend that
	// doesn't need a price table (docs/log/46 §0). Recorded as the ledger's cost_usd.
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func (claudeChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	c.startTurn()
	// 使用量台帳（ADR 0029 §3）: 呼び出しの開始時点で記録を defer に積む。成功・エラー
	// result・exec 失敗・パース失敗のどの経路を通っても必ず1回だけ行が残る。
	call := usageCall{Kind: session.KindClaude, ModelReq: chatModelFor(c, session.KindClaude)}
	defer recordUsageCall(ctx, &call, time.Now())
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModelFor(c, session.KindClaude))
	args = append(args, chatToolLimits()...) // no subagents (OOM) / no file+shell tools
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = randUUID()
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
	// 失敗した result にも usage は乗る（超過エラーは特に高い）ので、OK 判定より先に採る。
	call.Models, call.CostUSD = usageModelRows(r.ModelUsage), r.TotalCostUSD
	// modelUsage の無い result（古い CLI・異常終了）でも総量は残す。
	call.FallbackTotals(r.Usage.ledgerTokens(), "")
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

// chatStreamEvent is one incremental event a streamingProvider emits: either a text Delta
// for the current (tentative) answer, or a completed Step (the model finished a working
// message that ended in a tool call). Exactly one field is set per emit.
type chatStreamEvent struct {
	Delta string    // incremental text of the current answer
	Step  *chatStep // a just-completed working step (narration + tool names)
}

// streamingProvider is the optional token-streaming variant of chatProvider. emit is called
// per incremental event; the returned string is the final answer and the []chatStep are the
// working steps (docs/log/19). A provider that doesn't implement it falls back to send() (one
// emit of the whole result) in handleChatStream, so every agent works through the stream.
type streamingProvider interface {
	sendStream(ctx context.Context, c *chatConversation, prompt string, emit func(chatStreamEvent)) (string, []chatStep, error)
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
		Usage claudeUsage `json:"usage"`
	} `json:"message"`
	// Usage/ModelUsage/TotalCostUSD ride the final "result" line (same shape as claudeResult).
	Usage        claudeUsage                 `json:"usage"`
	ModelUsage   map[string]claudeModelUsage `json:"modelUsage"`
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

func (claudeChat) sendStream(ctx context.Context, c *chatConversation, prompt string, emit func(chatStreamEvent)) (string, []chatStep, error) {
	c.startTurn()
	// 使用量台帳（ADR 0029 §3）— send と同じく全経路で1回記録する。
	call := usageCall{Kind: session.KindClaude, ModelReq: chatModelFor(c, session.KindClaude)}
	defer recordUsageCall(ctx, &call, time.Now())
	// stream-json requires --verbose with -p; --include-partial-messages adds the
	// per-token text_delta events we forward for live display.
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--dangerously-skip-permissions", "--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModelFor(c, session.KindClaude))
	args = append(args, chatToolLimits()...) // no subagents (OOM) / no file+shell tools
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = randUUID()
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
							emit(chatStreamEvent{Delta: sl.Event.Delta.Text})
						}
					case "message_delta":
						// A message that stops to call a tool is a working step; flush it and
						// reset so the next message accumulates as a fresh (tentative) answer.
						if sl.Event.Delta.StopReason == "tool_use" {
							step := chatStep{Text: strings.TrimSpace(cur.String()), Tools: curTools}
							if step.Text != "" || len(step.Tools) > 0 {
								steps = append(steps, step)
								emit(chatStreamEvent{Step: &step})
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
					call.Models, call.CostUSD = usageModelRows(sl.ModelUsage), sl.TotalCostUSD
					call.FallbackTotals(sl.Usage.ledgerTokens(), "")
				}
			}
		}
		if rerr != nil {
			break // EOF or read error — the process is done streaming
		}
	}
	waitErr := cmd.Wait()
	// 利用者の停止操作や result イベント前の異常終了では modelUsage が来ない。assistant
	// イベントで見た最後のスナップショットを縮退として採る（出力側は途中なので partial）。
	// 採らないと「トークン 0 / measured=none」の行になり、**止めた回の消費だけが台帳から
	// 消える** — 止められるのは重いターンほど多いので、そこが欠けると配分の絵が狂う。
	call.FallbackTotals(ctxTrack.snap.ledgerTokens(), usageMeasuredPartial)
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

func (codexChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	c.startTurn()
	// 使用量台帳（ADR 0029 §3）。codex はどのイベントにもモデルを載せない（実測）ので、
	// -m を渡した時だけ requested、未指定なら default_unknown に縮退する。
	model := chatModelFor(c, session.KindCodex) // ピン留めが別 kind ならこの CLI の設定から解決
	call := usageCall{Kind: session.KindCodex, ModelReq: model}
	defer recordUsageCall(ctx, &call, time.Now())
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
	call.Totals = usage.ledgerTokens()
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
	// codex はモデルを名乗らないので、記録できるのは -m で渡した値だけ（未指定なら
	// codex 自身の既定＝こちらからは不明なので空のまま）。
	c.noteTurnModel(model)
	// codex の input_tokens は cached を含む（chat_usage.go）: fresh = input - cached。
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
	dir := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-codex")
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
func codexMCPArgs(c *chatConversation) (args, env []string) {
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
func parseCodexExecEvents(out []byte) (reply, threadID, execErr string, usage codexUsage) {
	var texts []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Type     string     `json:"type"`
			ThreadID string     `json:"thread_id"`
			Message  string     `json:"message"`
			Usage    codexUsage `json:"usage"`
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

func (opencodeChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	c.startTurn()
	pinned := chatModelFor(c, session.KindOpencode)                 // ピン留めが別 kind ならこの CLI の設定から解決
	call := usageCall{Kind: session.KindOpencode, ModelReq: pinned} // 使用量台帳（ADR 0029 §3）
	defer recordUsageCall(ctx, &call, time.Now())
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
	call.Totals = usage.ledgerTokens()
	if model != "" {
		call.Models = []usageModelRow{{Model: model, ModelRaw: model, Tokens: call.Totals}}
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
	// opencode はイベントに実モデルを載せる（parseOpencodeRunEvents）。取れなかった
	// 版では --model に渡した値へ退がる（渡していなければ空＝非表示）。
	if model != "" {
		c.noteTurnModel(model)
	} else {
		c.noteTurnModel(pinned)
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
// 実測 1.18.7（`opencode debug config`、opencode_contract_test.go が固定）: opencode
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
// the path a conversation runs in is part of its resume identity (実測 1.18.5:
// `run --session <id>` from another dir hangs outright rather than erroring). That is
// why the grant split (none/read/write) stays even though the file itself no longer
// differs per grant — collapsing it would strand every existing conversation's
// session.
//
// opencode takes this config from the DIR, not per invocation, so conversations that
// resolve to different server sets must not share one — otherwise a concurrent turn's
// rewrite decides which servers this turn gets. The dir key is therefore the grant
// plus a digest of the resolved registry set (docs/log/48 §7 制約), not the grant alone.
//
// The af MCP server is NOT here, and that is load-bearing rather than tidy: it needs
// the conversation id (docs/log/30 report_to), which is per conversation, not per grant.
// opencode merges the config sources and **the project config wins the collision**
// (実測 1.18.7), so an af entry here would not merely "resurface" — it would BEAT the
// per-conversation one and strip --conv from every chat, silently killing セッション報告
// for good. Registry servers are safe to carry here because both files derive them from
// the same conversation, so the copies never disagree. See opencodeChatConfig.
//
// Falls back to the shared chat workdir when the dir can't be prepared (opencode then
// runs with its defaults).
func opencodeChatDir(c *chatConversation) string {
	grant := "none"
	if c.afToolsEnabled() {
		grant = "read"
		if c.afWriteEnabled() {
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
	dir := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "opencode-"+grant+serverSetKey(servers))
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
// 【セッション報告】 it is told to wait for (docs/log/30). claude/codex/agy pass --conv in
// their own per-conversation config; opencode used to have nowhere to put it because
// its only config was the per-GRANT project dir shared by every conversation.
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
func opencodeChatConfig(c *chatConversation) string {
	if !c.afToolsEnabled() || !validConvID(c.ID) {
		return ""
	}
	dir := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "opencode-conv")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("opencode chat config: %v (af tools unavailable this turn)", err)
		return ""
	}
	mcpArgs := []string{paths.ExePath(), "mcp-stdio"}
	if c.afWriteEnabled() {
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

// opencodeOneShotConfig writes the static read-only policy config for opencode
// one-shots (title / branch / reply / edit-suggestion — oneShotHeadless) and returns
// its path for OPENCODE_CONFIG. One-shots run in the bare chatWorkdir with no
// opencode.json, so without this file the run inherited the user's global config —
// the only backend of oneShotHeadless whose tool posture wasn't pinned read-only
// (claude --tools "" / codex no tool grant / cursor --mode ask). docs/log/44 §1.3 の
// read-only 提案生成チャネルはここで閉じる。Returns "" when the file can't be
// written (the caller then degrades to today's behaviour rather than breaking
// titles on a broken home).
func opencodeOneShotConfig() string {
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
// as an EVENT on stdout and exits non-zero with an empty stderr (実測 1.18.5):
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
				turnErr = msg // 最後のエラーが正（後続の方が具体的なことが多い）
			}
		}
		for _, m := range []string{ev.ModelID, ev.Part.ModelID, ev.Message.ModelID} {
			if m != "" {
				model = m // 最後に見えたものが正（1 run 内でモデルが変わることは想定しない）
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
// the read tools plus `mcp(<server>/*)` for each granted server (rule syntax
// reverse-engineered from the binary and live-verified 2026-07-20). Command/write
// tools stay auto-denied — no --dangerously-skip-permissions. No usage events, so
// the context gauge stays empty (Context = nil).
type agyChat struct{}

func (agyChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	c.startTurn()
	// 使用量台帳（ADR 0029 §3）: agy は素のテキストしか返さないので measured=none —
	// トークンは 0 ではなく「未計測」として、回数だけを数える。
	call := usageCall{Kind: session.KindAgy, ModelReq: chatModelFor(c, session.KindAgy), Measured: usageMeasuredNone}
	defer recordUsageCall(ctx, &call, time.Now())
	home, wd, err := chatAgyHome(c)
	if err != nil {
		// Never fall back to the real HOME: the MCP/permission config is global
		// there and would leak into the user's interactive agy sessions.
		return "", fmt.Errorf("agy chat home: %v", err)
	}
	args, model := agyChatArgs(c, headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	cmd := exec.CommandContext(ctx, "agy", args...)
	cmd.Dir = wd
	cmd.Env = envWith("HOME=" + home)
	// agy may refresh the OAuth token via tmp+rename, replacing the symlink with a
	// diverging real file — fold a rotated token back to the shared one (codex 同様).
	defer reconcileChatCreds(agy.TokenPath(), filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("agy execution failed: %s", cliErr(err))
	}
	if c.AgyConversationID == "" {
		// First turn: adopt the conversation this run just recorded for its private
		// cwd inside the private home — doubly unambiguous.
		c.AgyConversationID = agy.LastConversationIn(
			filepath.Join(home, ".gemini", "antigravity-cli"), wd)
	}
	reply := strings.TrimRight(strings.TrimSpace(string(out)), "\n")
	if reply == "" {
		return "", errors.New("no response from agy")
	}
	call.OK = true
	c.noteTurnModel(model) // 渡した --model のみ（agy は名乗らない）
	return reply, nil
}

// agyChatArgs builds the argv for one agy chat turn: flags first, the prompt as
// `-p`'s value last (verified live v1.1.4 — `-p <prompt>` with the display-name
// --model and --conversation resume all honored). The second return is the model
// actually passed ("" when the pin was dropped or none was set, i.e. agy runs on its
// own default) — agy prints plain text and names no model, so this is the only
// truthful thing to record for the turn.
func agyChatArgs(c *chatConversation, prompt string) ([]string, string) {
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
//     and permissions.allow (both files carry it: the effective location has
//     shifted between builds, docs/log/32 D-5, and an extra copy is harmless).
//   - config/mcp_config.json — the granted MCP servers. agy's spawned MCP servers
//     inherit its env, so each entry pins env.HOME back to the REAL home (the af
//     server must read the user's actual session state — live-verified).
//
// Per-conversation (not per-grant like opencode) because a write grant's --conv
// must ride mcp_config.json args — there is no per-invocation override to carry
// the conversation id. The dir doubles as the cwd→UUID capture scope.
func chatAgyHome(c *chatConversation) (home, wd string, err error) {
	base := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "agy-"+c.ID)
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
	allow := agyChatAllowRules(c)
	settings := map[string]any{
		"enableTelemetry":   false,
		"trustedWorkspaces": []string{wd},
		"permissions":       map[string]any{"allow": allow},
	}
	files := map[string]map[string]any{
		filepath.Join(cliDir, "settings.json"):   settings,
		filepath.Join(cfgDir, "config.json"):     {"permissions": map[string]any{"allow": allow}},
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

// agyChatAllowRules is the permissions.allow set for a chat's agy: the read-only
// file tools (knowledge dirs stay readable) plus `mcp(<server>/*)` per granted
// server. Everything else — command execution, writes — stays auto-denied by -p,
// which IS the chat contract. Rule syntax verified live (mcp(af) and bare tool
// names do NOT match; docs/log/32 headlessChat 節).
func agyChatAllowRules(c *chatConversation) []string {
	allow := []string{"read_file", "list_dir", "grep_search", "find_files", "codebase_search"}
	for name := range agyChatServers(c) {
		allow = append(allow, "mcp("+name+"/*)")
	}
	sort.Strings(allow[5:]) // deterministic file content across turns
	return allow
}

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
func agyChatServers(c *chatConversation) map[string]any {
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
// under a self-minted valid v4 and resumes it thereafter (実測 docs/log/40 §-p / probe 8)
// — so context carries across turns via the same self-UUID identity the TUI/managed
// routes use (cursor.go). Auth is ambient (~/.config/cursor/auth.json), so no token
// injection. cursor has no system-prompt flag, so persona/knowledge ride the
// headlessPrompt preamble.
//
// Tool posture: --mode ask ("Q&A style … read-only", cursor's own execution mode —
// 実測 v2026.07.20). This is BOTH the chat contract (no host mutation: the model can
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
// The terminal `result.usage` is the ONLY cursor route carrying tokens (docs/log/40 §使用量
// — ACP/JSONL have none), so it feeds the context-fill snapshot. Fields are additive
// (fresh input + cache read + cache write), same shape as opencode.
type cursorChat struct{}

// cursorResult is `cursor-agent -p --output-format json`'s single result object
// (実測 docs/log/40 probe 9): {"type":"result","subtype":"success","is_error":…,
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

func (cursorChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	c.startTurn()
	// 使用量台帳（ADR 0029 §3）。cursor は result にモデルを載せない（実測）ので requested
	// 止まり。"auto" は --model を渡さない＝解決後のモデル不明なので default_unknown。
	call := usageCall{Kind: session.KindCursor}
	pinned := chatModelFor(c, session.KindCursor) // ピン留めが別 kind ならこの CLI の設定から解決
	if pinned != "" && pinned != "auto" {
		call.ModelReq = pinned
	}
	defer recordUsageCall(ctx, &call, time.Now())
	args := cursorChatBaseArgs()
	if m := pinned; m != "" && m != "auto" { // "auto" = cursor's default = no --model
		args = append(args, "--model", m)
	}
	if c.CursorSessionID == "" {
		c.CursorSessionID = randUUID() // --resume with a fresh valid v4 creates the chat
	}
	args = append(args, "--resume", c.CursorSessionID)
	cmd := exec.CommandContext(ctx, cursor.Bin(), args...)
	cmd.Dir = chatWorkdir()
	cmd.Env = cursor.EnvWithoutCI(os.Environ()) // CI を渡さない（cursor/ci_env.go）
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
	// 台帳の ModelReq と同じ基準: --model を渡した時だけ記録し、auto/未指定は空
	// （cursor 側で解決されたモデルは result に出ないので推測しない）。
	c.noteTurnModel(call.ModelReq)
	setChatContext(c, r.Usage.InputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens, 0,
		chatCtxModelFor(c, session.KindCursor))
	return reply, nil
}

// cursorChatBaseArgs is the shared argv prefix for a cursor headless turn:
// --disable-auto-update (fleet pins the version via image rebuild) precedes the
// subcommand as a root option (実測: it is rejected after -p), then print-mode JSON,
// --trust (skip the workspace-trust prompt), and --mode ask (read-only Q&A — the
// tool posture that keeps a chat turn from mutating the shared host; see cursorChat).
func cursorChatBaseArgs() []string {
	return []string{"--disable-auto-update", "-p", "--output-format", "json", "--trust", "--mode", "ask"}
}

// parseCursorResult decodes the -p result object. --output-format json emits one
// object, but cursor "異常時は整形 JSON なしで終わり得る" (docs/log/40) and may prefix
// stray lines, so fall back to scanning for the last line that parses as a result.
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
// candidate). Three deliberate departures from a chat turn (docs/log/46 §1-a-2, 実測
// 2026-07-25) — a one-shot is a pure classification-and-format task, so everything that
// makes claude a coding agent is dead weight paid on every call:
//
//	--system-prompt … REPLACES the default system prompt instead of appending to it
//	                  (--append-system-prompt left the whole Claude Code persona on).
//	--tools ""      … loads no built-in tool definitions at all (they were still being
//	                  sent even though the useful ones were disallowed).
//
// plus MAX_THINKING_TOKENS=0 (claudeOneShotEnv) — an 18-character title was costing
// ~500 thinking tokens. Measured on the title prompt: 入力側 16.0k→4.3k / out 533→13 /
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

// recommendedUtilityModel picks the cheap model shown as 「推奨（現在: …）」 in
// AssistantTab. The OpenCode Go route is pinned only when the live account catalog
// proves it is available; otherwise an empty result deliberately delegates to the
// CLI default rather than risking a metered/unentitled Zen model.
func recommendedUtilityModel(kind string) string {
	// 「使わないモデル」（model_deny.go）で除外された候補は自動選択もしない。
	switch kind {
	case session.KindClaude:
		return visibleModel(kind, "haiku")
	case session.KindCodex:
		return cheapOneShotModel(visibleModelIDs(kind, modelChoiceIDs(codex.Models())))
	case session.KindOpencode:
		const goModel = "opencode-go/deepseek-v4-flash"
		return recommendedCatalogModel(visibleModelIDs(kind, opencode.Models()), goModel, "")
	case session.KindAgy:
		return visibleModel(kind, defaultAgyChatModel)
	}
	return ""
}

// codexOneShotArgs is the argv for a codex one-shot. --ephemeral: a one-shot never
// needs resume, so don't persist a thread even into the chat-only CODEX_HOME.
//
// The two savings knobs (docs/log/46 §1-a-2 / §2-b), mirroring what the claude path does:
//   - -m <cheap model>: without it codex ran throwaway calls on whatever config.toml
//     pins — on a real workspace gpt-5.6-luna. AF_TITLE_MODEL_CODEX still wins, and an
//     empty pick (unknown catalog) falls back to today's "no -m" behaviour.
//   - -c model_reasoning_effort="low": the analog of MAX_THINKING_TOKENS=0. A title is
//     not a reasoning problem, and the user's configured effort (often "high") would
//     otherwise apply to every one-shot. "low" is supported by every listed model.
//
// The trailing "-" makes codex read the prompt from stdin; it must stay last.
func codexOneShotArgs() (args []string, autoPicked bool) {
	return codexOneShotArgsFor("")
}

func codexOneShotArgsFor(selected string) (args []string, autoPicked bool) {
	args = []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--color", "never", "-C", chatWorkdir()}
	if selected != "" {
		args = append(args, "-m", selected)
	} else if m := os.Getenv("AF_TITLE_MODEL_CODEX"); m != "" {
		args = append(args, "-m", m) // explicit user choice: never second-guess it
	} else if m := cheapOneShotModel(visibleModelIDs(session.KindCodex, modelChoiceIDs(codex.Models()))); m != "" {
		args, autoPicked = append(args, "-m", m), true
	}
	return append(args, "-c", `model_reasoning_effort="low"`, "-"), autoPicked
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
// usage も返すのは、一発呼び出しを台帳に載せるため（ADR 0029 §3）— 失敗して
// リトライした場合は「2回撃った」ことが2行として残るのが正しい。
func runCodexOneShot(ctx context.Context, args []string, prompt string) (string, codexUsage, error) {
	cmd := chatCodexCmd(ctx, nil, args...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", codexUsage{}, fmt.Errorf("codex execution failed: %s", cliErr(err))
	}
	reply, _, execErr, usage := parseCodexExecEvents(out)
	if execErr != "" {
		return "", usage, errors.New(execErr)
	}
	return reply, usage, nil
}

// codexOneShotRun は runCodexOneShot の形（テストで差し替える）。
type codexOneShotRun func(ctx context.Context, args []string, prompt string) (string, codexUsage, error)

// codexOneShotWithRetry は自前ピクの小型モデルで撃ち、失敗したらモデルを外して1度だけ
// 撃ち直す。**2回分のトークンを合算して返す**のが台帳側の要点。
//
// The cheap model came from the catalog, and a catalog entry is not proof the account may
// run it (実測: opencode lists claude-haiku-4-5 and then 500s on it). Falling back to the
// configured default once — a title that costs more still beats a title feature that is broken.
//
// 1回目の消費は失敗しても発生している。リトライ結果で上書きすると、実際に撃った分が台帳から
// 消える — 「安いモデルで失敗 → 既定のフラッグシップで撃ち直し」は**最も高くつく経路**なので、
// そこが1回分に見えるのが一番まずい。要求値（model_req）は「モデルを外して撃ち直した」ことが
// 分かるようリトライ側で上書きする（行は1本なので、後勝ちで実際に答えを出した方を載せる）。
func codexOneShotWithRetry(ctx context.Context, args []string, autoPicked bool, prompt string,
	run codexOneShotRun) (reply string, tok usageTokens, modelReq string, err error) {
	modelReq = argValue(args, "-m")
	reply, usage, err := run(ctx, args, prompt)
	tok = usage.ledgerTokens()
	if err != nil && autoPicked {
		reply, usage, err = run(ctx, codexOneShotArgsNoModel(args), prompt)
		tok, modelReq = tok.Add(usage.ledgerTokens()), ""
	}
	return reply, tok, modelReq, err
}

// oneShotHeadless runs one prompt through the preferred available backend and returns
// the reply text — the backend-agnostic core of the title/branch suggestions. persona
// is passed natively where possible (claude --system-prompt) and as a prompt preamble
// otherwise. claudeModel applies to the claude backend only (codex/opencode run their
// own configured defaults; override via AF_TITLE_MODEL_CODEX/_OPENCODE — docs/log/46 §2-b
// flags that an unset override means the CLI's own default, usually the flagship).
func oneShotHeadless(ctx context.Context, persona, prompt, claudeModel string) (string, error) {
	// 使用量台帳（ADR 0029 §3）。この関数は claude → codex → opencode → cursor → agy の
	// うち「最初に使えるもの」を選ぶので、kind は分岐の中で実行結果として埋める — 要求値を
	// 書くと claude-less ワークスペースの消費が全部 claude に化ける（docs/log/46 §2）。
	// oneShotHeadless の戻り値を広げず内部で記録するのは、記録点がここの内側にある以上、
	// 呼び出し側4箇所を触る理由がないため。
	call := usageCall{}
	defer recordUsageCall(ctx, &call, time.Now())
	kind := preferredHeadlessAgent()
	selected, configured := assistantUtilityModelPref(kind)
	selected = strings.TrimSpace(selected)
	autoRecommended := selected == assistantRecommendedModel
	if selected == assistantRecommendedModel {
		selected, configured = recommendedUtilityModel(kind), true
	}
	switch kind {
	case session.KindCodex:
		call.Kind = session.KindCodex
		defer func() { _, _ = chatCodexHome() }()
		full := headlessPrompt(persona, nil, prompt)
		if !configured && os.Getenv("AF_TITLE_MODEL_CODEX") == "" {
			selected = recommendedUtilityModel(kind)
			autoRecommended = selected != ""
		}
		args, autoPicked := codexOneShotArgsFor(selected)
		autoPicked = autoPicked || autoRecommended
		reply, tok, modelReq, err := codexOneShotWithRetry(ctx, args, autoPicked, full, runCodexOneShot)
		call.ModelReq, call.Totals, call.OK = modelReq, tok, err == nil
		return reply, err
	case session.KindOpencode:
		call.Kind = session.KindOpencode
		args := []string{"run", "--format", "json", "--dir", chatWorkdir()}
		env := opencode.Env()
		// read-only 姿勢（docs/log/44 §1.3）: chat と違い one-shot は素の chatWorkdir で走り
		// project config（opencode.json）が無いため、OPENCODE_CONFIG の deny が実効になる
		// （実測 1.18.7 の併合優先度: project > OPENCODE_CONFIG > global — opencodeChatDir 参照）。
		// 書けなかったときは従来どおり素で走る（title/reply を壊さない）— ホーム破損時の縮退。
		if cfg := opencodeOneShotConfig(); cfg != "" {
			env = append(env, "OPENCODE_CONFIG="+cfg)
		}
		// NOTE: deliberately NOT auto-picking a cheap model here (docs/log/46 §1-a-2). opencode's
		// catalog is a LISTING, not an entitlement: `opencode/claude-haiku-4-5` is listed and
		// selectable, yet running it returns "Unexpected server error" on an account without
		// it, while the configured default answers fine (実測 2026-07-25). A hard failure of
		// title/branch/reply suggestions is worse than their token cost, so opencode keeps the
		// user's default unless AF_TITLE_MODEL_OPENCODE names something explicitly.
		m := selected
		if !configured {
			m = os.Getenv("AF_TITLE_MODEL_OPENCODE")
			if m == "" {
				m = recommendedUtilityModel(kind)
			}
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
		reply, sesID, model, turnErr, usage := parseOpencodeRunEvents(out)
		if err != nil {
			if turnErr != "" {
				return "", fmt.Errorf("opencode turn failed: %s", turnErr)
			}
			return "", fmt.Errorf("opencode execution failed: %s", cliErr(err))
		}
		call.Totals, call.OK = usage.ledgerTokens(), true
		if model != "" {
			call.Models = []usageModelRow{{Model: model, ModelRaw: model, Tokens: call.Totals}}
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
		return reply, nil
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
				m = recommendedUtilityModel(kind)
			}
		}
		if m != "" {
			args = append(args, "--model", m)
			call.ModelReq = m
		}
		cmd := exec.CommandContext(ctx, cursor.Bin(), args...)
		cmd.Dir = chatWorkdir()
		cmd.Env = cursor.EnvWithoutCI(os.Environ()) // CI を渡さない（cursor/ci_env.go）
		cmd.Stdin = strings.NewReader(headlessPrompt(persona, nil, prompt))
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("cursor execution failed: %s", cliErr(err))
		}
		r, perr := parseCursorResult(out)
		if perr != nil {
			return "", perr
		}
		call.SetTotals(r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
		if r.IsError {
			return "", errors.New(strings.TrimSpace(r.Result))
		}
		call.OK = true
		return strings.TrimRight(strings.TrimSpace(r.Result), "\n"), nil
	case session.KindAgy:
		// agy has no ephemeral mode, so each one-shot leaves a throwaway conversation
		// behind — contained in the shared "oneshot" isolated home rather than the
		// user's real agy state (which would also spawn their own global MCP servers
		// on every title call). Runs on the cheap Flash default (quota-scarce free
		// plan) unless overridden.
		// agy は素のテキスト出力なのでトークンが一切取れない — measured=none で回数だけ数える。
		call.Kind, call.Measured = session.KindAgy, usageMeasuredNone
		home, wdir, err := chatAgyHome(&chatConversation{ID: "oneshot"})
		if err != nil {
			return "", fmt.Errorf("agy chat home: %v", err)
		}
		var args []string
		m := selected
		if !configured {
			m = envOr("AF_TITLE_MODEL_AGY", defaultAgyChatModel)
		}
		if m := agyChatModel(m, filterVisibleModels(session.KindAgy, agy.Models())); m != "" {
			args = append(args, "--model", m)
			call.ModelReq = m
		}
		args = append(args, "-p", headlessPrompt(persona, nil, prompt))
		cmd := exec.CommandContext(ctx, "agy", args...)
		cmd.Dir = wdir
		cmd.Env = envWith("HOME=" + home)
		defer reconcileChatCreds(agy.TokenPath(), filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("agy execution failed: %s", cliErr(err))
		}
		call.OK = true
		return strings.TrimRight(strings.TrimSpace(string(out)), "\n"), nil
	}
	// claude (default): the historical path, kept native. --no-session-persistence is
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
	// claude だけはモデル別の実測内訳とコストが返る（docs/log/46 §0）— エラー result でも
	// 課金は発生しているので、OK 判定より先に採る。exec エラー（停止・非ゼロ終了）でも
	// claude は構造化 result を吐いていることがあるので、**エラー return より先に**解析する。
	call.Models, call.CostUSD = usageModelRows(r.ModelUsage), r.TotalCostUSD
	call.FallbackTotals(r.Usage.ledgerTokens(), "") // modelUsage の無い応答の縮退
	if err != nil {
		return "", fmt.Errorf("claude execution failed: %s", cliErr(err))
	}
	if perr != nil || r.IsError {
		return "", errors.New("claude returned an invalid response/error")
	}
	call.OK = true
	return strings.TrimRight(r.Result, "\n"), nil
}

// argValue は argv 中のフラグに続く値を返す（見つからなければ ""）。台帳の model_req に
// 「結局どのモデルを要求したか」を、引数を組み立てた側の分岐を二重に書かずに載せるため。
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
	d := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd")
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
	return filepath.Join(homeDir(), ".config", "agent-fleet", "chat-claude")
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
// ListAgents / SendMessage は claude 自前の cross-session チャネル（docs/log/58 §58.17）。
// ヘッドレスの `-p` はソケットを bind しない＝**受信はできないが送信はできる**ので、
// 塞がないとアシスタントがワークスペース内のセッションへ AF の外から打ち込める。
// オペレーター面の投入は af MCP の send_to_session が持っていて、そちらは台帳を通る。
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
