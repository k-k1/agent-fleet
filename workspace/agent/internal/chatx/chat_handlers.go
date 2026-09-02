package chatx

// アシスタントチャットの HTTP ハンドラ（一覧・作成・取得・改名・削除・送信・ストリーム・consult）。
// chat.go からの機械的分割（docs/log/23 残②）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// --- HTTP handlers ---

func handleChatList(w http.ResponseWriter, r *http.Request) {
	metas, err := listConvs()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_list", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"conversations": metas})
}

type chatCreateReq struct {
	AssistantID string `json:"assistant_id"` // preferred: snapshot this assistant's settings
	Agent       string `json:"agent"`        // legacy fallback when no assistant_id
	Title       string `json:"title"`
	Model       string `json:"model"` // legacy override (ignored when assistant_id is set)
	// Ad-hoc attach (docs/log/19 Phase C): a Files right-click can hand a file/dir to the new
	// chat. AttachPath is browse-root-relative (resolved + denylisted via safeBrowsePath);
	// its dir is added to the conversation's knowledge so the assistant can read it, and a
	// seed prompt (below) is composed server-side with the absolute path.
	AttachPath string `json:"attach_path"`
	SeedVerb   string `json:"seed_verb"` // "translate" | "summarize" | "" (open-ended ask)
}

// verbPersona is the persona-embedded instruction for an ad-hoc Files verb (docs/log/30 ②).
// A translate/summarize chat opened from Files carries its own persona instead of pointing
// at a standing 翻訳/汎用 assistant, so those builtins could be removed with no loss. Any
// other verb ("") falls through to the generic chatPersona.
//
// docs/log/28 P6: 生成物（訳文・要約）を読むのは利用者なので、persona は表示言語で書く。翻訳の
// 言語ペアは表示言語と無関係に日本語↔英語のままにしてある — 「訳す方向」は入力から決まる話で、
// Console の表示言語で決めるものではない（languageRule も translate だけ除外している）。
func verbPersona(verb, lang string) string {
	en := lang == "en"
	switch verb {
	case "translate":
		if en {
			return "You are a translation assistant. " +
				"Translate the text you are given naturally, auto-detecting the direction between Japanese and English unless told otherwise. " +
				"Return the translation only — no preamble, no commentary. Preserve the Markdown formatting."
		}
		return "あなたは翻訳アシスタントです。" +
			"渡された文章を、指定がなければ日本語↔英語を自動判定して自然に翻訳してください。" +
			"訳文のみを返し、余計な前置きや解説は付けないでください。Markdown の書式は保持します。"
	case "summarize":
		if en {
			return "You are a summarization assistant. " +
				"Summarize the key points of the text you are given concisely, in the language of the original. " +
				"Put the important items in bullets, and add no preamble."
		}
		return "あなたは要約アシスタントです。" +
			"渡された文章の要点を、原文の言語に合わせて簡潔にまとめてください。" +
			"重要な項目は箇条書きにし、余計な前置きは付けないでください。"
	default:
		return ""
	}
}

// seedFor composes the first-turn prompt for an attached file/dir. The absolute path is
// used verbatim so the assistant's Read (scoped by --add-dir) resolves it directly.
// 会話の 1 通目としてそのまま表示される（利用者が自分で打ったように見える）ので、persona と
// 同じく表示言語で書く。
func seedFor(verb, abs string, isDir bool, lang string) string {
	if lang == "en" {
		switch verb {
		case "translate":
			return "Translate this file:\n" + abs
		case "summarize":
			return "Summarize this file:\n" + abs
		default:
			if isDir {
				return "Tell me about this directory:\n" + abs
			}
			return "Tell me about this file:\n" + abs
		}
	}
	switch verb {
	case "translate":
		return "次のファイルを翻訳してください：\n" + abs
	case "summarize":
		return "次のファイルを要約してください：\n" + abs
	default:
		if isDir {
			return "次のディレクトリについて教えてください：\n" + abs
		}
		return "次のファイルについて教えてください：\n" + abs
	}
}

