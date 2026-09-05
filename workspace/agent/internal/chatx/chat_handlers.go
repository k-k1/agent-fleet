package chatx

// HTTP handlers for assistant chat: list, create, get, rename, delete, send, stream and
// consult.

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

func HandleChatList(w http.ResponseWriter, r *http.Request) {
	metas, err := ListConvs()
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

// VerbPersona is the persona-embedded instruction for an ad-hoc Files verb (docs/log/30
// item 2). A translate/summarize chat opened from Files carries its own persona instead of
// pointing at a standing translate/general-purpose assistant, so those builtins could be
// removed with no loss. Any other verb ("") falls through to the generic chatPersona.
//
// docs/log/28 P6: the user is the one reading what comes out (a translation, a summary), so
// the persona is written in the display language. The translation language pair stays
// Japanese↔English regardless of the display language: which direction to translate follows
// from the input, not from the Console's display language (languageRule likewise exempts
// translate alone).
func VerbPersona(verb, lang string) string {
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

// SeedFor composes the first-turn prompt for an attached file/dir. The absolute path is
// used verbatim so the assistant's Read (scoped by --add-dir) resolves it directly. It is
// shown as-is as the conversation's first message, looking as though the user typed it, so
// like the persona it is written in the display language.
func SeedFor(verb, abs string, isDir bool, lang string) string {
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

// ChatDefaultTitle is the fallback name for a conversation created without one. The
// Console sends its own (catalog "chat.new_title"), so this covers the other creators —
// MCP / schedules / the bridge — and follows the display language for the same reason.
func ChatDefaultTitle(lang string) string {
	if lang == "en" {
		return "New chat"
	}
	return "新しいチャット"
}

func AppendUniqueStr(ss []string, v string) []string {
	for _, s := range ss {
		if s == v {
			return ss
		}
	}
	return append(ss, v)
}

func HandleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req chatCreateReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	lang := uiprefs.Locale()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = ChatDefaultTitle(lang)
	}
	now := NowMs()
	c := &ChatConversation{
		ID: RandUUID(), Slug: NewConvSlug(), Title: title, CreatedAt: now, UpdatedAt: now, Messages: []ChatMessage{},
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
		c.Model = ResolveChatModel(a.Agent, a.Model)
		c.Persona = a.Persona
		c.Tools = a.Tools
		c.Knowledge = a.Knowledge
		c.Integrations = a.Integrations
	case VerbPersona(req.SeedVerb, lang) != "":
		// Ad-hoc persona-embedded verb (docs/log/30 item 2): a translate/summarize action from
		// Files opens a standalone chat carrying the verb persona directly - there is no
		// standing translate/general-purpose assistant to point at.
		// Read-only (the attached file arrives via knowledge --add-dir below); SeedVerb is
		// persisted so languageRule() keeps a translate thread language-agnostic.
		c.Agent = PreferredHeadlessAgent()
		c.Model = ResolveChatModel(c.Agent, "")
		c.Persona = VerbPersona(req.SeedVerb, lang)
		c.Tools = assistants.ToolsNone
		c.SeedVerb = req.SeedVerb
	default:
		// Legacy path: plain agent + optional model, generic persona, read-only fleet tools
		// for claude (mirrors the pre-assistant default).
		if _, ok := ChatProviders[req.Agent]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatAgentUnsupported, "unsupported agent")
			return
		}
		c.Agent = req.Agent
		c.Model = ResolveChatModel(req.Agent, req.Model)
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
				c.Knowledge = AppendUniqueStr(c.Knowledge, dir)
				seed = SeedFor(req.SeedVerb, full, fi.IsDir(), lang)
			}
		}
	}

	if err := SaveConv(c); err != nil {
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

// HandleChatAsk runs ONE advisory turn with the named assistant and returns its reply
// text. The consult is stateless and forced tools=none: with no tool grant the sub-turn
// attaches no MCP, so it cannot write to sessions and cannot itself call ask_assistant —
// recursion and privilege-escalation are ruled out by construction (single hop, advice
// only). Reached only via the local Agent REST (mcp_stdio → localhost); not CP-exposed.
func HandleChatAsk(w http.ResponseWriter, r *http.Request) {
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
	c := &ChatConversation{
		ID: RandUUID(), Agent: a.Agent, Model: ResolveChatModel(a.Agent, a.Model),
		Persona: a.Persona, Tools: assistants.ToolsNone, Knowledge: a.Knowledge,
	}
	prov := ChatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	// Usage ledger (ADR 0029 §3). The conversation is not persisted, so ref stays empty:
	// there is nothing to attribute it to.
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureAssistantAsk, Trigger: usagex.TriggerUser})
	reply, err := prov.Send(ctx, c, prompt)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistant": a.Name, "reply": reply})
}

func HandleChatGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	c.InProgress = TurnInFlight(id) // transient: lets a reloaded client poll for a still-running reply
	httpx.WriteJSON(w, http.StatusOK, c)
}

