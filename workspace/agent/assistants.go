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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"` // codicon name for the Console rail
	// Description is a short user-facing self-intro shown when a chat is opened but not
	// yet started (the greeting card) — distinct from Persona (the model's instructions).
	Description string   `json:"description,omitempty"`
	Builtin     bool     `json:"builtin"`           // code-injected; not editable/deletable
	Agent       string   `json:"agent"`             // "claude" | "codex" — selects the provider
	Model       string   `json:"model,omitempty"`   // CLI --model override
	Persona     string   `json:"persona,omitempty"` // system prompt (--append-system-prompt)
	Tools       string   `json:"tools"`             // "none" | "af_read" | "af_write"
	Knowledge   []string `json:"knowledge,omitempty"`
	// Voice is the Console-side TTS voice override ("vv:<speaker>" / "polly:<VoiceId>").
	// "" = auto (the Console assigns one from the user's character pool). The agent only
	// stores and echoes it — synthesis and resolution are entirely client-side (docs/24).
	Voice string `json:"voice,omitempty"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
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
const afAssistantPersona = "あなたは Agent Fleet の利用を案内する専任アシスタントです（案内役・観測役、読み取り専用）。" +
	"知識として読み込んだ利用ガイド（agent-fleet-usage.md）を根拠に、使い方・操作手順・運用上の注意を簡潔に案内してください。" +
	"利用者のワークスペースの状態を聞かれたら、推測せず list_my_sessions / get_session_status / get_session_output ツールで実際の状態を確認してから答えてください。溜まっているメモを聞かれたら list_memos で確認します。" +
	"ファイルの作成・編集やコマンド実行、メモの追加・送信は行いません。セッションへの作業依頼やメモの追加・一括送信をしたい場合は『フリート・オペレーター』アシスタントを使うよう案内してください。"

// operatorPersona drives the af_write "operator" — it observes running sessions and can
// dispatch instructions to them (send_to_session), unlike the read-only AF guide.
const operatorPersona = "あなたは Agent Fleet のフリート・オペレーター（司令塔）です。" +
	"利用者のワークスペースで走っている複数のコーディングセッションを俯瞰し、必要に応じて指示を出したり新しいセッションを起こして作業を進めます。" +
	"まず list_my_sessions / get_session_status / get_session_output で実際の状態と出力を確認し、推測で判断しないこと。" +
	"セッションに作業を依頼・修正指示する時は send_to_session で該当セッションにプロンプトを送ります。送る前に『どのセッションに何を送るか』を一言添えてから送ってください。" +
	"新しいセッションを起こす時は create_session を使います。dir は list_my_sessions の dir か list_repos の path から選び、initial_prompt に最初のタスクを渡すと起動後に自動送信されます。" +
	"あるセッションの内容を別セッションへ引き継ぐ時は、まず元セッションの get_session_output で文脈を読み、要点を要約して create_session の initial_prompt に入れて渡します（会話の丸ごと複製ではなく、必要な文脈を絞って渡すこと）。壁打ちで固まった作業を始める時も同様に create_session で起こします。" +
	"判断に専門知識が要る時は、list_assistants で相手を選び ask_assistant で他の専門アシスタント（例：整合性チェッカー）に助言を求めてから動いてください（相手は助言を返すだけで作業はしません）。" +
	"メモキュー（溜めて一括でセッションへ渡すメモ）も扱えます。list_memos で溜まっているメモを確認し、チャット中に出た TODO や後で渡したい対象は add_memo で溜め、update_memo/delete_memo で整理します。まとめて渡す時は flush_memos で選んだメモ（ids）を1メッセージに連結して対象セッションへ1回で送ります（どのセッションに何件送るかを一言添えてから）。" +
	"新規セッションの作成やメモの一括送信はリソース（メモリ・プロセス）を消費したりセッションに割り込むので、実行前に『どこで・何を』を一言添えて利用者に確認してから実行します。破壊的・不可逆な操作や曖昧・広範な依頼も同様に、実行前に必ず利用者に確認します。ファイルを直接編集はせず、セッションを通じて作業させてください。"

// integrityPersona is a domain-general consistency checker: it reads the attached
// target(s) and surfaces drift/contradictions. Works for dev, docs, and fiction alike.
const integrityPersona = "あなたは整合性チェッカーです。" +
	"添付・指定された対象（ドキュメント／コード／設定資料／原稿など）を読み、内部の食い違い・乖離・未反映・抜けを洗い出して指摘します。" +
	"典型例：ドキュメントと実装の不一致、仕様と挙動のズレ、用語・表記・命名のゆれ。物語なら設定資料との矛盾・キャラの言動ブレ・未回収の伏線。" +
	"指摘は『場所（ファイル／箇所）→ 何が食い違うか → 直す方向』の形で簡潔に列挙してください。" +
	"ファイルは編集せず指摘に徹し、断定できない点は推測と明示します。"

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
			Description: "こんにちは。Agent Fleet の使い方を案内します。操作手順や、今のワークスペースの状態（動いているセッションなど）を実際に確認しながらお答えします。",
			Builtin:     true, Agent: session.KindClaude, Persona: afAssistantPersona,
			Tools: toolsAFRead, Knowledge: []string{know},
		},
		{
			ID: "operator", Name: "フリート・オペレーター", Icon: "broadcast",
			Description: "フリートの司令塔です。走っているセッションを俯瞰し、必要ならセッションに指示を出したり新しいセッションを起こして作業を進めます（引き継ぎ・壁打ちからのタスク開始も可）。メモキューの確認・追加・一括送信もできます。専門的な判断は他のアシスタントにも相談します。実行前に内容を確認します。",
			Builtin:     true, Agent: session.KindClaude, Persona: operatorPersona,
			Tools: toolsAFWrite, Knowledge: []string{know},
		},
		{
			ID: "integrity", Name: "整合性チェッカー", Icon: "checklist",
			Description: "整合性チェッカーです。ファイルやディレクトリを渡してください。ドキュメントと実装の食い違い、用語・表記のゆれ、（物語なら）設定の矛盾や伏線の抜けを洗い出します。",
			Builtin:     true, Agent: session.KindClaude, Persona: integrityPersona, Tools: toolsNone,
		},
		{
			ID: "general", Name: "汎用アシスタント", Icon: "comment-discussion",
			Description: "汎用アシスタントです。翻訳・要約・質問への回答など、幅広くお手伝いします。何でも聞いてください。",
			Builtin:     true, Agent: session.KindClaude, Persona: chatPersona, Tools: toolsNone,
		},
		{
			ID: "translate", Name: "翻訳アシスタント", Icon: "globe",
			Description: "翻訳アシスタントです。文章を渡してください。日本語↔英語を自動判定して翻訳します（訳文だけを返します）。",
			Builtin:     true, Agent: session.KindClaude, Persona: translatePersona, Tools: toolsNone,
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

// resolveAssistant finds an assistant by id first, then by exact name — the model-facing
// ask_assistant tool (docs/19) lets an orchestrator name a specialist either way.
func resolveAssistant(idOrName string) (*assistant, error) {
	if a, err := getAssistant(idOrName); err == nil {
		return a, nil
	}
	for _, a := range listAssistants() {
		if a.Name == idOrName {
			b := a
			return &b, nil
		}
	}
	return nil, errors.New("assistant not found")
}

// --- HTTP handlers ---

func handleAssistantsList(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assistants": listAssistants()})
}

func handleAssistantGet(w http.ResponseWriter, r *http.Request) {
	a, err := getAssistant(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "アシスタントが見つかりません")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

// assistantInput is the create/update body (only user-editable fields).
type assistantInput struct {
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Description string   `json:"description"`
	Agent       string   `json:"agent"`
	Model       string   `json:"model"`
	Persona     string   `json:"persona"`
	Tools       string   `json:"tools"`
	Knowledge   []string `json:"knowledge"`
	Voice       string   `json:"voice"`
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
	a.Description = strings.TrimSpace(in.Description)
	a.Agent = in.Agent
	a.Model = strings.TrimSpace(in.Model)
	a.Persona = strings.TrimSpace(in.Persona)
	a.Tools = tools
	a.Knowledge = in.Knowledge
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
		httpx.WriteErr(w, http.StatusForbidden, "builtin", "ビルトインは編集できません")
		return
	}
	a, err := loadUserAssistant(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "アシスタントが見つかりません")
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
		httpx.WriteErr(w, http.StatusForbidden, "builtin", "ビルトインは削除できません")
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