// chatDefaultTitle is the fallback name for a conversation created without one. The
// Console sends its own (catalog "chat.new_title"), so this covers the other creators —
// MCP / schedules / the bridge — and follows the display language for the same reason.
func chatDefaultTitle(lang string) string {
	if lang == "en" {
		return "New chat"
	}
	return "新しいチャット"
}

func appendUniqueStr(ss []string, v string) []string {
	for _, s := range ss {
		if s == v {
			return ss
		}
	}
	return append(ss, v)
}

func handleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req chatCreateReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	lang := uiprefs.Locale()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = chatDefaultTitle(lang)
	}
	now := nowMs()
	c := &chatConversation{
		ID: randUUID(), Slug: newConvSlug(), Title: title, CreatedAt: now, UpdatedAt: now, Messages: []chatMessage{},
	}

	switch {
	case req.AssistantID != "":
		// Snapshot the assistant's settings onto the conversation (docs/log/19 Q2): later edits
		// to the assistant leave existing threads untouched.
		a, err := assistants.Get(req.AssistantID, assistantDeps())
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatAssistantNotFound, "assistant not found")
			return
		}
		c.AssistantID = a.ID
		c.Agent = a.Agent
		c.Model = resolveChatModel(a.Agent, a.Model)
		c.Persona = a.Persona
		c.Tools = a.Tools
		c.Knowledge = a.Knowledge
		c.Integrations = a.Integrations
	case verbPersona(req.SeedVerb, lang) != "":
		// Ad-hoc persona-embedded verb (docs/log/30 ②): a Files 翻訳/要約 opens a standalone chat
		// carrying the verb persona directly — no standing 翻訳/汎用 assistant to point at.
		// Read-only (the attached file arrives via knowledge --add-dir below); SeedVerb is
		// persisted so languageRule() keeps a translate thread language-agnostic.
		c.Agent = preferredHeadlessAgent()
		c.Model = resolveChatModel(c.Agent, "")
		c.Persona = verbPersona(req.SeedVerb, lang)
		c.Tools = assistants.ToolsNone
		c.SeedVerb = req.SeedVerb
	default:
		// Legacy path: plain agent + optional model, generic persona, read-only fleet tools
		// for claude (mirrors the pre-assistant default).
		if _, ok := chatProviders[req.Agent]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatAgentUnsupported, "unsupported agent")
			return
		}
		c.Agent = req.Agent
		c.Model = resolveChatModel(req.Agent, req.Model)
		if req.Agent == session.KindClaude {
			c.Tools = assistants.ToolsAFRead
		} else {
			c.Tools = assistants.ToolsNone
		}
	}

	// Ad-hoc attach from a Files right-click (docs/log/19 Phase C): resolve the target safely,
	// add its dir to knowledge so the assistant can read it, and compose the seed prompt.
	var seed string
	if req.AttachPath != "" {
		if full, _, ok := safeBrowsePath(req.AttachPath); ok {
			if fi, err := os.Stat(full); err == nil {
				dir := full
				if !fi.IsDir() {
					dir = filepath.Dir(full)
				}
				c.Knowledge = appendUniqueStr(c.Knowledge, dir)
				seed = seedFor(req.SeedVerb, full, fi.IsDir(), lang)
			}
		}
	}

	if err := saveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	c.Seed = seed // transient: set after save so it's returned once but never persisted
	httpx.WriteJSON(w, http.StatusOK, c)
}

// chatAskReq is the assistant-to-assistant consult (docs/log/19): an af_write orchestrator's
// ask_assistant tool posts here to get a specialist's advice.
type chatAskReq struct {
	Assistant string `json:"assistant"` // id or exact name of the assistant to consult
	Prompt    string `json:"prompt"`
}

