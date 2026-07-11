package main

// アシスタントチャットのプロバイダ実装（claude/codex の headless CLI 駆動）と
// CLI 起動まわりの下回り。chat.go からの機械的分割（docs/23 残②）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	}
	headlessAvailMu.Lock()
	headlessAvailAt[kind], headlessAvail[kind] = time.Now(), v
	headlessAvailMu.Unlock()
	return v
}

// preferredHeadlessAgent picks the backend for new builtin-assistant conversations
// and one-shot calls: the first authenticated of claude → codex → opencode. Falls
// back to claude when nothing is connected (the call then surfaces a clear error).
func preferredHeadlessAgent() string {
	for _, k := range []string{session.KindClaude, session.KindCodex, session.KindOpencode} {
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

// claudeChat runs `claude -p` (headless), pinning a session id on the first turn
// and resuming it thereafter so context carries across turns. Auth is the
// container's existing CLAUDE_CODE_OAUTH_TOKEN / CLAUDE_CONFIG_DIR (subscription).
type claudeChat struct{}

type claudeResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
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
	if c.afToolsEnabled() {
		args = append(args, chatMCPArgs(c.afWriteEnabled())...)
	}
	cmd := chatClaudeCmd(ctx, args...)
	// claude writes .credentials.json via tmp+rename (verified with strace): a refresh
	// during this run replaces the symlink with a real file. Re-run the reconcile after
	// the process exits to copy the rotated token back to shared and relink immediately,
	// so the shared login (used by the interactive sessions) never goes stale.
	defer func() { _, _ = ensureChatClaudeConfig() }()
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude 実行に失敗しました: %s", cliErr(err))
	}
	var r claudeResult
	if err := json.Unmarshal(out, &r); err != nil {
		return "", fmt.Errorf("claude 応答の解析に失敗しました: %v", err)
	}
	if r.SessionID != "" {
		c.ClaudeSessionID = r.SessionID
	}
	if r.IsError {
		return "", fmt.Errorf("claude がエラーを返しました: %s", r.Result)
	}
	return strings.TrimRight(r.Result, "\n"), nil
}

// streamingProvider is the optional token-streaming variant of chatProvider. emit
// is called with each incremental text delta; the returned string is the full reply.
// A provider that doesn't implement it falls back to send() (one emit of the whole
// result) in handleChatStream, so every agent works through the stream endpoint.
type streamingProvider interface {
	sendStream(ctx context.Context, c *chatConversation, prompt string, emit func(delta string)) (string, error)
}

// streamLine is one JSONL event from `claude --output-format stream-json`. We read
// the incremental text_delta events (with --include-partial-messages) for live
// display, capture the session id for resume, and take the final `result` as the
// authoritative reply text.
type streamLine struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Event     struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

