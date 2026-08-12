// Package transcript は claude/codex/opencode の3パーサが共有する出力語彙
// (docs/23 P1-W5)。JSONタグは Console の型と対 — 変更禁止。
package transcript

// Part is one ordered piece of a turn: rendered text, a faint tool trace, a
// subagent delegation, an AskUserQuestion the user can answer inline, an
// ExitPlanMode plan, or a turn-level failure the agent recorded instead of an answer.
type Part struct {
	Kind string `json:"kind"`           // "text" | "thinking" | "tool" | "delegation" | "question" | "plan" | "userfile" | "error"
	Text string `json:"text,omitempty"` // kind=text/thinking: Markdown / kind=error: the provider's message
	Tool string `json:"tool,omitempty"` // kind=tool/delegation/question/plan/userfile: tool name
	Info string `json:"info,omitempty"` // kind=tool/delegation: short human-facing label / kind=error: error name + HTTP status
	// Cause is kind=error only: the machine-readable reason the Console keys its
	// recovery guidance off ("auth" = signing in again clears it; "" = no guidance).
	// The label above is the provider's own name and changes between versions, so the
	// Console must not pattern-match it; this field is the stable axis. Optional —
	// an agent that cannot classify its failures simply omits it.
	Cause     string     `json:"cause,omitempty"`
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
	QID       string     `json:"qid,omitempty"`       // kind=question/plan/delegation: tool_use id, so the Console can patch a late-arriving answer (see CollectInteractionAnswers) onto an already-delivered turn

	// ViewImageCallID/ViewImageData carry a codex view_image tool_result's inline
	// "data:image/...;base64,..." payload(s) from the pure rollout parser up to
	// readTranscript, which persists them to a servable file and appends a sibling
	// kind=userfile Part with the result — never serialized to the Console (json:"-"),
	// and cleared once persisted. kind=tool (Tool="view_image") only.
	ViewImageCallID string   `json:"-"`
	ViewImageData   []string `json:"-"`
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
	// Preview is the mockup / code snippet the agent attached to this option (claude's
	// AskUserQuestion `preview`) so the choices can be COMPARED before one is picked —
	// the material the question is about, not decoration. Dropping it here left the
	// mirror showing two labels whose difference was only visible in the CLI. Whitespace
	// is load-bearing (ASCII box drawings), so it travels verbatim. Optional: the other
	// agents' question tools have no equivalent and simply omit it.
	Preview string `json:"preview,omitempty"`
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
	// AnchorID is the AGENT's own stable identifier for this turn, opaque to the Console:
	// claude = message uuid, codex = turn id, opencode = message id ("msg_…"). It is the
	// handle "branch from this message" (docs/55) passes back to POST /fork {"at": …}.
	// Idx cannot serve: it is a line/message ordinal that moves under compaction, and a
	// branch taken from a silently shifted point looks entirely plausible to the user.
	// Empty = this kind has no such id (or the row predates it) — the Console then hides
	// the affordance for that turn instead of guessing.
	AnchorID string `json:"anchorId,omitempty"`
	// EndTS is when an assistant turn FINISHED (RFC3339). The Console shows a block's
	// end time in its footer, not its start: a turn that runs tools for minutes would
	// otherwise be stamped with the moment the model began (its first thinking/tool
	// event), which reads as the wrong date entirely on a long turn.
	// Agents that write one turn as MANY rows (claude, codex) leave this empty — the
	// Console folds those rows into one block and the last row's TS already is the end.
	// It is for agents that record a whole turn as a SINGLE row (opencode's message,
	// copilot's turn_start…turn_end span), where TS alone can only be the start.
	// "" = unknown; the Console falls back to TS.
	EndTS string `json:"endTs,omitempty"`
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
