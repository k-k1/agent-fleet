package main

// Assistant chat — a headless-CLI LLM chat/translation assistant, distinct from
// the tmux coding sessions. See docs/log/19-assistant-chat.md.
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
// プロバイダ実装は chat_providers.go、HTTP ハンドラは chat_handlers.go（docs/log/23 残②で分割）。

import (
	"os"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// chatStep is one "作業過程" item of an assistant turn (docs/log/19): the narration the model
// emitted right before it called a tool. On a tool-using answer claude produces several
// assistant messages — each ending in a tool call (stop_reason=tool_use) is a working step,
// and only the last (stop_reason=end_turn) is the final answer. We keep the steps so the UI
// can show the process separately from — but alongside — the final reply (保持).
type chatStep struct {
	Text  string   `json:"text,omitempty"`  // narration before the tool call(s)
	Tools []string `json:"tools,omitempty"` // tool name(s) invoked in this step
}

// chatMessage is one turn in a conversation.
type chatMessage struct {
	Role    string `json:"role"` // "user" | "assistant" | "report" (docs/log/30 セッション報告) | "notice" (システム通知)
	Content string `json:"content"`
	TS      int64  `json:"ts"` // unix millis
	// Agent is the backend that actually generated this assistant turn. It may differ
	// from chatConversation.Agent when auth loss makes the chat fall back to another
	// connected CLI. Empty on legacy messages whose executing backend was not recorded.
	Agent string `json:"agent,omitempty"`
	// Model is the model that drove THIS assistant turn — the value the CLI itself
	// reported (claude/opencode) or, for the CLIs that report none, the exact value we
	// passed on the command line for this turn. Never the conversation's current
	// setting: a model change (or a fallback to another backend) must not rewrite what
	// past turns were answered with. Empty when nothing was passed and nothing was
	// reported (e.g. cursor's "auto") — the UI then shows no model rather than a guess.
	Model string `json:"model,omitempty"`
	// Steps is the assistant turn's working process (narration before each tool call),
	// separated from Content (the final answer). Empty for user turns and tool-less replies.
	Steps []chatStep `json:"steps,omitempty"`
	// Session names the reporting session for role=="report" (docs/log/30) — the Console
	// renders these as a session-origin card, not a user/assistant bubble.
	Session string `json:"session,omitempty"`
	// Instr lists the instruction-ledger row ids this completion report covers
	// (docs/log/51 Phase 2). It is the DELIVERY IDEMPOTENCY key: a retry after a crash
	// between「追記成功」and「台帳更新」finds its own ids here and appends nothing.
	// Empty for interim reports (question / plan-approval), which are non-consuming
	// and may legitimately repeat within one instruction.
	Instr []string `json:"instr,omitempty"`
	// Delivered marks a report that has been fed into the provider's context (either
	// by its own auto turn or injected into a later prompt). An undelivered report
	// exists in the stored thread but the LLM hasn't seen it yet.
	Delivered bool `json:"delivered,omitempty"`
	// NoticeKey / NoticeArgs localize the bodies WE generate — role=="notice" (ADR 0033)
	// and, since docs/log/28 P6, role=="report" as well: the Console renders the catalog
	// entry for the user's locale and only falls back to Content (source language) for
	// records written before the key existed. See chat_notice.go / chat_report_text.go.
	NoticeKey  string            `json:"notice_key,omitempty"`
	NoticeArgs map[string]string `json:"notice_args,omitempty"`
	// ReportKind / ReportReason are the EVENT a role=="report" card stands for
	// (answer-ready / question / plan-approval / exit …, qualified by turn-failed,
	// turn-aborted, oom …). docs/log/28 P6 separated the two readers of a report: the card
	// the user reads comes from NoticeKey (display), and the operator's marching orders
	// are re-rendered from these fields at injection time (reportPromptFor) in the
	// display language — storing the instruction text would freeze its language and
	// make translating the card change what the operator is told to do.
	// Empty on reports written before P6: those fall back to Content on both sides.
	ReportKind   string `json:"report_kind,omitempty"`
	ReportReason string `json:"report_reason,omitempty"`
}

// chatConversation is the persisted record (one JSON file per conversation).
type chatConversation struct {
	ID string `json:"id"`
	// Slug is the conversation's short addressable identity ("a"+6 base32 — the
	// assistant twin of session slugs "s…"), used wherever a human or an automation
	// (schedules, operator tools) references a conversation without a UUID. Assigned at
	// creation; conversations from before the field are backfilled at agent start
	// (backfillConvSlugs). Immutable once set.
	Slug  string `json:"slug,omitempty"`
	Agent string `json:"agent"` // preferred provider snapshotted at creation
	// ActiveAgent is the backend that generated the latest successful assistant turn.
	// Keeping it separate preserves the user's preferred provider while letting the UI
	// truthfully show a live fallback to codex/opencode/agy.
	ActiveAgent string        `json:"active_agent,omitempty"`
	Title       string        `json:"title"`
	Model       string        `json:"model,omitempty"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
	Messages    []chatMessage `json:"messages"`
	// Provider-native resume handles so each turn continues the CLI's own
	// conversation (context + caching) instead of re-feeding the whole history.
	ClaudeSessionID   string `json:"claude_session_id,omitempty"`
	CodexSessionID    string `json:"codex_session_id,omitempty"`
	OpencodeSessionID string `json:"opencode_session_id,omitempty"`
	// AgyConversationID is agy's native conversation UUID (`--conversation`), captured
	// from cache/last_conversations.json after the first `-p` turn (agy has no
	// session-id flag nor structured output to hand it over — see agyChat).
	AgyConversationID string `json:"agy_conversation_id,omitempty"`
	// CursorSessionID is cursor's chat UUID (`--resume <uuid>`), self-minted on the
	// first turn (cursor CREATEs a chat under a fresh valid v4) and echoed back in the
	// -p result's session_id — see cursorChat.
	CursorSessionID string `json:"cursor_session_id,omitempty"`
	// Provider cursors are the number of canonical Messages already represented in
	// each native provider session. A provider that returns after fallback receives
	// the intervening user/assistant turns before the new prompt, instead of resuming
	// a stale private history and mistaking an old action for the current request.
	ClaudeMessageCursor   int `json:"claude_message_cursor,omitempty"`
	CodexMessageCursor    int `json:"codex_message_cursor,omitempty"`
	OpencodeMessageCursor int `json:"opencode_message_cursor,omitempty"`
	AgyMessageCursor      int `json:"agy_message_cursor,omitempty"`
	CursorMessageCursor   int `json:"cursor_message_cursor,omitempty"`
	// AFTools attaches the local Agent Fleet MCP tools (read-only) to this chat's
	// claude so it can inspect the user's workspace (docs/log/19 Q1). Legacy field kept for
	// conversations created before assistants (Q2); new conversations drive tools via the
	// snapshot Tools grant below and afToolsEnabled() reconciles the two.
	AFTools bool `json:"af_tools,omitempty"`
	// Assistant snapshot (docs/log/19 Q2): the conversation copies its assistant's settings at
	// creation, so later edits to the assistant don't rewrite existing threads. AssistantID
	// is kept for display/reference; Persona/Tools/Knowledge drive the provider.
	AssistantID string   `json:"assistant_id,omitempty"`
	Persona     string   `json:"persona,omitempty"`   // --append-system-prompt (falls back to chatPersona)
	Tools       string   `json:"tools,omitempty"`     // "none" | "af_read" | "af_write"
	Knowledge   []string `json:"knowledge,omitempty"` // dirs passed to --add-dir
	// Integrations snapshots the assistant's ops MCP servers (docs/log/25 Phase 1), e.g.
	// ["pagerduty"]. mcpConfigArgs attaches each read-only via mcp-run when the user
	// has the matching connection configured.
	Integrations []string `json:"integrations,omitempty"`
	// Locked pins the conversation against deletion (docs/log/45): while true,
	// DELETE /chat/conversations/{id} is refused with 403 locked. Toggled by
	// POST /chat/conversations/{id}/lock; nothing else writes it, so a turn that
	// rewrites the conversation preserves it like any other field.
	Locked bool `json:"locked,omitempty"`
	// Seed is a transient first-turn prompt returned by create (Files attach, docs/log/19
	// Phase C) for the Console to prefill the composer. It is set AFTER saveConv, so it is
	// never persisted — the composer owns it thereafter.
	Seed string `json:"seed,omitempty"`
	// SeedVerb records the ad-hoc Files verb that created this thread ("translate" |
	// "summarize"), when the chat was opened without a standing assistant (docs/log/30 ②).
	// It drives the persona-embedded verb behaviour — notably the translate language
	// exemption below — so deleting the old 翻訳/汎用 builtins costs no capability.
	SeedVerb string `json:"seed_verb,omitempty"`
	// InProgress is a transient flag set only by handleChatGet when an assistant turn is
	// still running for this conversation (chat_live.go). It lets a client that reloaded
	// mid-answer know the reply is still coming and poll for it. Never persisted (set only
	// on the GET response, after load and never before saveConv).
	InProgress bool `json:"in_progress,omitempty"`
	// AutoTurns counts the operator turns run automatically off session reports since
	// the user's last message (docs/log/30). Capped at maxAutoTurns; reset on every user
	// send — the structural stop for an unattended follow-up loop.
	AutoTurns int `json:"auto_turns,omitempty"`
	// AutoPausedNotified marks that the "自動応答の上限に達しました" pause notice has
	// already been appended for the CURRENT cap-reach, so the user is told exactly once
	// per unattended run instead of on every further report while capped. Reset with
	// AutoTurns on every user send.
	AutoPausedNotified bool `json:"auto_paused_notified,omitempty"`
	// Context is the conversation's current context-window fill, captured from the
	// provider's own usage events after each successful turn (chat_usage.go) — the
	// chat analog of get_session_usage's context (same wire shape as the mirror's
	// ContextBar). Nil until the first turn or when the provider reported no usage.
	Context *usagex.ContextUsage `json:"context,omitempty"`
	// CtxWarned marks that the context-pressure notice (chatCtxWarnPct) has been
	// appended for the CURRENT crossing; reset when usage falls back under the
	// threshold (e.g. the provider compacted) so a later re-crossing warns again.
	CtxWarned bool `json:"ctx_warned,omitempty"`
	// PendingHandoff is the compaction summary (docs/log/33 第2段) waiting to ride the
	// next prompt as a preamble — the new provider session's seed context. Cleared
	// only after that turn succeeds (injectHandoff / chat_compact.go).
	PendingHandoff string `json:"pending_handoff,omitempty"`
	// Plan is the conversation's standing work plan (docs/log/33 第5段), carried into every
	// fresh provider session **verbatim** — unlike PendingHandoff it is never summarized
	// and never consumed, so repeated compaction cannot erode it. Written by compaction
	// (差分更新), the 計画を更新 button and hand editing; see chat_plan.go.
	Plan          string `json:"plan,omitempty"`
	PlanUpdatedAt int64  `json:"plan_updated_at,omitempty"`
	// turnModel carries the model of the turn currently running, from the provider
	// (which alone knows what it passed / what the CLI reported) to the caller that
	// appends the assistant message. Unexported = never persisted and never sent to the
	// Console: it is scratch for one turn, while chatMessage.Model is the record.
	// Every provider clears it on entry and sets it only on success, so a failed or
	// non-answering call can never lend its model to the next appended message.
	turnModel string
	// modelOverride swaps the model for the NEXT provider call only (unexported =
	// never persisted). 自動ターン専用モデル（設定 > アシスタント）が使う: 報告処理の
	// 定型ターンだけを軽量モデルで回し、利用者ターン・圧縮の要約ターンは会話本来の
	// モデルのまま。呼び出し側（runReportAutoTurn）が send の直前に立てて直後に
	// 倒す。claude のみ（chatModel 経由 — codex/opencode は c.Model を直接読む）。
	// 注意: prompt cache はモデル毎に別なので、上書きターンは会話モデルのキャッシュ
	// に乗らない — 自動ターンは散発的でどのみち冷えている（実測）ため、単価差の
	// 利得が支配的という判断。
	modelOverride string
}

// startTurn resets the per-turn scratch state. Called at the top of every provider
// send/sendStream — including the ones that end up not reporting a model at all, so a
// previous call in the same request (an auto-compaction turn, a retry after recovery)
// cannot leak its model into this turn's message.
func (c *chatConversation) startTurn() { c.turnModel = "" }

// noteTurnModel records the model that actually drove this turn. Providers call it at
// their success point only.
func (c *chatConversation) noteTurnModel(model string) { c.turnModel = strings.TrimSpace(model) }

// afToolsEnabled reports whether the fleet MCP tools attach to this chat at all (read
// or write grant). New conversations set Tools; pre-assistant conversations only have
// AFTools.
func (c *chatConversation) afToolsEnabled() bool {
	switch c.Tools {
	case assistants.ToolsAFRead, assistants.ToolsAFWrite:
		return true
	case assistants.ToolsNone:
		return false
	default: // legacy conversations created before assistants had no Tools field
		return c.AFTools
	}
}

// afWriteEnabled reports whether the write tools (send_to_session …) are exposed to this
// chat — only when the assistant granted af_write (docs/log/19 Q2 opt-in).
func (c *chatConversation) afWriteEnabled() bool { return c.Tools == assistants.ToolsAFWrite }

// chatOutputRule is appended to every chat's system prompt. The file/shell tools are hard-
// disallowed (chatToolLimits), so a write attempt just fails — this steers the model away
// from even trying (it otherwise tends to "save the result to a file" at the end of a big
// task, then fail) and to always return the full result as chat text.
//
// Locale-branched like the other prompts that steer the reply language (docs/log/28 §4):
// this rule sits in EVERY system prompt, so a Japanese-only version quietly biases every
// answer toward Japanese even for a user whose Console is English.
const chatOutputRule = "【出力ルール（厳守）】ファイルの作成・編集・保存やコマンド実行はできません（ツールは無効化されています）。" +
	"翻訳・要約・回答などの成果物は、長い場合でも省略・分割保存をせず、必ずこの返信の本文にテキストとして全文出力してください。" +
	"ファイルへ保存しようとしないでください。"

const chatOutputRuleEN = "[Output rules (strict)] You cannot create, edit or save files, or run commands (the tools are disabled). " +
	"Always return the whole result — translation, summary, answer — as text in the body of this reply, " +
	"however long it is; never abbreviate it or split it into saved files. Do not try to write to a file."

func chatOutputRuleFor(locale string) string {
	if locale == "en" {
		return chatOutputRuleEN
	}
	return chatOutputRule
}

// langRuleJA / langRuleEN force the reply language when the user pins one in ui-prefs
// (DisplayTab). Each is written in its own target language so it also steers the model
// (a Japanese rule after an English persona would give mixed signals). No rule = follow
// the input language, the persona-driven default.
const (
	langRuleJA = "【言語】特に指示がない限り、必ず日本語で回答してください（渡された文章が他言語でも回答は日本語で）。"
	langRuleEN = "[Language] Unless explicitly instructed otherwise, always respond in English (even if the given text is in another language)."
)

// languageRule returns the forced-output-language directive for this conversation, or ""
// to leave language to the input/persona. Translation is exempt: its whole job is
// auto-detecting direction (JA↔EN), which a forced language would break. That is now the
// ad-hoc "translate" verb (docs/log/30 ②); the AssistantID check keeps threads created by the
// old 翻訳 builtin exempt too.
//
// "auto" is symmetric again (docs/log/28 P6). It used to fall back to the display language
// for "en" only: the personas and the output rule were written in Japanese, so with no
// rule at all an English Console still got Japanese answers, and that one-sided patch
// corrected it. P6 localized the actual prompts — persona (builtin and ad-hoc), output
// rule, carry-over preambles — so a display language now steers the reply on its own and
// auto can go back to meaning the same thing in both locales: follow the input.
//
// That is also the better behaviour for the case the patch got wrong: on an English
// Console, "translate this into Japanese" or "summarize this Japanese article in
// Japanese" no longer fights a forced-English directive. Whoever wants the reply pinned
// regardless of input still has 設定 > 回答言語 (ja / en), which wins over everything here.
func (c *chatConversation) languageRule() string {
	if c.SeedVerb == "translate" || c.AssistantID == "translate" {
		return ""
	}
	switch uiprefs.ChatOutputLanguage() {
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
	lang := uiprefs.Locale()
	base := chatPersonaFor(lang)
	if strings.TrimSpace(c.Persona) != "" {
		base = c.Persona
	}
	s := base + "\n\n" + chatOutputRuleFor(lang)
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
	ID           string               `json:"id"`
	Slug         string               `json:"slug,omitempty"` // short addressable id ("a…", see chatConversation.Slug)
	Agent        string               `json:"agent"`
	ActiveAgent  string               `json:"active_agent,omitempty"`
	AssistantID  string               `json:"assistant_id,omitempty"` // which assistant backs this thread (Q2)
	Title        string               `json:"title"`
	Model        string               `json:"model,omitempty"`
	CreatedAt    int64                `json:"created_at"`
	UpdatedAt    int64                `json:"updated_at"`
	MessageCount int                  `json:"message_count"`
	Context      *usagex.ContextUsage `json:"context,omitempty"` // current context fill (chat_usage.go)
	Locked       bool                 `json:"locked,omitempty"`  // 削除ロック（docs/log/45）: true の間 DELETE は拒否
}

// chatPersona keeps the headless agent in plain conversational mode (translate,
// summarize, answer) rather than reaching for file edits or bash on its own.
// docs/log/28 P6: 表示言語で書く（アシスタントを選ばない会話の唯一のペルソナなので、ここが
// 日本語のままだと英語 Console でも回答が日本語に倒れる）。
func chatPersonaFor(lang string) string {
	if lang == "en" {
		return "You assist an Agent Fleet user with their work. " +
			"Handle what you are asked — translating or summarizing Markdown documents, answering questions — concisely. " +
			"Unless you are told otherwise, do not create or edit files and do not run commands; answer directly in the chat."
	}
	return "あなたは Agent Fleet 利用者の作業を補助するアシスタントです。" +
		"Markdown 文書の翻訳や要約、質問への回答など、頼まれた作業に簡潔に応じてください。" +
		"特に指示がない限り、ファイルの作成・編集やコマンド実行はせず、チャットで直接回答してください。"
}

// defaultChatModel is the model the assistant chat's claude runs on when the assistant
// (or conversation) doesn't pin one — Sonnet keeps assistant chats fast/cheap. Override
// deployment-wide with AF_CHAT_MODEL. An explicit per-assistant model still wins.
const defaultChatModel = "claude-sonnet-5"

// defaultCodexChatModel favors the high-volume Luna tier for conversational
// assistants. Assistants that need deeper coding/reasoning can still pin gpt-5.6
// explicitly in their template.
const defaultCodexChatModel = "gpt-5.6-luna"

// defaultOpencodeChatModel favors the capable general-purpose model in the
// currently connected OpenCode catalog. An assistant can still pin a different
// provider/model explicitly in its template.
const defaultOpencodeChatModel = "opencode/nemotron-3-ultra-free"

// defaultAgyChatModel favors the fast Gemini Flash tier for conversational
// assistants: chat is latency-sensitive, agy's distinctive value here is Gemini
// (its Claude models duplicate the claude backend and are Thinking-only), and
// Flash is the quota-cheapest choice on the scarce Starter/free plan (docs/log/32
// Track D). The value is `agy models` display-name syntax; a name the live
// catalog no longer lists is dropped at send time (agyChatModel) so a rename
// upstream degrades to agy's own default instead of a hard error.
const defaultAgyChatModel = "Gemini 3.5 Flash (Medium)"

const assistantRecommendedModel = "recommended"

func recommendedCatalogModel(ids []string, target, fallback string) string {
	if slices.Contains(ids, target) {
		return target
	}
	return fallback
}

// recommendedAssistantModel resolves the safe product recommendation against the
// live catalog. OpenCode Go ids are selected only when this account actually lists
// them; a non-Go account keeps the universally available Nemotron fallback.
// 「使わないモデル」（model_deny.go）で除外された候補は推奨としても選ばない — 空を
// 返して CLI 自身の既定へ委ねる。カタログ由来の候補は絞ったカタログから選び直す。
func recommendedAssistantModel(agent string) string {
	switch agent {
	case session.KindClaude:
		return visibleModel(agent, "sonnet")
	case session.KindCodex:
		return visibleModel(agent, defaultCodexChatModel)
	case session.KindOpencode:
		const goModel = "opencode-go/glm-5.2"
		return recommendedCatalogModel(visibleModelIDs(agent, opencode.Models()), goModel,
			visibleModel(agent, defaultOpencodeChatModel))
	case session.KindAgy:
		return visibleModel(agent, defaultAgyChatModel)
	}
	return "" // cursor: Auto is the only entitlement-safe recommendation
}

// resolveChatModel applies an agent-specific default only when the assistant did not
// pin a model. The resolved value is snapshotted onto a new conversation, so an
// existing thread keeps its prior model selection.
func resolveChatModel(agent, model string) string {
	if model = strings.TrimSpace(model); model != "" {
		return model
	}
	if model, ok := assistantChatModelPref(agent); ok {
		model = strings.TrimSpace(model)
		if model != assistantRecommendedModel {
			return model
		}
	}
	return recommendedAssistantModel(agent)
}

// chatModel resolves the --model for a conversation: a per-call override first
// (自動ターン専用モデル — modelOverride のコメント参照), then its own model if set,
// else the deployment default (AF_CHAT_MODEL or defaultChatModel).
func chatModel(c *chatConversation) string {
	if c.modelOverride != "" {
		return c.modelOverride
	}
	if c.Model != "" {
		return c.Model
	}
	return envOr("AF_CHAT_MODEL", defaultChatModel)
}

// chatModelFor resolves the --model for the backend that is ACTUALLY driving this turn.
// 会話が持つ Model はピン留めされたエージェント基準の1本しかないので、認証フォールバック
// （chatProviderFor）や利用者による途中切替で別バックエンドが回すターンにそのまま渡すと、
// その CLI に他 CLI のモデル id を食わせることになる（codex に "sonnet" 等）。別バックエンドの
// ターンでは設定「アシスタントのモデル」の当該 CLI 行から解決し直す — 設定画面の説明
// （「優先順位で別の CLI に切り替わった場合も、その CLI の行で選んだモデルを使います」）が
// 実装上の契約。プロバイダは自分の kind を渡すだけでよい。
func chatModelFor(c *chatConversation, kind string) string {
	if c.modelOverride != "" {
		return c.modelOverride // 自動ターン専用モデル（呼び出し側が claude ターンだけに立てる）
	}
	if c.Agent != "" && kind != c.Agent {
		return resolveChatModel(kind, "")
	}
	if kind == session.KindClaude {
		return chatModel(c) // 旧来の AF_CHAT_MODEL 既定を保つ
	}
	return c.Model // 空 = その CLI 自身の既定に委ねる（--model を渡さない）
}

// chatTimeout bounds a single CLI turn so a hung process can't wedge the request.
const chatTimeout = 240 * time.Second
