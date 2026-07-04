package main

// Assistant chat — a headless-CLI LLM chat/translation assistant, distinct from
// the tmux coding sessions. See docs/19-assistant-chat.md.
//
// Why a parallel subsystem (not a SessionKind): the Agent interface in agent.go
// is PTY-shaped (buildLaunch returns a tmux program). A real chat has no TUI, so
// forcing it through that contract would break the session machinery (listing,
// archive, idle detection). Instead a chat "conversation" is a stored JSON record
// tagged with an agent kind, and chatProviders[agent] drives the right CLI in
// non-interactive mode — reusing the container's existing per-user subscription
// auth (no new credentials, ToS-clean). One PaneKind renders any conversation;
// the agent kind only selects the backend + presentation.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// chatMessage is one turn in a conversation.
type chatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
	TS      int64  `json:"ts"` // unix millis
}

// chatConversation is the persisted record (one JSON file per conversation).
type chatConversation struct {
	ID        string        `json:"id"`
	Agent     string        `json:"agent"` // "claude" | "codex" — selects the provider
	Title     string        `json:"title"`
	Model     string        `json:"model,omitempty"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
	Messages  []chatMessage `json:"messages"`
	// Provider-native resume handles so each turn continues the CLI's own
	// conversation (context + caching) instead of re-feeding the whole history.
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	CodexSessionID  string `json:"codex_session_id,omitempty"`
	// AFTools attaches the local Agent Fleet MCP tools (read-only) to this chat's
	// claude so it can inspect the user's workspace (docs/19 Q1). Legacy field kept for
	// conversations created before assistants (Q2); new conversations drive tools via the
	// snapshot Tools grant below and afToolsEnabled() reconciles the two.
	AFTools bool `json:"af_tools,omitempty"`
	// Assistant snapshot (docs/19 Q2): the conversation copies its assistant's settings at
	// creation, so later edits to the assistant don't rewrite existing threads. AssistantID
	// is kept for display/reference; Persona/Tools/Knowledge drive the provider.
	AssistantID string   `json:"assistant_id,omitempty"`
	Persona     string   `json:"persona,omitempty"`   // --append-system-prompt (falls back to chatPersona)
	Tools       string   `json:"tools,omitempty"`     // "none" | "af_read"
	Knowledge   []string `json:"knowledge,omitempty"` // dirs passed to --add-dir
}

// afToolsEnabled reports whether the read-only fleet MCP tools attach to this chat.
// New conversations set Tools; pre-assistant conversations only have AFTools.
func (c *chatConversation) afToolsEnabled() bool {
	if c.Tools != "" {
		return c.Tools == toolsAFRead
	}
	return c.AFTools
}

// personaOf is the system prompt for this conversation: the assistant snapshot's persona,
// or the generic chat persona for legacy/unset conversations.
func (c *chatConversation) personaOf() string {
	if strings.TrimSpace(c.Persona) != "" {
		return c.Persona
	}
	return chatPersona
}

// knowledgeArgs returns --add-dir flags for each knowledge dir that currently exists.
// Builtin knowledge is re-materialized first so a container rebuild self-heals.
func (c *chatConversation) knowledgeArgs() []string {
	if len(c.Knowledge) == 0 {
		return nil
	}
	_ = ensureBuiltinKnowledge()
	var args []string
	for _, d := range c.Knowledge {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			args = append(args, "--add-dir", d)
		}
	}
	return args
}

// chatMeta is the light shape returned by the list endpoint (no message bodies).
type chatMeta struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	Title        string `json:"title"`
	Model        string `json:"model,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	MessageCount int    `json:"message_count"`
}

// chatPersona keeps the headless agent in plain conversational mode (translate,
// summarize, answer) rather than reaching for file edits or bash on its own.
const chatPersona = "あなたは Agent Fleet 利用者の作業を補助するアシスタントです。" +
	"Markdown 文書の翻訳や要約、質問への回答など、頼まれた作業に簡潔に応じてください。" +
	"特に指示がない限り、ファイルの作成・編集やコマンド実行はせず、チャットで直接回答してください。"

// chatTimeout bounds a single CLI turn so a hung process can't wedge the request.
const chatTimeout = 240 * time.Second

// Per-conversation lock so concurrent turns to the SAME conversation serialize
// (load-modify-save), while different conversations proceed in parallel.
var convLocks sync.Map // id -> *sync.Mutex

func lockConv(id string) func() {
	m, _ := convLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// --- store ---

func chatDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "chats")
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

// chatMCPArgs attaches the local Agent Fleet stdio MCP server (this same binary's
// `mcp-stdio` subcommand) to a chat's claude, scoped strictly to it (no global/project
// MCP config leaks in, and it doesn't leak out to the interactive sessions). docs/19 Q1.
func chatMCPArgs() []string {
	cfg := fmt.Sprintf(`{"mcpServers":{"af":{"command":%q,"args":["mcp-stdio"]}}}`, agentExe())
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

// validConvID guards path traversal: IDs are our own randUUID() output.
func validConvID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-') {
			return false
		}
	}
	return true
}

func convPath(id string) string { return filepath.Join(chatDir(), id+".json") }

