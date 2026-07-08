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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// --- providers ---

// chatProvider drives one agent's CLI in non-interactive mode. send appends the
// assistant reply's text as its return value and may mutate c's resume handles.
type chatProvider interface {
	send(ctx context.Context, c *chatConversation, prompt string) (string, error)
}

var chatProviders = map[string]chatProvider{
	session.KindClaude: claudeChat{},
	session.KindCodex:  codexChat{},
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

// codexChat is a documented seam. The provider dispatch is real (two entries), but
// codex's `--json` event schema and resume-id capture need live verification, so
// it is not yet exposed in the New-Chat picker (registry cap headlessChat is
// claude-only). Phase A.2 implements this via `codex exec --json` /
// `codex exec resume <id>` and flips the cap. See docs/19-assistant-chat.md.
type codexChat struct{}

func (codexChat) send(context.Context, *chatConversation, string) (string, error) {
	return "", errors.New("codex チャットは準備中です（Phase A.2）")
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
	shared := filepath.Join(claudeConfigDir(), ".credentials.json")
	reconcileChatCreds(shared, filepath.Join(dir, ".credentials.json"))
	// Seed onboarding/theme/trust once from the shared config so a headless run in a
	// fresh dir doesn't stall on first-run prompts; it diverges independently after.
	seed := filepath.Join(dir, ".claude.json")
	if _, err := os.Stat(seed); os.IsNotExist(err) {
		if b, rerr := os.ReadFile(filepath.Join(claudeConfigDir(), ".claude.json")); rerr == nil {
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
	cfg := fmt.Sprintf(`{"mcpServers":{"af":{"command":%q,"args":[%s]}}}`, agentExe(), serverArgs)
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
