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

var chatMu sync.Mutex // serializes load-modify-save on the send path

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
		"--append-system-prompt", chatPersona}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.ClaudeSessionID != "" {
		args = append(args, "--resume", c.ClaudeSessionID)
	} else {
		c.ClaudeSessionID = randUUID()
		args = append(args, "--session-id", c.ClaudeSessionID)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = chatWorkdir()
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
	Agent string `json:"agent"`
	Title string `json:"title"`
	Model string `json:"model"`
}

func handleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req chatCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if _, ok := chatProviders[req.Agent]; !ok {
		writeErr(w, http.StatusBadRequest, "bad_agent", "未対応のエージェントです")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新しいチャット"
	}
	now := nowMs()
	c := &chatConversation{
		ID: randUUID(), Agent: req.Agent, Title: title, Model: strings.TrimSpace(req.Model),
		CreatedAt: now, UpdatedAt: now, Messages: []chatMessage{},
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

	chatMu.Lock()
	defer chatMu.Unlock()

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
