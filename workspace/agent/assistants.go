package main

// アシスタント層のうち **main にしか置けない 2 つ**がここに残る（実体は internal/assistants・
// 別名は alias_assistants.go）:
//
//   - //go:embed の組み込みナレッジ。embed のパスは相対で `..` を書けず、
//     `workspace/agent/knowledge/af-usage.md` は .dockerignore（`**/*.md` を落として
//     `agent/knowledge/*.md` だけ戻している）と scripts/docs-check.py が直接見ているので、
//     ディレクトリごと動かすと Docker ビルドと docs ワークフローが壊れる。
//   - HTTP ハンドラ。errcodes.go の errCodeAssistant* と chat 家系（chatProviders /
//     nowMs / randUUID / validConvID / appendUniqueStr）に依存していて、どちらも所有外。

import (
	"embed"
	"errors"

	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

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

// --- HTTP handlers ---

func handleAssistantsList(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistants": listAssistants()})
}

func handleAssistantGet(w http.ResponseWriter, r *http.Request) {
	a, err := getAssistant(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeAssistantNotFound, "assistant not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

// assistantInput is the create/update body (only user-editable fields).
type assistantInput struct {
	Name         string   `json:"name"`
	Icon         string   `json:"icon"`
	Description  string   `json:"description"`
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
	Persona      string   `json:"persona"`
	Tools        string   `json:"tools"`
	Knowledge    []string `json:"knowledge"`
	Integrations []string `json:"integrations"`
	Voice        string   `json:"voice"`
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
	integrations := []string{}
	for _, id := range in.Integrations {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !validIntegration(id) {
			return errors.New("未対応の連携です: " + id)
		}
		integrations = appendUniqueStr(integrations, id)
	}
	a.Name = name
	a.Icon = strings.TrimSpace(in.Icon)
	a.Description = strings.TrimSpace(in.Description)
	a.Agent = in.Agent
	a.Model = strings.TrimSpace(in.Model)
	a.Persona = strings.TrimSpace(in.Persona)
	a.Tools = tools
	a.Knowledge = in.Knowledge
	a.Integrations = integrations
	a.Voice = strings.TrimSpace(in.Voice)
	return nil
}

func handleAssistantCreate(w http.ResponseWriter, r *http.Request) {
	var in assistantInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	now := nowMs()
	a := &assistant{ID: randUUID(), Builtin: false, CreatedAt: now, UpdatedAt: now}
	if err := applyInput(a, in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := saveUserAssistant(a); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func handleAssistantUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if isBuiltinID(id) {
		httpx.WriteErr(w, http.StatusForbidden, errCodeAssistantBuiltinEdit, "builtin assistants cannot be edited")
		return
	}
	a, err := loadUserAssistant(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeAssistantNotFound, "assistant not found")
		return
	}
	var in assistantInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := applyInput(a, in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	a.UpdatedAt = nowMs()
	if err := saveUserAssistant(a); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func handleAssistantDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if isBuiltinID(id) {
		httpx.WriteErr(w, http.StatusForbidden, errCodeAssistantBuiltinDelete, "builtin assistants cannot be deleted")
		return
	}
	if !validConvID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	if err := os.Remove(assistantPath(id)); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