func (claudeChat) sendStream(ctx context.Context, c *chatConversation, prompt string, emit func(string)) (string, error) {
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
	if c.afToolsEnabled() {
		args = append(args, chatMCPArgs(c.afWriteEnabled())...)
	}
	cmd := chatClaudeCmd(ctx, args...)
	defer func() { _, _ = ensureChatClaudeConfig() }() // copy any refreshed token back to shared (see send)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("claude 起動に失敗しました: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude 起動に失敗しました: %v", err)
	}

	var acc strings.Builder // accumulated deltas (fallback if result is empty)
	var result string       // authoritative final text from the result event
	var resultErr bool
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
					if sl.Event.Type == "content_block_delta" &&
						sl.Event.Delta.Type == "text_delta" && sl.Event.Delta.Text != "" {
						acc.WriteString(sl.Event.Delta.Text)
						emit(sl.Event.Delta.Text)
					}
				case "result":
					result = sl.Result
					resultErr = sl.IsError
				}
			}
		}
		if rerr != nil {
			break // EOF or read error — the process is done streaming
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("claude 実行に失敗しました: %s", stderrOr(err, &stderr))
	}
	if resultErr {
		return "", fmt.Errorf("claude がエラーを返しました: %s", result)
	}
	final := result
	if final == "" {
		final = acc.String()
	}
	return strings.TrimRight(final, "\n"), nil
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
	args := []string{"exec", "--json", "--skip-git-repo-check", "--color", "never", "-C", chatWorkdir()}
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	if c.afToolsEnabled() {
		args = append(args, codexMCPArgs(c.afWriteEnabled())...)
	}
	if c.CodexSessionID != "" {
		args = append(args, "resume", c.CodexSessionID)
	}
	args = append(args, "-") // read the prompt from stdin (personas can exceed argv comfort)
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = chatWorkdir()
	cmd.Stdin = strings.NewReader(headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex 実行に失敗しました: %s", cliErr(err))
	}
	reply, threadID, execErr := parseCodexExecEvents(out)
	if threadID != "" {
		c.CodexSessionID = threadID
	}
	if execErr != "" {
		return "", fmt.Errorf("codex がエラーを返しました: %s", execErr)
	}
	if reply == "" {
		return "", errors.New("codex から応答が得られませんでした")
	}
	return reply, nil
}

// codexMCPArgs attaches the local Agent Fleet stdio MCP server to a codex exec via
// -c config overrides (codex's mcp_servers table) — the codex analog of chatMCPArgs.
func codexMCPArgs(write bool) []string {
	serverArgs := `["mcp-stdio"]`
	if write {
		serverArgs = `["mcp-stdio","--write"]`
	}
	return []string{
		"-c", "mcp_servers.af.command=" + tomlString(paths.ExePath()),
		"-c", "mcp_servers.af.args=" + serverArgs,
	}
}

