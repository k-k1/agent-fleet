package main

// Assistant templates (docs/19 Q2). An "assistant" is a configurable persona for the
// headless-CLI chat (custom-GPT style): a name, an agent backend, an optional model,
// a system prompt (persona), a tool grant, and optional knowledge dirs. A conversation
// SNAPSHOTS an assistant's settings at creation (see chat.go chatCreate), so later
// edits to the assistant don't retroactively rewrite existing threads.
//
// Two sources merge into one list:
//   - builtins: code-injected, always present, not user-editable/deletable. The flagship
//     is "Agent Fleet アシスタント" (usage guidance, read-only fleet tools, USAGE knowledge).
//   - user-defined: JSON files under ~/.config/agent-fleet/assistants/<id>.json (full CRUD).
//
// Write tools (af_write=send_to_session …) are intentionally NOT a tools value yet;
// they land with the later write-tools opt-in step (docs/19). Today: "none" | "af_read".

import (
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tool grants an assistant can hold. af_read attaches the local read-only stdio MCP
// (docs/19 Q1); af_write additionally exposes the write tools (send_to_session …) by
// starting that MCP server with --write (docs/19 Q2 opt-in).
const (
	toolsNone    = "none"
	toolsAFRead  = "af_read"
	toolsAFWrite = "af_write"
)

func validToolGrant(t string) bool {
	return t == toolsNone || t == toolsAFRead || t == toolsAFWrite
}

// assistant is a chat persona template (builtin or user-defined).
type assistant struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon,omitempty"`    // codicon name for the Console rail
	Builtin   bool     `json:"builtin"`           // code-injected; not editable/deletable
	Agent     string   `json:"agent"`             // "claude" | "codex" — selects the provider
	Model     string   `json:"model,omitempty"`   // CLI --model override
	Persona   string   `json:"persona,omitempty"` // system prompt (--append-system-prompt)
	Tools     string   `json:"tools"`             // "none" | "af_read"
	Knowledge []string `json:"knowledge,omitempty"`
	CreatedAt int64    `json:"created_at,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

// --- builtin knowledge (embedded, materialized to a runtime dir) ---

//go:embed knowledge/af-usage.md
var knowledgeFS embed.FS

// knowledgeDir is where embedded builtin knowledge is materialized so a headless CLI's
// --add-dir has a real path to read. It lives under the chat config tree, outside repos.
func knowledgeDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "knowledge", "af")
}

// ensureBuiltinKnowledge materializes the embedded USAGE doc into knowledgeDir and
// returns it. Idempotent (safe to call every turn) so it self-heals after a container
// rebuild wipes ~/.config while an old conversation still references the path.
func ensureBuiltinKnowledge() string {
	dir := knowledgeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dir
	}
	if b, err := knowledgeFS.ReadFile("knowledge/af-usage.md"); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "agent-fleet-usage.md"), b, 0o600)
	}
	return dir
}

// afAssistantPersona keeps the flagship assistant focused on guiding the user through
// Agent Fleet, grounded in the materialized USAGE knowledge and the read-only tools.
const afAssistantPersona = "あなたは Agent Fleet の利用を案内する専任アシスタントです。" +
	"知識として読み込んだ利用ガイド（agent-fleet-usage.md）を根拠に、使い方・操作手順・運用上の注意を簡潔に案内してください。" +
	"利用者のワークスペースの状態を聞かれたら、推測せず list_my_sessions / get_session_status / get_session_output ツールで実際の状態を確認してから答えてください。" +
	"ファイルの作成・編集やコマンド実行は行わず、案内と説明に徹してください。"

// translatePersona focuses the assistant on faithful translation.
const translatePersona = "あなたは翻訳アシスタントです。" +
	"渡された文章を、指定がなければ日本語↔英語を自動判定して自然に翻訳してください。" +
	"訳文のみを返し、余計な前置きや解説は付けないでください。Markdown の書式は保持します。"

// AF_ASSISTANT_ID is the flagship builtin's stable id (referenced by the Console to
// mark it undeletable and as the default new-chat assistant).
const afAssistantID = "af"

// builtinAssistants returns the code-injected assistants, freshly materializing any
// embedded knowledge. Order here is the display order (flagship first).
func builtinAssistants() []assistant {
	know := ensureBuiltinKnowledge()
	return []assistant{
		{
			ID: afAssistantID, Name: "Agent Fleet アシスタント", Icon: "rocket",
			Builtin: true, Agent: kindClaude, Persona: afAssistantPersona,
			Tools: toolsAFRead, Knowledge: []string{know},
		},
		{
			ID: "general", Name: "汎用アシスタント", Icon: "comment-discussion",
			Builtin: true, Agent: kindClaude, Persona: chatPersona, Tools: toolsNone,
		},
		{
			ID: "translate", Name: "翻訳アシスタント", Icon: "globe",
			Builtin: true, Agent: kindClaude, Persona: translatePersona, Tools: toolsNone,
		},
	}
}

func isBuiltinID(id string) bool {
	for _, a := range builtinAssistants() {
		if a.ID == id {
			return true
		}
	}
	return false
}

// --- user-defined store ---

func assistantsDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "assistants")
}

func assistantPath(id string) string { return filepath.Join(assistantsDir(), id+".json") }

func loadUserAssistant(id string) (*assistant, error) {
	if !validConvID(id) { // user ids are randUUID() like conversation ids — same guard blocks traversal
		return nil, errors.New("invalid assistant id")
	}
	b, err := os.ReadFile(assistantPath(id))
	if err != nil {
		return nil, err
	}
	var a assistant
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	a.Builtin = false // never trust a file that claims builtin
	return &a, nil
}

func saveUserAssistant(a *assistant) error {
	if err := os.MkdirAll(assistantsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(assistantPath(a.ID), append(b, '\n'), 0o600)
}

func listUserAssistants() []assistant {
	ents, err := os.ReadDir(assistantsDir())
	if err != nil {
		return nil
	}
	out := []assistant{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if isBuiltinID(id) {
			continue // a stray file must never shadow a builtin
		}
		if a, err := loadUserAssistant(id); err == nil {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// listAssistants merges builtins (first) with user-defined ones.
func listAssistants() []assistant {
	return append(builtinAssistants(), listUserAssistants()...)
}

// getAssistant resolves an id to a builtin or a user assistant.
func getAssistant(id string) (*assistant, error) {
	for _, a := range builtinAssistants() {
		if a.ID == id {
			b := a
			return &b, nil
		}
	}
	return loadUserAssistant(id)
}

// --- HTTP handlers ---

func handleAssistantsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"assistants": listAssistants()})
}

func handleAssistantGet(w http.ResponseWriter, r *http.Request) {
	a, err := getAssistant(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "アシスタントが見つかりません")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// assistantInput is the create/update body (only user-editable fields).
type assistantInput struct {
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	Agent     string   `json:"agent"`
	Model     string   `json:"model"`
	Persona   string   `json:"persona"`
	Tools     string   `json:"tools"`
	Knowledge []string `json:"knowledge"`
}

// applyInput validates the input and folds it onto a (new or existing) assistant.
func applyInput(a *assistant, in assistantInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("名前を入力してください")
	}
	if _, ok := chatProviders[in.Agent]; !ok {
		return errors.New("未対応のエージェントです")
	}
	tools := in.Tools
	if tools == "" {
		tools = toolsNone
	}
	if !validToolGrant(tools) {
		return errors.New("未対応のツール指定です")
	}
	a.Name = name
	a.Icon = strings.TrimSpace(in.Icon)
	a.Agent = in.Agent
	a.Model = strings.TrimSpace(in.Model)
	a.Persona = strings.TrimSpace(in.Persona)
	a.Tools = tools
	a.Knowledge = in.Knowledge
	return nil
}

func handleAssistantCreate(w http.ResponseWriter, r *http.Request) {
	var in assistantInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	now := nowMs()
	a := &assistant{ID: randUUID(), Builtin: false, CreatedAt: now, UpdatedAt: now}
	if err := applyInput(a, in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := saveUserAssistant(a); err != nil {
		writeErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func handleAssistantUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if isBuiltinID(id) {
		writeErr(w, http.StatusForbidden, "builtin", "ビルトインは編集できません")
		return
	}
	a, err := loadUserAssistant(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "アシスタントが見つかりません")
		return
	}
	var in assistantInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if err := applyInput(a, in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	a.UpdatedAt = nowMs()
	if err := saveUserAssistant(a); err != nil {
		writeErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func handleAssistantDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if isBuiltinID(id) {
		writeErr(w, http.StatusForbidden, "builtin", "ビルトインは削除できません")
		return
	}
	if !validConvID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	if err := os.Remove(assistantPath(id)); err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, "delete", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
