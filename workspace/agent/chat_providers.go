package main

// アシスタントチャットのプロバイダ実装（claude/codex/opencode/agy の headless CLI
// 駆動）と CLI 起動まわりの下回り。chat.go からの機械的分割（docs/23 残②）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
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
}

// --- backend availability (claude-less workspaces, docs/19) ----------------------
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
		v = opencode.Available()
	case session.KindAgy:
		v = agy.SignedIn()
	}
	headlessAvailMu.Lock()
	headlessAvailAt[kind], headlessAvail[kind] = time.Now(), v
	headlessAvailMu.Unlock()
	return v
}

// preferredHeadlessAgent picks the backend for new builtin-assistant conversations
// and one-shot calls. The user's explicit choice (設定 > エージェント「アシスタントの
// エージェント」, ui-prefs assistantAgent) wins while that CLI is usable; otherwise —
// auto, unset, or the pinned CLI not connected — the first authenticated of
// claude → codex → opencode → agy. agy sits last on purpose: its Starter/free
// quota is tiny (docs/32 Track D), so auto-selection reaches it only in an
// agy-only workspace; using it otherwise is an explicit pin. Falls back to
// claude when nothing is connected (the call then surfaces a clear error).
func preferredHeadlessAgent() string {
	if pin := assistantAgentPref(); pin != "" && headlessAgentAvailable(pin) {
		return pin
	}
	for _, k := range []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindAgy} {
		if headlessAgentAvailable(k) {
			return k
		}
	}
	return session.KindClaude
}

// chatProviderFor resolves the provider driving this conversation: the pinned agent
// while its CLI is authenticated, else the preferred available backend — so a
// claude-less (codex-only / opencode-only) workspace still gets working assistants.
// Each provider keeps its own resume handle on the conversation, so a later switch
// back resumes cleanly.
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
// metadata. Production providers are the four value types below.
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
}

