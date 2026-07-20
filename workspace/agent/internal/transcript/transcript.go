// Package transcript は claude/codex/opencode の3パーサが共有する出力語彙
// (docs/23 P1-W5)。JSONタグは Console の型と対 — 変更禁止。
package transcript

// Part is one ordered piece of a turn: rendered text, a faint tool trace, a
// subagent delegation, an AskUserQuestion the user can answer inline, or an
// ExitPlanMode plan.
type Part struct {
	Kind      string     `json:"kind"`                // "text" | "thinking" | "tool" | "delegation" | "question" | "plan" | "userfile"
	Text      string     `json:"text,omitempty"`      // kind=text/thinking: Markdown
	Tool      string     `json:"tool,omitempty"`      // kind=tool/delegation/question/plan/userfile: tool name
	Info      string     `json:"info,omitempty"`      // kind=tool/delegation: short human-facing label
	Output    string     `json:"output,omitempty"`    // kind=tool/delegation: output/final result, truncated
	Prompt    string     `json:"prompt,omitempty"`    // kind=delegation: full instruction sent to the child
	AgentType string     `json:"agentType,omitempty"` // kind=delegation: Explore/general-purpose/task name
	Status    string     `json:"status,omitempty"`    // kind=delegation: requested/running/completed/failed
	Model     string     `json:"model,omitempty"`     // kind=delegation: explicitly selected child model
	Questions []Question `json:"questions,omitempty"` // kind=question: AskUserQuestion
	Answer    string     `json:"answer,omitempty"`    // kind=question: the chosen answer text
	Plan      string     `json:"plan,omitempty"`      // kind=plan: ExitPlanMode plan Markdown
	File      string     `json:"file,omitempty"`      // kind=tool: edit/write target (openable as a diff)
	Edits     []Edit     `json:"edits,omitempty"`     // kind=tool: before/after per edit (Edit/Write/MultiEdit)
	Files     []string   `json:"files,omitempty"`     // kind=userfile: SendUserFile paths, browse-root-relative (openable in a pane)
	Caption   string     `json:"caption,omitempty"`   // kind=userfile: optional caption the agent attached
	QID       string     `json:"-"`                   // kind=question: tool_use id, to resolve the answer (never serialized)
}

// Edit is one before/after pair for an edit-family tool, so the Console can render
// a diff. Write is a single all-added entry (Old=""); MultiEdit is one entry per edit.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Question mirrors one AskUserQuestion entry (header + prompt + options).
type Question struct {
	// ID identifies the runtime-side Interaction this pending question belongs to
	// (docs/27 §5) — managed driver のセッションだけが埋め、Console は id 付きの質問を
	// POST /respond の構造化回答で返す。TUI 由来（hooks/probe/rollout）の質問は空の
	// まま＝従来どおり keys/seq で TUI モーダルを駆動する。省略可の追加フィールド
	// なので既存ワイヤと互換（タグ変更ではない）。
	ID          string   `json:"id,omitempty"`
	Header      string   `json:"header,omitempty"`
	Question    string   `json:"question"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Turn is one displayable conversation turn.
type Turn struct {
	Role  string `json:"role"`  // "user" | "assistant"
	Parts []Part `json:"parts"` // ordered text/tool pieces
	Text  string `json:"text"`  // concatenated text only (for copy / fallback)
	// Source attributes a user turn's origin: "operator" = injected by the fleet operator
	// (an af_write assistant's create_session / send_to_session), "" = the user's own input
	// (composer or raw terminal). Set server-side by matching the operator-injection store
	// (docs/30 ②), so the mirror can badge operator-driven prompts distinctly.
	Source    string `json:"source,omitempty"`
	Model     string `json:"model,omitempty"`     // assistant only: the model that answered
	Effort    string `json:"effort,omitempty"`    // assistant only: reasoning effort/variant (codex reasoning_effort, opencode variant); "" when the agent records none (claude)
	CtxWindow int    `json:"ctxWindow,omitempty"` // assistant only: the model's real context-window size when the agent records it (codex model_context_window); 0 = let the Console guess from the model name
	Sidechain bool   `json:"sidechain,omitempty"` // true = a subagent (Task) sidechain turn
	Branch    string `json:"branch,omitempty"`    // git branch at the time of the turn
	Cwd       string `json:"cwd,omitempty"`       // working dir at the time of the turn
	// Token usage (assistant only), per event; the Console sums output across a turn's
	// events and takes the last event's input/cache as the context size.
	InTok       int    `json:"inTok,omitempty"`
	OutTok      int    `json:"outTok,omitempty"`
	CacheRead   int    `json:"cacheRead,omitempty"`
	CacheCreate int    `json:"cacheCreate,omitempty"`
	TS          string `json:"ts"`  // RFC3339 from the transcript line, "" if absent
	Idx         int    `json:"idx"` // transcript line index — a stable render key
	// Compact: this "turn" is claude's auto-compaction summary (jsonl isCompactSummary),
	// written as a user message. The Console shows it as a collapsible "圧縮されました"
	// block instead of a normal user prompt.
	Compact bool `json:"compact,omitempty"`
}

// Context is a session-level context-fill reading reported by the agent itself
// (agy の /context スクレイプ)。ターン毎の usage（InTok/CacheRead/CacheCreate）が
// 取れる agent ではそちらが正で、これは転写に token 情報を一切持たない agent の
// 縮退ソース — Console の ContextBar は内訳なしの合計/window だけを描く。
type Context struct {
	Tokens int    `json:"tokens"`       // current context fill (agent-reported estimate)
	Window int    `json:"window"`       // context-window size the fill is against
	At     string `json:"at,omitempty"` // RFC3339 scrape time (staleness hint)
}

// Task is a ToDo task reconstructed from the transcript's Task tool calls. Unlike
// TodoWrite (which resends the whole list each time), TaskCreate/TaskUpdate are
// incremental.
type Task struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Active  string `json:"activeForm,omitempty"`
	Status  string `json:"status"` // pending | in_progress | completed
}