// parseCodexExecEvents walks a codex exec --json JSONL stream: the reply is the
// agent_message items joined (normally one), the thread id comes from thread.started,
// and a turn.failed / error event surfaces as execErr.
func parseCodexExecEvents(out []byte) (reply, threadID, execErr string) {
	var texts []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
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
	return strings.TrimRight(strings.Join(texts, "\n\n"), "\n"), threadID, execErr
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
		return "", fmt.Errorf("opencode 実行に失敗しました: %s", cliErr(err))
	}
	reply, sesID := parseOpencodeRunEvents(out)
	if sesID != "" {
		c.OpencodeSessionID = sesID
	}
	if reply == "" {
		return "", errors.New("opencode から応答が得られませんでした")
	}
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
// settles), and every event carries the session id for resume.
func parseOpencodeRunEvents(out []byte) (reply, sesID string) {
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
				ID   string `json:"id"`
				Type string `json:"type"`
				Text string `json:"text"`
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
	}
	var parts []string
	for _, id := range order {
		parts = append(parts, texts[id])
	}
	return strings.TrimRight(strings.Join(parts, "\n\n"), "\n"), sesID
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
		args := []string{"exec", "--json", "--skip-git-repo-check", "--color", "never", "-C", chatWorkdir()}
		if m := os.Getenv("AF_TITLE_MODEL_CODEX"); m != "" {
			args = append(args, "-m", m)
		}
		args = append(args, "-")
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Dir = chatWorkdir()
		cmd.Stdin = strings.NewReader(headlessPrompt(persona, nil, prompt))
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("codex 実行に失敗しました: %s", cliErr(err))
		}
		reply, _, execErr := parseCodexExecEvents(out)
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
			return "", fmt.Errorf("opencode 実行に失敗しました: %s", cliErr(err))
		}
		reply, _ := parseOpencodeRunEvents(out)
		return reply, nil
	}
	// claude (default): the historical path, kept native.
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", persona, "--model", claudeModel}
	args = append(args, chatToolLimits()...)
	cmd := chatClaudeCmd(ctx, args...)
	defer func() { _, _ = ensureChatClaudeConfig() }()
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude 実行に失敗しました: %s", cliErr(err))
	}
	var r claudeResult
	if json.Unmarshal(out, &r) != nil || r.IsError {
		return "", errors.New("claude が不正な応答/エラーを返しました")
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

// chatClaudeDir is the chat-only CLAUDE_CONFIG_DIR (docs/19 Q3): an isolated
// settings/trust/transcript tree so the headless chat's `claude -p` does NOT inherit
// the interactive tmux sessions' status hooks, does not clutter their projects/
// transcript tree, and can carry its own MCP config — while sharing ONLY the
// subscription credentials with them (see ensureChatClaudeConfig).
func chatClaudeDir() string {
	if v := os.Getenv("AF_CHAT_CLAUDE_DIR"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".config", "agent-fleet", "chat-claude")
}

// ensureChatClaudeConfig prepares chatClaudeDir and shares the subscription login by
// symlinking its .credentials.json to the interactive sessions' shared config, then
// returns the dir. Credentials must be a SINGLE shared file: OAuth refresh rotates
// the refresh token, so two independent copies would race and one side would lose
// auth.
//
// claude writes its JSON state (incl. creds) via tmp-file + rename (verified with
// strace: `.claude.json.tmp.* → rename(.claude.json)`). That means:
//   - an interactive session / agent re-auth renames the SHARED file → our symlink is
//     path-based, so it transparently follows to the fresh file. No action needed.
//   - the chat's OWN refresh renames the LINK path → the symlink becomes a real file
//     holding the rotated token, diverging from shared. reconcileChatCreds copies the
//     newer token back to shared and relinks; callers run it both before AND right
//     after each claude exec, so the shared login is refreshed within one turn.
//
// A file bind-mount would NOT help (atomic-rename of the source makes the mount stale,
// and rename onto a mountpoint EBUSYs) — the symlink + copy-back is the robust choice.
func ensureChatClaudeConfig() (string, error) {
	dir := chatClaudeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	shared := filepath.Join(claude.ConfigDir(), ".credentials.json")
	reconcileChatCreds(shared, filepath.Join(dir, ".credentials.json"))
	// Seed onboarding/theme/trust once from the shared config so a headless run in a
	// fresh dir doesn't stall on first-run prompts; it diverges independently after.
	seed := filepath.Join(dir, ".claude.json")
	if _, err := os.Stat(seed); os.IsNotExist(err) {
		if b, rerr := os.ReadFile(filepath.Join(claude.ConfigDir(), ".claude.json")); rerr == nil {
			_ = os.WriteFile(seed, b, 0o600)
		}
	}
	return dir, nil
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

// chatMCPArgs attaches the local Agent Fleet stdio MCP server (this same binary's
// `mcp-stdio` subcommand) to a chat's claude, scoped strictly to it (no global/project
// MCP config leaks in, and it doesn't leak out to the interactive sessions). docs/19 Q1.
func chatMCPArgs(write bool) []string {
	serverArgs := `"mcp-stdio"`
	if write {
		// Advertise the write tools too (docs/19 Q2). The advertised set is the gate:
		// an af_read chat's server never lists send_to_session, so the model can't call it.
		serverArgs = `"mcp-stdio","--write"`
	}
	cfg := fmt.Sprintf(`{"mcpServers":{"af":{"command":%q,"args":[%s]}}}`, paths.ExePath(), serverArgs)
	return []string{"--mcp-config", cfg, "--strict-mcp-config"}
}

// chatClaudeCmd builds a claude exec configured for the chat: run in chatWorkdir with
// the chat-only CLAUDE_CONFIG_DIR (shared creds). Falls back to the inherited env if
// the config dir can't be prepared.
func chatClaudeCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = chatWorkdir()
	if ccd, err := ensureChatClaudeConfig(); err == nil {
		cmd.Env = envWith("CLAUDE_CONFIG_DIR=" + ccd)
	}
	return cmd
}