// handleChatAsk runs ONE advisory turn with the named assistant and returns its reply
// text. The consult is stateless and forced tools=none: with no tool grant the sub-turn
// attaches no MCP, so it cannot write to sessions and cannot itself call ask_assistant —
// recursion and privilege-escalation are ruled out by construction (single hop, advice
// only). Reached only via the local Agent REST (mcp_stdio → localhost); not CP-exposed.
func handleChatAsk(w http.ResponseWriter, r *http.Request) {
	var req chatAskReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatPromptEmpty, "prompt is empty")
		return
	}
	a, err := assistants.Resolve(strings.TrimSpace(req.Assistant), assistantDeps())
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatAssistantNotFound, "assistant not found")
		return
	}
	// Ephemeral, non-persisted conversation carrying the assistant's persona/model/knowledge
	// but NO tools (advisory only).
	c := &chatConversation{
		ID: randUUID(), Agent: a.Agent, Model: resolveChatModel(a.Agent, a.Model),
		Persona: a.Persona, Tools: assistants.ToolsNone, Knowledge: a.Knowledge,
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	// 使用量台帳（ADR 0029 §3）。会話は非永続なので ref は空 — 束ねる先が無い。
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureAssistantAsk, Trigger: usagex.TriggerUser})
	reply, err := prov.send(ctx, c, prompt)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistant": a.Name, "reply": reply})
}

func handleChatGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	c.InProgress = turnInFlight(id) // transient: lets a reloaded client poll for a still-running reply
	httpx.WriteJSON(w, http.StatusOK, c)
}

// handleChatStop cancels a conversation's in-flight assistant turn (the Stop button).
// The streaming turn is detached from its request connection, so aborting the SSE fetch
// no longer stops it — this explicit cancel does. Idempotent: reports whether a running
// turn was found, but always succeeds.
func handleChatStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatConversationNotFnd, "invalid conversation id")
		return
	}
	stopped := cancelLiveTurn(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": stopped})
}

// chatPatchReq carries the mutable conversation settings. Pointers distinguish "absent"
// from "empty": a client that only renames must not clear the agent, and vice versa.
type chatPatchReq struct {
	Title *string `json:"title,omitempty"`
	Agent *string `json:"agent,omitempty"`
}

// handleChatPatch updates a conversation in place (docs/log/19):
//   - title: the auto-title from the first message is often not what the user wants once
//     the thread has a topic.
//   - agent: switch the backend CLI mid-thread. 設定 > アシスタントの「エージェント優先順位」は
//     新規会話と one-shot にしか効かない（会話は作成時にエージェントをスナップショットする）
//     ので、進行中の会話を別 CLI へ移す明示的な入口がここ。切替そのものは会話ファイルの
//     ピン留めを差し替えるだけで、次の送信で syncProviderPrompt が「新バックエンドがまだ
//     知らない履歴」を再生する（認証フォールバックと同じ経路 — chat_provider_context.go）。
func handleChatPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatPatchReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Title == nil && req.Agent == nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "nothing to update")
		return
	}
	var title string
	if req.Title != nil {
		var ok bool
		title, ok = cleanTitle(*req.Title) // same control-char/length gate as a session rename
		if !ok || title == "" {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatTitleEmpty, "display name is empty")
			return
		}
	}
	if req.Agent != nil {
		if _, ok := chatProviders[*req.Agent]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatAgentUnsupported, "unsupported agent")
			return
		}
		// 実行中のターンは切替の対象外（handleChatDelete と同じ作法）。ストリームは会話
		// ロックをターン全体で握るので、待たせると数分ブロックしたうえ、走っている
		// プロバイダと保存されるピン留めが食い違ったまま終わる。
		if turnInFlight(id) {
			httpx.WriteErr(w, http.StatusConflict, "conversation_busy",
				"a turn is in progress — stop it before switching the agent")
			return
		}
	}
	unlock := lockConv(id) // serialize with an in-flight turn's load-modify-save
	defer unlock()
	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if req.Title != nil {
		c.Title = title
	}
	if req.Agent != nil {
		switchChatAgent(c, *req.Agent)
	}
	c.UpdatedAt = nowMs()
	if err := saveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// switchChatAgent re-pins a conversation to another backend. Idempotent: re-selecting the