// HandleChatStop cancels a conversation's in-flight assistant turn (the Stop button).
// The streaming turn is detached from its request connection, so aborting the SSE fetch
// no longer stops it — this explicit cancel does. Idempotent: reports whether a running
// turn was found, but always succeeds.
func HandleChatStop(w http.ResponseWriter, r *http.Request) {
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

// HandleChatPatch updates a conversation in place (docs/log/19):
//   - title: the auto-title from the first message is often not what the user wants once
//     the thread has a topic.
//   - agent: switch the backend CLI mid-thread. The agent priority under Settings >
//     Assistants applies only to new conversations and one-shots, because a conversation
//     snapshots its agent at creation, so this is the explicit way to move a running
//     conversation to another CLI. The switch itself only replaces the pin in the
//     conversation file; on the next send syncProviderPrompt replays the history the new
//     backend has not seen yet (the same path as the auth fallback,
//     chat_provider_context.go).
func HandleChatPatch(w http.ResponseWriter, r *http.Request) {
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
		if _, ok := ChatProviders[*req.Agent]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeChatAgentUnsupported, "unsupported agent")
			return
		}
		// A turn in flight is out of scope for a switch, as in HandleChatDelete. The stream
		// holds the conversation lock for the whole turn, so waiting blocks for minutes and
		// still ends with the running provider and the saved pin disagreeing.
		if TurnInFlight(id) {
			httpx.WriteErr(w, http.StatusConflict, "conversation_busy",
				"a turn is in progress — stop it before switching the agent")
			return
		}
	}
	unlock := LockConv(id) // serialize with an in-flight turn's load-modify-save
	defer unlock()
	c, err := LoadConv(id)
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
	c.UpdatedAt = NowMs()
	if err := SaveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// switchChatAgent re-pins a conversation to another backend. Idempotent: re-selecting the
// current agent changes nothing (no notice, no model churn).
//
// A conversation carries only ONE Model, keyed to the agent it was created with, so on a
// switch it is re-resolved from the new CLI's setting (the assistant model under Settings >
// Assistants); carrying it over would hand one CLI another vendor's model id. The resume
// handle and the per-backend message cursors are kept: switching back lets that agent's
// native session continue where it left off, replaying only the gap. ActiveAgent is updated
// too so the header reflects the new agent without waiting for the next turn - an explicit
// choice is a more recent fact than the last turn.
func switchChatAgent(c *ChatConversation, kind string) {
	if c.Agent == kind {
		return
	}
	c.Agent = kind
	c.ActiveAgent = kind
	c.Model = ResolveChatModel(kind, "")
	c.Messages = append(c.Messages, newNotice(noticeKeyAgentSwitched, map[string]string{"agent": kind},
		"エージェントを "+kind+" に切り替えました。これまでの会話はそのまま引き継がれます。"))
}

func HandleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	// Serialize with an in-flight turn: deleting without the lock lets the saveConv at the end
	// of that turn recreate the deleted conversation (and its per-conversation MCP config,
	// credentials included) as a zombie. While a turn runs, return 409 and have the user stop
	// it rather than blocking for minutes.
	if TurnInFlight(id) {
		httpx.WriteErr(w, http.StatusConflict, "conversation_busy",
			"a turn is in progress — stop it before deleting")
		return
	}
	unlock := LockConv(id)
	defer unlock()
	// Deletion lock (docs/log/45): a locked conversation is not deleted. An unreadable one
	// passes through, for the same reason the os.Remove below tolerates not-exist: cleanup
	// should still proceed.
	if c, err := LoadConv(id); err == nil && c.Locked {
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
	// Drop get_session_output's per-conversation cursor (mcp_stdio.go outputCursors) too.
	mcpx.OutputCursors.Remove(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type chatSendReq struct {
	Content string `json:"content"`
}

func HandleChatSend(w http.ResponseWriter, r *http.Request) {
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

	unlock := LockConv(id)
	defer unlock()

	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	prov := ChatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	actualAgent := ChatProviderKind(c, prov)

	c.Messages = append(c.Messages, ChatMessage{Role: "user", Content: content, TS: NowMs()})
	// docs/log/33 stage 4: if we would enter a new turn still over the threshold, compact
	// pre-emptively first; on success the injectHandoff right below carries that summary.
	MaybeAutoCompact(r.Context(), c, prov)
	// docs/log/30: reports that never got their own auto turn ride the next prompt, and a
	// user message resets the unattended auto-turn budget.
	prompt, pendingReports := InjectPendingReports(c, content)
	// docs/log/33: a compaction summary rides the NEW session's first prompt, outermost.
	prompt, handoff := InjectCarryover(c, actualAgent, prompt)
	prompt = SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
	c.AutoTurns, c.AutoPausedNotified = 0, false

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, chatTurnUsageTag(c, usagex.TriggerUser)) // usage ledger (ADR 0029 §3)
	reply, err := prov.Send(ctx, c, prompt)
	if err != nil && RecoverForRetry(ctx, c, prov, err) {
		// docs/log/33 stage 3: overflow detected, so summarize and fold the current session
		// and retry on a new one. The reports are still undelivered, so they are re-injected
		// and the summary is prepended as well.
		prompt, pendingReports = InjectPendingReports(c, content)
		prompt, handoff = InjectCarryover(c, actualAgent, prompt)
		prompt = SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		reply, err = prov.Send(ctx, c, prompt)
	}
	if err != nil {
		if IsContextOverflowErr(err) {
			NoteContextOverflow(c) // close the black hole (even compaction was impossible)
		}
		// Persist the user turn + resume handle even on failure so a retry continues.
		c.UpdatedAt = NowMs()
		_ = SaveConv(c)
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	MarkReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = "" // carried into the new session — done
	}

	assistant := ChatMessage{Role: "assistant", Content: reply, Agent: actualAgent, Model: c.TurnModel, TS: NowMs()}
	c.Messages = append(c.Messages, assistant)
	c.ActiveAgent = actualAgent
	MarkProviderSynced(c, actualAgent, len(c.Messages))
	NoteContextPressure(c) // appends a notice when context is tight (chat_usage.go)
	c.UpdatedAt = NowMs()
	if err := SaveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": assistant, "conversation": c})
}

// HandleChatStream is the streaming (Phase B) form of send: it runs the provider
// with token streaming and forwards deltas as Server-Sent Events. Frames:
//
//	data: {"delta":"<text>"}                      — an incremental chunk
//	data: {"error":"<msg>"}                       — provider/exec failure
//	data: {"done":true,"message":…,"conversation":…} — final turn saved
//
// Providers without a streaming variant fall back to a single delta (the full reply).
func HandleChatStream(w http.ResponseWriter, r *http.Request) {
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

	unlock := LockConv(id)
	defer unlock()

	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	prov := ChatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	actualAgent := ChatProviderKind(c, prov)

	c.Messages = append(c.Messages, ChatMessage{Role: "user", Content: content, TS: NowMs()})
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
	ctx = usagex.WithTag(ctx, chatTurnUsageTag(c, usagex.TriggerUser)) // usage ledger (ADR 0029 §3)
	deregister := RegisterLiveTurn(id, cancel)
	defer deregister()

	var reply string
	var steps []chatStep
	runTurn := func(p string) {
		steps = nil
		if sp, ok := prov.(streamingProvider); ok {
			reply, steps, err = sp.SendStream(ctx, c, p, func(ev ChatStreamEvent) {
				if ev.Step != nil {
					emit(map[string]any{"step": ev.Step}) // a completed work-trail item
				} else if ev.Delta != "" {
					emit(map[string]string{"delta": ev.Delta})
				}
			})
		} else {
			reply, err = prov.Send(ctx, c, p)
			if err == nil {
				emit(map[string]string{"delta": reply})
			}
		}
	}
	// docs/log/33 stage 4: if we would enter a new turn still over the threshold, compact
	// pre-emptively first; it runs on the detached ctx, so a reload does not abort it, and on
	// success the injectHandoff below carries the summary. The prompt must be built after the
	// compaction, i.e. once PendingHandoff is set.
	MaybeAutoCompact(ctx, c, prov)
	// docs/log/30: undelivered session reports ride this prompt; docs/log/33: a compaction
	// summary rides the NEW session's first prompt, outermost.
	prompt, pendingReports := InjectPendingReports(c, content)
	prompt, handoff := InjectCarryover(c, actualAgent, prompt)
	prompt = SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
	runTurn(prompt)
	if err != nil && RecoverForRetry(ctx, c, prov, err) {
		// docs/log/33 stage 3: overflow detected, so summarize and fold the current session
		// and retry on a new one. The overflow error is a 400 arriving right after the first
		// send, so no delta has fired yet and nothing is displayed twice.
		prompt, pendingReports = InjectPendingReports(c, content)
		prompt, handoff = InjectCarryover(c, actualAgent, prompt)
		prompt = SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		runTurn(prompt)
	}
	if err != nil {
		if IsContextOverflowErr(err) {
			NoteContextOverflow(c) // close the black hole (even compaction was impossible)
		}
		c.UpdatedAt = NowMs()
		_ = SaveConv(c) // persist the user turn + resume handle so a retry continues
		emit(map[string]any{"error": err.Error()})
		return
	}
	MarkReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = "" // carried into the new session — done
	}

	assistant := ChatMessage{Role: "assistant", Content: reply, Steps: steps, Agent: actualAgent, Model: c.TurnModel, TS: NowMs()}
	c.Messages = append(c.Messages, assistant)
	c.ActiveAgent = actualAgent
	MarkProviderSynced(c, actualAgent, len(c.Messages))
	NoteContextPressure(c) // appends a notice when context is tight (chat_usage.go); it reaches the client on the done conversation
	c.UpdatedAt = NowMs()
	_ = SaveConv(c)
	emit(map[string]any{"done": true, "message": assistant, "conversation": c})
}
