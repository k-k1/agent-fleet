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
//
// このファイルは会話モデルとペルソナ/言語/ツールポリシーのみを持つ。永続化は chat_store.go、
// プロバイダ実装は chat_providers.go、HTTP ハンドラは chat_handlers.go（docs/23 残②で分割）。

import (
	"os"
	"strings"
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
	ClaudeSessionID   string `json:"claude_session_id,omitempty"`
	CodexSessionID    string `json:"codex_session_id,omitempty"`
	OpencodeSessionID string `json:"opencode_session_id,omitempty"`
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
	Tools       string   `json:"tools,omitempty"`     // "none" | "af_read" | "af_write"
	Knowledge   []string `json:"knowledge,omitempty"` // dirs passed to --add-dir
	// Seed is a transient first-turn prompt returned by create (Files attach, docs/19
	// Phase C) for the Console to prefill the composer. It is set AFTER saveConv, so it is
	// never persisted — the composer owns it thereafter.
	Seed string `json:"seed,omitempty"`
}

// afToolsEnabled reports whether the fleet MCP tools attach to this chat at all (read
// or write grant). New conversations set Tools; pre-assistant conversations only have
// AFTools.
func (c *chatConversation) afToolsEnabled() bool {
	switch c.Tools {
	case toolsAFRead, toolsAFWrite:
		return true
	case toolsNone:
		return false
	default: // legacy conversations created before assistants had no Tools field
		return c.AFTools
	}
}

// afWriteEnabled reports whether the write tools (send_to_session …) are exposed to this
// chat — only when the assistant granted af_write (docs/19 Q2 opt-in).
func (c *chatConversation) afWriteEnabled() bool { return c.Tools == toolsAFWrite }

// chatOutputRule is appended to every chat's system prompt. The file/shell tools are hard-
// disallowed (chatToolLimits), so a write attempt just fails — this steers the model away
// from even trying (it otherwise tends to "save the result to a file" at the end of a big
// task, then fail) and to always return the full result as chat text.
const chatOutputRule = "【出力ルール（厳守）】ファイルの作成・編集・保存やコマンド実行はできません（ツールは無効化されています）。" +
	"翻訳・要約・回答などの成果物は、長い場合でも省略・分割保存をせず、必ずこの返信の本文にテキストとして全文出力してください。" +
	"ファイルへ保存しようとしないでください。"

// langRuleJA / langRuleEN force the reply language when the user pins one in ui-prefs
// (DisplayTab). Each is written in its own target language so it also steers the model
// (a Japanese rule after an English persona would give mixed signals). No rule = follow
// the input language, the persona-driven default.
const (
	langRuleJA = "【言語】特に指示がない限り、必ず日本語で回答してください（渡された文章が他言語でも回答は日本語で）。"
	langRuleEN = "[Language] Unless explicitly instructed otherwise, always respond in English (even if the given text is in another language)."
)

// languageRule returns the forced-output-language directive for this conversation, or ""
// to leave language to the input/persona. The translate assistant is exempt: its whole
// job is auto-detecting direction (JA↔EN), which a forced language would break.
func (c *chatConversation) languageRule() string {
	if c.AssistantID == "translate" {
		return ""
	}
	switch chatOutputLanguage() {
	case "ja":
		return langRuleJA
	case "en":
		return langRuleEN
	}
	return ""
}

// personaOf is the system prompt for this conversation: the assistant snapshot's persona
// (or the generic chat persona), followed by the global output rule and, when the user
// pinned an output language, a language directive.
func (c *chatConversation) personaOf() string {
	base := chatPersona
	if strings.TrimSpace(c.Persona) != "" {
		base = c.Persona
	}
	s := base + "\n\n" + chatOutputRule
	if rule := c.languageRule(); rule != "" {
		s += "\n\n" + rule
	}
	return s
}

// knowledgeArgs returns --add-dir flags for each knowledge dir that currently exists.
// Builtin knowledge is re-materialized first so a container rebuild self-heals.
func (c *chatConversation) knowledgeArgs() []string {
	var args []string
	for _, d := range c.knowledgeDirs() {
		args = append(args, "--add-dir", d)
	}
	return args
}

// knowledgeDirs returns the existing knowledge dirs (re-materializing builtin
// knowledge first). claude passes them as --add-dir; codex/opencode get them listed
// in the prompt preamble (their read tools can open the paths directly).
func (c *chatConversation) knowledgeDirs() []string {
	if len(c.Knowledge) == 0 {
		return nil
	}
	_ = ensureBuiltinKnowledge()
	var dirs []string
	for _, d := range c.Knowledge {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// chatMeta is the light shape returned by the list endpoint (no message bodies).
type chatMeta struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	AssistantID  string `json:"assistant_id,omitempty"` // which assistant backs this thread (Q2)
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

// defaultChatModel is the model the assistant chat's claude runs on when the assistant
// (or conversation) doesn't pin one — Sonnet keeps assistant chats fast/cheap. Override
// deployment-wide with AF_CHAT_MODEL. An explicit per-assistant model still wins.
const defaultChatModel = "claude-sonnet-5"

// chatModel resolves the --model for a conversation: its own model if set, else the
// deployment default (AF_CHAT_MODEL or defaultChatModel).
func chatModel(c *chatConversation) string {
	if c.Model != "" {
		return c.Model
	}
	return envOr("AF_CHAT_MODEL", defaultChatModel)
}

// chatTimeout bounds a single CLI turn so a hung process can't wedge the request.
const chatTimeout = 240 * time.Second