// current agent changes nothing (no notice, no model churn).
//
// Model は作成時エージェント基準の1本しか持たないので、切替時に新しい CLI の設定
// （設定 > アシスタント「アシスタントのモデル」）から解決し直す — 持ち越すと別 CLI に他社の
// モデル id を渡すことになる。resume ハンドルとバックエンド毎のメッセージカーソルは残す:
// 元のエージェントへ戻したときに、そのエージェントの native セッションを続きから使え、
// 空いた分だけが再生される。ActiveAgent も更新して、次のターンを待たずにヘッダが
// 切替後のエージェントを映すようにする（明示的な選択は最後のターンより新しい事実）。
func switchChatAgent(c *chatConversation, kind string) {
	if c.Agent == kind {
		return
	}
	c.Agent = kind
	c.ActiveAgent = kind
	c.Model = resolveChatModel(kind, "")
	c.Messages = append(c.Messages, newNotice(noticeKeyAgentSwitched, map[string]string{"agent": kind},
		"エージェントを "+kind+" に切り替えました。これまでの会話はそのまま引き継がれます。"))
}

func handleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	// 実行中ターンと直列化: ロック無しで消すと、ターン末尾の saveConv が削除済み
	// 会話（と資格情報入りの会話別 MCP config）を再作成してゾンビ化する。実行中は
	// 数分待たせるより 409 で先に止めてもらう（Stop してから削除）。
	if turnInFlight(id) {
		httpx.WriteErr(w, http.StatusConflict, "conversation_busy",
			"a turn is in progress — stop it before deleting")
		return
	}
	unlock := lockConv(id)
	defer unlock()
	// 削除ロック（docs/log/45）: ロック中の会話は消さない。読めない会話は素通り
	// （下の os.Remove が not-exist を許容するのと同じで、掃除は続けたい）。
	if c, err := loadConv(id); err == nil && c.Locked {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLocked,
			"conversation is locked against deletion; unlock it first")
		return
	}
	if err := os.Remove(convPath(id)); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_delete", err.Error())
		return
	}
	// agy chats get a private working dir (agyChatDir) and opencode chats a private
	// config file (opencodeChatConfig) — drop both with the thread. validConvID above
	// already blocked traversal in id. Best-effort; a no-op for conversations on other
	// backends.
	chatWD := filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd")
	_ = os.RemoveAll(filepath.Join(chatWD, "agy-"+id))
	_ = os.Remove(filepath.Join(chatWD, "opencode-conv", id+".json"))
	// claude chats get a per-conversation --mcp-config file (docs/log/48 P2) — it holds
	// the attached servers' credentials, so it goes with the thread rather than
	// lingering until the next container rebuild.
	removeChatMCPConfig(id)
	// get_session_output の会話別カーソル（mcp_stdio.go outputCursors）も一緒に落とす。
	mcpx.OutputCursors.Remove(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type chatSendReq struct {
	Content string `json:"content"`
}

func handleChatSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatSendReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatMessageEmpty, "message is empty")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	actualAgent := chatProviderKind(c, prov)

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: content, TS: nowMs()})
	// docs/log/33 第4段: 閾値超過のまま新ターンに入るなら、先に予防的自動圧縮（成功
	// すれば直後の injectHandoff がその要約を乗せる）。
	maybeAutoCompact(r.Context(), c, prov)
	// docs/log/30: reports that never got their own auto turn ride the next prompt, and a
	// user message resets the unattended auto-turn budget.
	prompt, pendingReports := injectPendingReports(c, content)
	// docs/log/33: a compaction summary rides the NEW session's first prompt, outermost.
	prompt, handoff := injectCarryover(c, actualAgent, prompt)
	prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
	c.AutoTurns, c.AutoPausedNotified = 0, false

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, chatTurnUsageTag(c, usagex.TriggerUser)) // 使用量台帳（ADR 0029 §3）
	reply, err := prov.send(ctx, c, prompt)
	if err != nil && recoverForRetry(ctx, c, prov, err) {
		// docs/log/33 第3段: 超過を検知 → 現行セッションを要約して畳み、新セッションで
		// リトライ。reports は未配信なので再注入され、要約も前置される。
		prompt, pendingReports = injectPendingReports(c, content)
		prompt, handoff = injectCarryover(c, actualAgent, prompt)
		prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		reply, err = prov.send(ctx, c, prompt)
	}
	if err != nil {
		if isContextOverflowErr(err) {
			noteContextOverflow(c) // black hole を塞ぐ（圧縮も不能だった）
		}
		// Persist the user turn + resume handle even on failure so a retry continues.
		c.UpdatedAt = nowMs()
		_ = saveConv(c)
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	markReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = "" // carried into the new session — done
	}

	assistant := chatMessage{Role: "assistant", Content: reply, Agent: actualAgent, Model: c.turnModel, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
	c.ActiveAgent = actualAgent
	markProviderSynced(c, actualAgent, len(c.Messages))
	noteContextPressure(c) // 逼迫時は notice を追記（chat_usage.go）
	c.UpdatedAt = nowMs()
	if err := saveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": assistant, "conversation": c})
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
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatMessageEmpty, "message is empty")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	actualAgent := chatProviderKind(c, prov)

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: content, TS: nowMs()})
	c.AutoTurns, c.AutoPausedNotified = 0, false

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
	// Tell the Console which backend is actually producing this turn before the first
	// token arrives, so the live bubble/header switches immediately on fallback.
	emit(map[string]any{"agent": actualAgent})

	// Detach the turn from the request context (WithoutCancel) so a browser reload —
	// which aborts this SSE request — doesn't cancel the provider or lose the reply:
	// the turn keeps running, saves below, and a reconnecting client re-attaches via
	// in_progress polling. An EXPLICIT stop still cancels it through the registered
	// cancel func (handleChatStop); the bounded chatTimeout caps a runaway turn.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), chatTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, chatTurnUsageTag(c, usagex.TriggerUser)) // 使用量台帳（ADR 0029 §3）
	deregister := registerLiveTurn(id, cancel)
	defer deregister()

	var reply string
	var steps []chatStep
	runTurn := func(p string) {
		steps = nil
		if sp, ok := prov.(streamingProvider); ok {
			reply, steps, err = sp.sendStream(ctx, c, p, func(ev chatStreamEvent) {
				if ev.Step != nil {
					emit(map[string]any{"step": ev.Step}) // a completed 作業過程 item
				} else if ev.Delta != "" {
					emit(map[string]string{"delta": ev.Delta})
				}
			})
		} else {
			reply, err = prov.send(ctx, c, p)
			if err == nil {
				emit(map[string]string{"delta": reply})
			}
		}
	}
	// docs/log/33 第4段: 閾値超過のまま新ターンに入るなら、先に予防的自動圧縮（detached
	// ctx 上なのでリロードでも中断されない）。成功すれば下の injectHandoff が要約を
	// 乗せる。プロンプト構築は圧縮の後（PendingHandoff 反映後）でなければならない。
	maybeAutoCompact(ctx, c, prov)
	// docs/log/30: undelivered session reports ride this prompt; docs/log/33: a compaction
	// summary rides the NEW session's first prompt, outermost.
	prompt, pendingReports := injectPendingReports(c, content)
	prompt, handoff := injectCarryover(c, actualAgent, prompt)
	prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
	runTurn(prompt)
	if err != nil && recoverForRetry(ctx, c, prov, err) {
		// docs/log/33 第3段: 超過を検知 → 現行セッションを要約して畳み、新セッションで
		// リトライ。超過エラーは初回送信直後の 400 なので delta 未発火＝二重表示なし。
		prompt, pendingReports = injectPendingReports(c, content)
		prompt, handoff = injectCarryover(c, actualAgent, prompt)
		prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		runTurn(prompt)
	}
	if err != nil {
		if isContextOverflowErr(err) {
			noteContextOverflow(c) // black hole を塞ぐ（圧縮も不能だった）
		}
		c.UpdatedAt = nowMs()
		_ = saveConv(c) // persist the user turn + resume handle so a retry continues
		emit(map[string]any{"error": err.Error()})
		return
	}
	markReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = "" // carried into the new session — done
	}

	assistant := chatMessage{Role: "assistant", Content: reply, Steps: steps, Agent: actualAgent, Model: c.turnModel, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
	c.ActiveAgent = actualAgent
	markProviderSynced(c, actualAgent, len(c.Messages))
	noteContextPressure(c) // 逼迫時は notice を追記（chat_usage.go）— done の conversation で届く
	c.UpdatedAt = nowMs()
	_ = saveConv(c)
	emit(map[string]any{"done": true, "message": assistant, "conversation": c})
}
