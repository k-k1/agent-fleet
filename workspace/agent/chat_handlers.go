package main

// アシスタントチャットの HTTP ハンドラ（一覧・作成・取得・改名・削除・送信・ストリーム・consult）。
// chat.go からの機械的分割（docs/23 残②）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	// Ad-hoc attach (docs/19 Phase C): a Files right-click can hand a file/dir to the new
	// chat. AttachPath is browse-root-relative (resolved + denylisted via safeBrowsePath);
	// its dir is added to the conversation's knowledge so the assistant can read it, and a
	// seed prompt (below) is composed server-side with the absolute path.
	AttachPath string `json:"attach_path"`
	SeedVerb   string `json:"seed_verb"` // "translate" | "summarize" | "" (open-ended ask)
}

// seedFor composes the first-turn prompt for an attached file/dir. The absolute path is
// used verbatim so the assistant's Read (scoped by --add-dir) resolves it directly.
func seedFor(verb, abs string, isDir bool) string {
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
			httpx.WriteErr(w, http.StatusBadRequest, "bad_assistant", "アシスタントが見つかりません")
			return
		}
		c.AssistantID = a.ID
		c.Agent = a.Agent
		c.Model = a.Model
		c.Persona = a.Persona
		c.Tools = a.Tools
		c.Knowledge = a.Knowledge
		c.Integrations = a.Integrations
	} else {
		// Legacy path: plain agent + optional model, generic persona, read-only fleet tools
		// for claude (mirrors the pre-assistant default).
		if _, ok := chatProviders[req.Agent]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_agent", "未対応のエージェントです")
			return
		}
		c.Agent = req.Agent
		c.Model = strings.TrimSpace(req.Model)
		if req.Agent == session.KindClaude {
			c.Tools = toolsAFRead
		} else {
			c.Tools = toolsNone
		}
	}

	// Ad-hoc attach from a Files right-click (docs/19 Phase C): resolve the target safely,
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
				seed = seedFor(req.SeedVerb, full, fi.IsDir())
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

// chatAskReq is the assistant-to-assistant consult (docs/19): an af_write orchestrator's
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
		httpx.WriteErr(w, http.StatusBadRequest, "empty", "prompt が空です")
		return
	}
	a, err := resolveAssistant(strings.TrimSpace(req.Assistant))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "アシスタントが見つかりません")
		return
	}
	// Ephemeral, non-persisted conversation carrying the assistant's persona/model/knowledge
	// but NO tools (advisory only).
	c := &chatConversation{
		ID: randUUID(), Agent: a.Agent, Model: a.Model,
		Persona: a.Persona, Tools: toolsNone, Knowledge: a.Knowledge,
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	reply, err := prov.send(ctx, c, prompt)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistant": a.Name, "reply": reply})
}

func handleChatGet(w http.ResponseWriter, r *http.Request) {
	c, err := loadConv(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

type chatRenameReq struct {
	Title string `json:"title"`
}

// handleChatRename changes a conversation's display title (docs/19): the auto-title from
// the first message is often not what the user wants once the thread has a topic.
func handleChatRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatRenameReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty", "表示名が空です")
		return
	}
	unlock := lockConv(id) // serialize with an in-flight turn's load-modify-save
	defer unlock()
	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	c.Title = title
	c.UpdatedAt = nowMs()
	if err := saveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func handleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validConvID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	if err := os.Remove(convPath(id)); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_delete", err.Error())
		return
	}
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
		httpx.WriteErr(w, http.StatusBadRequest, "empty", "メッセージが空です")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: content, TS: nowMs()})

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	reply, err := prov.send(ctx, c, content)
	if err != nil {
		// Persist the user turn + resume handle even on failure so a retry continues.
		c.UpdatedAt = nowMs()
		_ = saveConv(c)
		httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
		return
	}

	assistant := chatMessage{Role: "assistant", Content: reply, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
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
		httpx.WriteErr(w, http.StatusBadRequest, "empty", "メッセージが空です")
		return
	}

	unlock := lockConv(id)
	defer unlock()

	c, err := loadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "会話が見つかりません")
		return
	}
	prov := chatProviderFor(c) // pinned agent, or the available fallback (claude-less WS)

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
	var steps []chatStep
	if sp, ok := prov.(streamingProvider); ok {
		reply, steps, err = sp.sendStream(ctx, c, content, func(ev chatStreamEvent) {
			if ev.Step != nil {
				emit(map[string]any{"step": ev.Step}) // a completed 作業過程 item
			} else if ev.Delta != "" {
				emit(map[string]string{"delta": ev.Delta})
			}
		})
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

	assistant := chatMessage{Role: "assistant", Content: reply, Steps: steps, TS: nowMs()}
	c.Messages = append(c.Messages, assistant)
	c.UpdatedAt = nowMs()
	_ = saveConv(c)
	emit(map[string]any{"done": true, "message": assistant, "conversation": c})
}
