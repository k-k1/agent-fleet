package main

// アシスタント層のうち **main にしか置けない 3 つ**がここに残る（実体は internal/assistants を
// 直接呼ぶ。ウェーブ B の別名 alias_assistants.go は RECLAIM-B で回収済み）:
//
//   - //go:embed の組み込みナレッジ。embed のパスは相対で `..` を書けず、
//     `workspace/agent/knowledge/af-usage.md` は .dockerignore（`**/*.md` を落として
//     `agent/knowledge/*.md` だけ戻している）と scripts/docs-check.py が直接見ているので、
//     ディレクトリごと動かすと Docker ビルドと docs ワークフローが壊れる。
//   - HTTP ハンドラ。errcodes.go の errCodeAssistant* と chat 家系（chatProviders /
//     nowMs / randUUID / appendUniqueStr）に依存していて、どちらも所有外。
//   - `assistantDeps()`（末尾）。internal/assistants へ渡す Deps を組む唯一の入口で、
//     別名ではなく DI の構築点。

import (
	"embed"
	"errors"

	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistants": assistants.List(assistantDeps())})
}

func handleAssistantGet(w http.ResponseWriter, r *http.Request) {
	a, err := assistants.Get(r.PathValue("id"), assistantDeps())
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
func applyInput(a *assistants.Assistant, in assistantInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errors.New("名前を入力してください")
	}
	if _, ok := chatProviders[in.Agent]; !ok {
		return errors.New("未対応のエージェントです")
	}
	tools := in.Tools
	if tools == "" {
		tools = assistants.ToolsNone
	}
	if !assistants.ValidToolGrant(tools) {
		return errors.New("未対応のツール指定です")
	}
	integrations := []string{}
	for _, id := range in.Integrations {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !assistants.ValidIntegration(id) {
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
	a := &assistants.Assistant{ID: randUUID(), Builtin: false, CreatedAt: now, UpdatedAt: now}
	if err := applyInput(a, in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := assistants.SaveUser(a); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func handleAssistantUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if assistants.IsBuiltinID(id, assistantDeps()) {
		httpx.WriteErr(w, http.StatusForbidden, errCodeAssistantBuiltinEdit, "builtin assistants cannot be edited")
		return
	}
	a, err := assistants.LoadUser(id)
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
	if err := assistants.SaveUser(a); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func handleAssistantDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if assistants.IsBuiltinID(id, assistantDeps()) {
		httpx.WriteErr(w, http.StatusForbidden, errCodeAssistantBuiltinDelete, "builtin assistants cannot be deleted")
		return
	}
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	if err := os.Remove(assistants.PathFor(id)); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// assistantDeps は main にしか置けない 2 つ（//go:embed のナレッジと chat 家系の
// 既定エージェント）を internal/assistants へ**引数で**渡す唯一の場所。
//
// 🔥 最初はパッケージ変数のフックに init で代入していたが、レビューの変異試験で
// **その 2 行を消しても main のテストが全部緑**になった（develop ではコンパイラが強制していた
// 依存が、移送で「無言で外せる実行時代入」に化けていた）。公開フィールドの struct でも同じで、
// **片方を書き落としてコンパイルが通る**。引数 2 つの NewDeps にして初めて、渡し忘れが
// コンパイルエラーになる。**この関数を経由しない assistants 呼び出しを増やさないこと。**
func assistantDeps() assistants.Deps {
	return assistants.NewDeps(
		ensureBuiltinKnowledge, // //go:embed が main に残るため
		preferredHeadlessAgent, // chat 家系にあるため
	)
}
