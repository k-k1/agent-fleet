package main

// The three parts of the assistant layer that can only live in main (the substance is in
// internal/assistants, called directly):
//
//   - The //go:embed builtin knowledge. An embed path is relative and cannot say `..`, and
//     `workspace/agent/knowledge/af-usage.md` is read directly by .dockerignore (which drops
//     `**/*.md` and adds `agent/knowledge/*.md` back) and by scripts/docs-check.py, so
//     moving the directory breaks the Docker build and the docs workflow.
//   - The HTTP handlers. They depend on errcodes.go's errCodeAssistant* and on the chat
//     family (chatProviders / nowMs / randUUID / appendUniqueStr), neither of them owned
//     here.
//   - `assistantDeps()` (at the bottom), the one place the Deps handed to
//     internal/assistants is assembled — a DI construction point, not an alias.

import (
	"embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
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
	_ = os.WriteFile(filepath.Join(dir, "agent-fleet-version.md"), []byte(runningVersionDoc(buildVersion)), 0o600)
	return dir
}

// runningVersionDoc tells the builtin assistants which release this workspace runs, so
// "what changed since my version" can be answered against the release history in the
// guide (ref/releases). It is the Agent's own build, i.e. the workspace image: the
// Control Plane may already be newer (the "restart required" badge), which is why the
// text says "this workspace" rather than "this deployment".
func runningVersionDoc(v string) string {
	// Dev images are stamped "dev" or "<next>-dev-<sha>"; neither names a published release.
	if v == "" || strings.Contains(v, "dev") {
		label := ""
		if v != "" && v != "dev" {
			label = " (" + v + ")"
		}
		return "# Running version\n\n" +
			"This workspace runs a development build" + label + ", not a published release. It may contain " +
			"changes newer than the latest entry in the release history; say so rather " +
			"than naming a release.\n\n" +
			"このワークスペースは開発ビルド" + label + "で動いていて、公開されたリリースではありません。" +
			"更新履歴の最新版より新しい変更を含むことがあるので、版を断定せずそう伝えてください。\n"
	}
	return "# Running version\n\n" +
		"This workspace runs Agent Fleet " + v + ". The Control Plane can be newer until " +
		"the workspace is restarted.\n\n" +
		"このワークスペースは Agent Fleet " + v + " で動いています。ワークスペースを再起動するまでは、" +
		"Control Plane のほうが新しい版のことがあります。\n"
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

// inputError is applyInput's refusal: a stable code the Console localizes (err.<code>) and an
// English developer message. integration names the rejected id in its own field, because the
// Console shows the localized text and must not append the English message to get at the id.
type inputError struct {
	code        string
	msg         string
	integration string
}

// inputErrorWire is inputError's response body; integration is omitted when empty.
type inputErrorWire struct {
	Error struct {
		Code        string `json:"code"`
		Message     string `json:"message"`
		Integration string `json:"integration,omitempty"`
	} `json:"error"`
}

func (e *inputError) write(w http.ResponseWriter) {
	var body inputErrorWire
	body.Error.Code, body.Error.Message, body.Error.Integration = e.code, e.msg, e.integration
	httpx.WriteJSON(w, http.StatusBadRequest, body)
}

// applyInput validates the input and folds it onto a (new or existing) assistant.
func applyInput(a *assistants.Assistant, in assistantInput) *inputError {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return &inputError{code: errCodeAssistantNameRequired, msg: "name is required"}
	}
	if _, ok := chatx.ChatProviders[in.Agent]; !ok {
		return &inputError{code: errCodeAssistantAgentUnsupported, msg: "unsupported agent: " + in.Agent}
	}
	tools := in.Tools
	if tools == "" {
		tools = assistants.ToolsNone
	}
	if !assistants.ValidToolGrant(tools) {
		return &inputError{code: errCodeAssistantToolsUnsupported, msg: "unsupported tool grant: " + tools}
	}
	integrations := []string{}
	for _, id := range in.Integrations {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !assistants.ValidIntegration(id) {
			return &inputError{code: errCodeAssistantIntegrationUnsupported, msg: "unsupported integration: " + id, integration: id}
		}
		integrations = chatx.AppendUniqueStr(integrations, id)
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
	now := chatx.NowMs()
	a := &assistants.Assistant{ID: chatx.RandUUID(), Builtin: false, CreatedAt: now, UpdatedAt: now}
	if ierr := applyInput(a, in); ierr != nil {
		ierr.write(w)
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
	if ierr := applyInput(a, in); ierr != nil {
		ierr.write(w)
		return
	}
	a.UpdatedAt = chatx.NowMs()
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

// assistantDeps is the only place the main-only things (the //go:embed knowledge, the
// shipped user guide's path and the chat family's default agent) are passed to
// internal/assistants, and they are passed as arguments.
//
// Assignment to package-variable hooks in init was tried first: mutation testing during
// review deleted those two lines and every test in main stayed green, because a dependency
// the compiler used to enforce had become a runtime assignment that can be removed silently.
// A struct with exported fields has the same hole — leave one field out and it still
// compiles. Only NewDeps' positional arguments turn a forgotten dependency into a compile error.
// Do not add assistants calls that bypass this function.
func assistantDeps() assistants.Deps {
	return assistants.NewDeps(
		ensureBuiltinKnowledge,       // //go:embed stays in main
		agentFleetDocsRoot,           // the staged guide (fs.go)
		chatx.PreferredHeadlessAgent, // lives in the chat family
	)
}