func (claudeChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModel(c))
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
	if r.SessionID != "" {
		c.ClaudeSessionID = r.SessionID
	}
	if r.IsError {
		return "", fmt.Errorf("claude returned an error: %s", r.Result)
	}
	if err != nil {
		return "", fmt.Errorf("claude execution failed: %s", cliErr(err))
	}
	t := claudeCtx{model: chatModel(c)}
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
// working steps (docs/19). A provider that doesn't implement it falls back to send() (one
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
	// Usage/ModelUsage ride the final "result" line (same shape as claudeResult).
	Usage      claudeUsage                 `json:"usage"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
	Event      struct {
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
	// stream-json requires --verbose with -p; --include-partial-messages adds the
	// per-token text_delta events we forward for live display.
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--dangerously-skip-permissions", "--append-system-prompt", c.personaOf()}
	args = append(args, "--model", chatModel(c))
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

	// Split the run into working steps and a final answer (docs/19). claude emits one
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
	ctxTrack := claudeCtx{model: chatModel(c)} // context-fill tracker (chat_usage.go)
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
				}
			}
		}
		if rerr != nil {
			break // EOF or read error — the process is done streaming
		}
	}
	waitErr := cmd.Wait()
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
	// The default read-only sandbox is exactly the chat contract (no file writes, no
	// state mutation) — the claude side enforces the same via --disallowedTools. Global
	// exec flags must precede the resume subcommand (verified live: resume rejects
	// --color/-C placed after it).
	// Headless chat has no approval UI. Explicitly decline escalation while keeping
	// shell commands in the read-only sandbox: MCP calls (including af_write) can run,
	// but the model still cannot mutate the workspace through shell/file tools.
	args := codexChatBaseArgs()
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	if c.afToolsEnabled() {
		args = append(args, codexMCPArgs(c.afWriteEnabled(), c.ID)...)
	}
	if c.CodexSessionID != "" {
		args = append(args, "resume", c.CodexSessionID)
	}
	args = append(args, "-") // read the prompt from stdin (personas can exceed argv comfort)
	cmd := chatCodexCmd(ctx, args...)
	defer func() { _, _ = chatCodexHome() }() // fold a rotated token back to shared (see chatCodexHome)
	cmd.Stdin = strings.NewReader(headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex execution failed: %s", cliErr(err))
	}
	reply, threadID, execErr, usage := parseCodexExecEvents(out)
	if threadID != "" {
		c.CodexSessionID = threadID
	}
	if execErr != "" {
		return "", fmt.Errorf("codex returned an error: %s", execErr)
	}
	if reply == "" {
		return "", errors.New("no response from codex")
	}
	// codex の input_tokens は cached を含む（chat_usage.go）: fresh = input - cached。
	setChatContext(c, usage.InputTokens-usage.CachedInputTokens, usage.CachedInputTokens,
		0, 0, chatCtxModelFor(c))
	return reply, nil
}

func codexChatBaseArgs() []string {
	return []string{"-a", "never", "-s", "read-only", "exec", "--json", "--skip-git-repo-check", "--color", "never", "-C", chatWorkdir()}
}

// chatCodexHome prepares the chat-only CODEX_HOME — the codex analog of claude's
// chat-only CLAUDE_CONFIG_DIR (docs/19 Q3): an isolated sessions/config tree so the
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
// the chat-only CODEX_HOME (shared login). Falls back to the inherited env (the real
// ~/.codex) if the isolated home can't be prepared.
func chatCodexCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = chatWorkdir()
	if home, err := chatCodexHome(); err == nil {
		cmd.Env = envWith("CODEX_HOME=" + home)
	}
	return cmd
}

// codexMCPArgs attaches the local Agent Fleet stdio MCP server to a codex exec via
// -c config overrides (codex's mcp_servers table) — the codex analog of chatMCPArgs.
// convID rides along for write grants (docs/30 report_to auto-attach; see mcpConfigArgs).
func codexMCPArgs(write bool, convID string) []string {
	serverArgs := `["mcp-stdio"]`
	if write {
		serverArgs = `["mcp-stdio","--write","--conv",` + tomlString(convID) + `]`
	}
	return []string{
		"-c", "mcp_servers.af.command=" + tomlString(paths.ExePath()),
		"-c", "mcp_servers.af.args=" + serverArgs,
		// Codex has a distinct MCP approval layer. Headless exec has no UI to answer
		// it, so the default policy reports "user cancelled MCP tool call" unless the
		// explicitly granted Agent Fleet server is pre-approved.
		"-c", "mcp_servers.af.default_tools_approval_mode=\"approve\"",
	}
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
	dir := opencodeChatDir(c)
	args := []string{"run", "--format", "json", "--dir", dir}
	if c.OpencodeSessionID != "" {
		args = append(args, "--session", c.OpencodeSessionID)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	cmd := exec.CommandContext(ctx, "opencode", args...)
	cmd.Dir = dir
	cmd.Env = envWith(opencode.Env()...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("opencode execution failed: %s", cliErr(err))
	}
	reply, sesID, usage := parseOpencodeRunEvents(out)
	if sesID != "" {
		c.OpencodeSessionID = sesID
	}
	if reply == "" {
		return "", errors.New("no response from opencode")
	}
	setChatContext(c, usage.Input, usage.Cache.Read, usage.Cache.Write, 0, chatCtxModelFor(c))
	return reply, nil
}

// opencodeChatDir prepares the per-tool-grant working dir for a chat's opencode run,
// with an opencode.json carrying the chat policy: file edits and shell denied (the
// opencode analog of chatToolLimits), plus — for af grants — the local Agent Fleet MCP
// server. Grants get separate dirs (none/read/write) because the config is per-dir and
// conversations with different grants run concurrently. Falls back to the shared
// chat workdir when the dir can't be prepared (opencode then runs with its defaults).
func opencodeChatDir(c *chatConversation) string {
	grant := "none"
	if c.afToolsEnabled() {
		grant = "read"
		if c.afWriteEnabled() {
			grant = "write"
		}
	}
	dir := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "opencode-"+grant)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return chatWorkdir()
	}
	cfg := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"permission": map[string]any{"edit": "deny", "bash": "deny"},
	}
	if grant != "none" {
		mcpArgs := []string{paths.ExePath(), "mcp-stdio"}
		if grant == "write" {
			mcpArgs = append(mcpArgs, "--write")
		}
		cfg["mcp"] = map[string]any{
			"af": map[string]any{"type": "local", "command": mcpArgs, "enabled": true},
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return chatWorkdir()
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), append(b, '\n'), 0o600); err != nil {
		return chatWorkdir()
	}
	return dir
}

// parseOpencodeRunEvents walks an opencode run --format json stream: the reply is the
// text parts in arrival order (deduped by part id — opencode may re-emit a part as it
// settles), every event carries the session id for resume, and step_finish parts carry
// the per-call tokens that feed the context-fill snapshot (chat_usage.go; last wins).
func parseOpencodeRunEvents(out []byte) (reply, sesID string, usage opencodeUsage) {
	texts := map[string]string{} // part id -> latest text
	var order []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				ID     string        `json:"id"`
				Type   string        `json:"type"`
				Text   string        `json:"text"`
				Tokens opencodeUsage `json:"tokens"`
			} `json:"part"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			sesID = ev.SessionID
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
	return strings.TrimRight(strings.Join(parts, "\n\n"), "\n"), sesID, usage
}