func loadConv(id string) (*chatConversation, error) {
	if !validConvID(id) {
		return nil, errors.New("invalid conversation id")
	}
	b, err := os.ReadFile(convPath(id))
	if err != nil {
		return nil, err
	}
	var c chatConversation
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveConv(c *chatConversation) error {
	if err := os.MkdirAll(chatDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(convPath(c.ID), append(b, '\n'), 0o600)
}

func listConvs() ([]chatMeta, error) {
	ents, err := os.ReadDir(chatDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []chatMeta{}, nil
		}
		return nil, err
	}
	out := []chatMeta{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		c, err := loadConv(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // skip unreadable entries rather than failing the whole list
		}
		out = append(out, chatMeta{
			ID: c.ID, Agent: c.Agent, Title: c.Title, Model: c.Model,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, MessageCount: len(c.Messages),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// randUUID returns a random RFC-4122 v4 UUID, used for both conversation IDs and
// the claude --session-id we pin.
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func nowMs() int64 { return time.Now().UnixMilli() }

// --- providers ---

// chatProvider drives one agent's CLI in non-interactive mode. send appends the
// assistant reply's text as its return value and may mutate c's resume handles.
type chatProvider interface {
	send(ctx context.Context, c *chatConversation, prompt string) (string, error)
}

var chatProviders = map[string]chatProvider{
	kindClaude: claudeChat{},
	kindCodex:  codexChat{},
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
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = randUUID()
		args = append(args, "--session-id", c.ClaudeSessionID)
	}
	args = append(args, c.knowledgeArgs()...)
	if c.afToolsEnabled() {
		args = append(args, chatMCPArgs()...)
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
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = randUUID()
		args = append(args, "--session-id", c.ClaudeSessionID)
	}
	args = append(args, c.knowledgeArgs()...)
	if c.afToolsEnabled() {
		args = append(args, chatMCPArgs()...)
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

// --- HTTP handlers ---

func handleChatList(w http.ResponseWriter, r *http.Request) {
	metas, err := listConvs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "chat_list", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": metas})
}

type chatCreateReq struct {
	AssistantID string `json:"assistant_id"` // preferred: snapshot this assistant's settings
	Agent       string `json:"agent"`        // legacy fallback when no assistant_id
	Title       string `json:"title"`
	Model       string `json:"model"` // legacy override (ignored when assistant_id is set)
}

func handleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req chatCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新しいチャット"
	}
	now := nowMs()
	c := &chatConversation{
		ID: randUUID(), Title: title, CreatedAt: now, UpdatedAt: now, Messages: []chatMessage{},
	}

	if req.AssistantID != "" {
		// Snapshot the assistant's settings onto the conversation (docs/19 Q2): later edits
		// to the assistant leave existing threads untouched.
		a, err := getAssistant(req.AssistantID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_assistant", "アシスタントが見つかりません")
			return
		}
		c.AssistantID = a.ID
		c.Agent = a.Agent
		c.Model = a.Model
		c.Persona = a.Persona
		c.Tools = a.Tools
		c.Knowledge = a.Knowledge
	} else {
		// Legacy path: plain agent + optional model, generic persona, read-only fleet tools
		// for claude (mirrors the pre-assistant default).
		if _, ok := chatProviders[req.Agent]; !ok {
			writeErr(w, http.StatusBadRequest, "bad_agent", "未対応のエージェントです")
			return
		}
		c.Agent = req.Agent
		c.Model = strings.TrimSpace(req.Model)
		if req.Agent == kindClaude {
			c.Tools = toolsAFRead
		} else {
			c.Tools = toolsNone
		}
	}

	if err := saveConv(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func handleChatGet(w http.ResponseWriter, r *http.Request) {
	c, err := loadConv(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func handleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validConvID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	if err := os.Remove(convPath(id)); err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, "chat_delete", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type chatSendReq struct {
	Content string `json:"content"`
}

func handleChatSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatSendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeErr(w, http.StatusBadRequest, "empty", "メッセージが空です")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	prov, ok := chatProviders[c.Agent]
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_agent", "未対応のエージェントです")
		return
	}

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: content, TS: nowMs()})

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	reply, err := prov.send(ctx, c, content)
	if err != nil {
		// Persist the user turn + resume handle even on failure so a retry continues.
		c.UpdatedAt = nowMs()
		_ = saveConv(c)
		writeErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}

	assistant := chatMessage{Role: "assistant", Content: reply, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
	c.UpdatedAt = nowMs()
	if err := saveConv(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": assistant, "conversation": c})
}

// handleChatStream is the streaming (Phase B) form of send: it runs the provider
// with token streaming and forwards deltas as Server-Sent Events. Frames:
//
//	data: {"delta":"<text>"}                      — an incremental chunk
//	data: {"error":"<msg>"}                       — provider/exec failure
//	data: {"done":true,"message":…,"conversation":…} — final turn saved
//
// Providers without a streaming variant fall back to a single delta (the full reply).
func handleChatStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatSendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeErr(w, http.StatusBadRequest, "empty", "メッセージが空です")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	prov, ok := chatProviders[c.Agent]
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_agent", "未対応のエージェントです")
		return
	}

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: content, TS: nowMs()})

	// From here the response is an SSE stream; per-frame errors ride the stream body.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // hint any intermediary not to buffer
	flusher, _ := w.(http.Flusher)
	emit := func(obj any) {
		b, _ := json.Marshal(obj)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()

	var reply string
	if sp, ok := prov.(streamingProvider); ok {
		reply, err = sp.sendStream(ctx, c, content, func(d string) { emit(map[string]string{"delta": d}) })
	} else {
		reply, err = prov.send(ctx, c, content)
		if err == nil {
			emit(map[string]string{"delta": reply})
		}
	}
	if err != nil {
		c.UpdatedAt = nowMs()
		_ = saveConv(c) // persist the user turn + resume handle so a retry continues
		emit(map[string]any{"error": err.Error()})
		return
	}

	assistant := chatMessage{Role: "assistant", Content: reply, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
	c.UpdatedAt = nowMs()
	_ = saveConv(c)
	emit(map[string]any{"done": true, "message": assistant, "conversation": c})
}