// agyChat runs `agy -p` (print mode, plain-text stdout — v1.1.4 has no structured
// output), resuming via `--conversation <UUID>`. The UUID is captured from agy's
// cwd→last-conversation map (cache/last_conversations.json), which a `-p` run
// writes on process exit (docs/32 Track D-3 — unlike the TUI, which flushes it
// only on graceful exit). agy has no system-prompt flag, so persona/knowledge ride
// the headlessPrompt preamble.
//
// Tools: agy's MCP config is GLOBAL-only (~/.gemini/config/mcp_config.json — no
// per-invocation flag like claude --mcp-config / codex -c), so every turn runs
// under a per-conversation isolated HOME (chatAgyHome) that shares ONLY the OAuth
// token with the user's real ~/.gemini. `-p` auto-denies tool prompts (docs/32
// D-5); the isolated home's permissions.allow re-opens exactly the chat contract:
// the read tools plus `mcp(<server>/*)` for each granted server (rule syntax
// reverse-engineered from the binary and live-verified 2026-07-20). Command/write
// tools stay auto-denied — no --dangerously-skip-permissions. No usage events, so
// the context gauge stays empty (Context = nil).
type agyChat struct{}

func (agyChat) send(ctx context.Context, c *chatConversation, prompt string) (string, error) {
	home, wd, err := chatAgyHome(c)
	if err != nil {
		// Never fall back to the real HOME: the MCP/permission config is global
		// there and would leak into the user's interactive agy sessions.
		return "", fmt.Errorf("agy chat home: %v", err)
	}
	args := agyChatArgs(c, headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
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
	return reply, nil
}

// agyChatArgs builds the argv for one agy chat turn: flags first, the prompt as
// `-p`'s value last (verified live v1.1.4 — `-p <prompt>` with the display-name
// --model and --conversation resume all honored).
func agyChatArgs(c *chatConversation, prompt string) []string {
	var args []string
	if c.Model != "" { // fetch the catalog only when there is a pin to validate
		if m := agyChatModel(c.Model, agy.Models()); m != "" {
			args = append(args, "--model", m)
		}
	}
	if c.AgyConversationID != "" {
		args = append(args, "--conversation", c.AgyConversationID)
	}
	return append(args, "-p", prompt)
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
//     shifted between builds, docs/32 D-5, and an extra copy is harmless).
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
// names do NOT match; docs/32 headlessChat 節).
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
// grant, plus one server per READY ops integration. env.HOME pins the spawned
// servers back to the real home (they inherit agy's isolated-HOME env otherwise).
func agyChatServers(c *chatConversation) map[string]any {
	exe := agyChatExe()
	env := map[string]any{"HOME": homeDir()}
	servers := map[string]any{}
	if c.afToolsEnabled() {
		sargs := []any{"mcp-stdio"}
		if c.afWriteEnabled() {
			sargs = []any{"mcp-stdio", "--write", "--conv", c.ID}
		}
		servers["af"] = map[string]any{"command": exe, "args": sargs, "env": env}
	}
	for _, id := range c.Integrations {
		reg, ok := opsIntegrations[id]
		if !ok || !integrationReady(id) {
			continue // unknown, or the user hasn't connected it — skip silently
		}
		sargs := make([]any, len(reg.runArgs))
		for i, a := range reg.runArgs {
			sargs[i] = a
		}
		servers[id] = map[string]any{"command": exe, "args": sargs, "env": env}
	}
	return servers
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

// oneShotHeadless runs one prompt through the preferred available backend and returns
// the reply text — the backend-agnostic core of the title/branch suggestions. persona
// is passed natively where possible (claude --append-system-prompt) and as a prompt
// preamble otherwise. claudeModel applies to the claude backend only (codex/opencode
// run their own configured defaults; override via AF_TITLE_MODEL_CODEX/_OPENCODE).
func oneShotHeadless(ctx context.Context, persona, prompt, claudeModel string) (string, error) {
	switch preferredHeadlessAgent() {
	case session.KindCodex:
		// --ephemeral: a one-shot never needs resume, so don't persist a thread even
		// into the chat-only CODEX_HOME.
		args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--color", "never", "-C", chatWorkdir()}
		if m := os.Getenv("AF_TITLE_MODEL_CODEX"); m != "" {
			args = append(args, "-m", m)
		}
		args = append(args, "-")
		cmd := chatCodexCmd(ctx, args...)
		defer func() { _, _ = chatCodexHome() }()
		cmd.Stdin = strings.NewReader(headlessPrompt(persona, nil, prompt))
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("codex execution failed: %s", cliErr(err))
		}
		reply, _, execErr, _ := parseCodexExecEvents(out)
		if execErr != "" {
			return "", errors.New(execErr)
		}
		return reply, nil
	case session.KindOpencode:
		args := []string{"run", "--format", "json", "--dir", chatWorkdir()}
		if m := os.Getenv("AF_TITLE_MODEL_OPENCODE"); m != "" {
			args = append(args, "--model", m)
		}
		args = append(args, headlessPrompt(persona, nil, prompt))
		cmd := exec.CommandContext(ctx, "opencode", args...)
		cmd.Dir = chatWorkdir()
		cmd.Env = envWith(opencode.Env()...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("opencode execution failed: %s", cliErr(err))
		}
		reply, sesID, _ := parseOpencodeRunEvents(out)
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
	case session.KindAgy:
		// agy has no ephemeral mode, so each one-shot leaves a throwaway conversation
		// behind — contained in the shared "oneshot" isolated home rather than the
		// user's real agy state (which would also spawn their own global MCP servers
		// on every title call). Runs on the cheap Flash default (quota-scarce free
		// plan) unless overridden.
		home, wdir, err := chatAgyHome(&chatConversation{ID: "oneshot"})
		if err != nil {
			return "", fmt.Errorf("agy chat home: %v", err)
		}
		var args []string
		if m := agyChatModel(envOr("AF_TITLE_MODEL_AGY", defaultAgyChatModel), agy.Models()); m != "" {
			args = append(args, "--model", m)
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
		return strings.TrimRight(strings.TrimSpace(string(out)), "\n"), nil
	}
	// claude (default): the historical path, kept native. --no-session-persistence is
	// claude's --ephemeral analog (print-mode only, no transcript written, no resume):
	// a one-shot never resumes, so don't pile per-call jsonl into Claude's projects tree.
	args := []string{"-p", "--no-session-persistence", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", persona, "--model", claudeModel}
	args = append(args, chatToolLimits()...)
	cmd := chatClaudeCmd(ctx, args...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude execution failed: %s", cliErr(err))
	}
	var r claudeResult
	if json.Unmarshal(out, &r) != nil || r.IsError {
		return "", errors.New("claude returned an invalid response/error")
	}
	return strings.TrimRight(r.Result, "\n"), nil
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
func chatToolLimits() []string {
	return []string{"--disallowedTools",
		"Agent", "Task", "Workflow",
		"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit"}
}

// mcpConfigArgs builds the chat's --mcp-config from two orthogonal sources, scoped
// strictly to this claude (--strict-mcp-config: no global/project MCP leaks in, and
// none of these leak out to the interactive sessions). docs/19 Q1, docs/25 Phase 1.
//   - the local Agent Fleet stdio server ("af"), when the assistant grants af tools;
//     with af_write it also advertises the write tools (the advertised set is the gate).
//   - one server per ops integration ("pagerduty" …) the assistant holds, launched via
//     `workspace-agent mcp-run <id>` which injects the user's stored key at spawn — so a
//     server is attached only when that connection is actually configured.
func (c *chatConversation) mcpConfigArgs() []string {
	exe := paths.ExePath()
	servers := map[string]any{}
	if c.afToolsEnabled() {
		sargs := []any{"mcp-stdio"}
		if c.afWriteEnabled() {
			// --conv hands the server its owning conversation id so create_session /
			// send_to_session auto-attach report_to (docs/30) — the report link is
			// tool-side plumbing, never something the model has to carry.
			sargs = []any{"mcp-stdio", "--write", "--conv", c.ID}
		}
		servers["af"] = map[string]any{"command": exe, "args": sargs}
	}
	for _, id := range c.Integrations {
		reg, ok := opsIntegrations[id]
		if !ok || !integrationReady(id) {
			continue // unknown, or the user hasn't connected it — skip silently
		}
		sargs := make([]any, len(reg.runArgs))
		for i, a := range reg.runArgs {
			sargs[i] = a
		}
		servers[id] = map[string]any{"command": exe, "args": sargs}
	}
	if len(servers) == 0 {
		return nil
	}
	cfg, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(cfg), "--strict-mcp-config"}
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
