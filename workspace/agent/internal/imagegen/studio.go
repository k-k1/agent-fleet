package imagegen

// The image studio's contract (ADR 0100): the stored shape, the edit log and the knowledge
// document, frozen as types before the store, the pane and the MCP tools are written against
// them in parallel. The handlers in studio_http.go answer 501 until the store lands; what is
// fixed here is the vocabulary every one of those lanes speaks.
//
// The Console's copy of these types is console/src/features/imagegen/wire.ts (ImageStudio*,
// DraftLog*, Knowledge*). Keep the two in step by hand; the JSON keys are the contract.

import (
	"encoding/json"
	"errors"
)

// BindStudioSession moves a studio's `session` — the truth of the binding (decision 2) — and
// is how a session create and a recreate bind BEFORE the session is published or launched, so
// the first turn's get_image_studio already finds the studio naming it back. The studio store
// installs it; nil means this Agent has no store, and a create that asks for a studio is refused
// rather than started half-bound.
//
// The move is conditional on what the studio names now:
//   - previous == "": a create. Refused with ErrStudioNotFound when there is no such studio, and
//     with ErrStudioBound when it names another session that is still alive; a stopped or
//     deleted one is replaced, and its name comes back as `replaced`. The caller (sessionx, which
//     owns the meta lock this package cannot reach) clears that session's Meta.Studio: left in
//     place, it would resume claiming a studio that no longer names it — refused by every studio
//     tool and by generate_image, and routed by the pane to somebody else's studio.
//   - previous != "": a recreate (session is the new slot) or the rollback of a failed launch
//     (session is "" or the old slot). Applied only while the studio still names previous;
//     otherwise ErrStudioBound and nothing changes. `replaced` is always "" here.
var BindStudioSession func(studio, session, previous string) (replaced string, err error)

// The refusals BindStudioSession answers with, typed so the create can answer 404 and 409.
var (
	ErrStudioNotFound = errors.New("no such image studio")
	ErrStudioBound    = errors.New("the image studio is bound to another session")
)

// ImageStudioDraft is the studio's draft: one request as the member is composing it. The keys are
// POST /imagegen/jobs' own (jobRequest), so pressing a button turns a draft into a job without a
// translation table — including the camelCase spellings that route inherited.
//
// Every field may be empty: a draft is allowed to be unfinished (decision 3 — saving checks each
// field on its own; the request as a whole is checked when a job is made from it).
type ImageStudioDraft struct {
	// Provider is the id of the ready provider row the pane resolved; a trial never falls back
	// to another one (decision 3).
	Provider       string        `json:"provider,omitempty"`
	Model          string        `json:"model,omitempty"`
	Op             string        `json:"op,omitempty"`
	Prompt         string        `json:"prompt,omitempty"`
	NegativePrompt string        `json:"negativePrompt,omitempty"`
	Size           string        `json:"size,omitempty"`
	AspectRatio    string        `json:"aspectRatio,omitempty"`
	Count          int           `json:"count,omitempty"`
	Inputs         []string      `json:"inputs,omitempty"`
	Mask           string        `json:"mask,omitempty"`
	Loras          []loraRequest `json:"loras,omitempty"`
	Seed           *int64        `json:"seed,omitempty"`
	Strength       *float64      `json:"strength,omitempty"`
	Params         *EngineParams `json:"params,omitempty"`
	Label          string        `json:"label,omitempty"`
	OutDir         string        `json:"out_dir,omitempty"`
	Jobs           int           `json:"jobs,omitempty"`
	SeedPolicy     string        `json:"seed_policy,omitempty"`
	// FullSteps is the pane's "trial at full steps" (ADR 0081 decision 11). The member's, like
	// jobs and seed: it decides what a press costs, and moving a draft into a studio must not
	// lose it (decision 2, "nobody using ADR 0081 loses anything").
	FullSteps bool `json:"full_steps,omitempty"`
	// SuggestModel is the agent's proposal for Model, which only a person may set (decision 4);
	// the pane shows it as a card.
	SuggestModel string `json:"suggest_model,omitempty"`
}

// ImageStudioAgentFields are the draft fields the agent may write (decision 4); every other field is
// the member's alone. inputs is writable only because the gate on references exists (inputs.go).
var ImageStudioAgentFields = []string{"prompt", "negativePrompt", "params", "size", "loras", "strength", "op", "inputs", "suggest_model"}

// ImageStudio is one studio as stored at paths.ImagegenStudiosDir()/<id>.json (decision 2). Kept
// small on purpose: the edit log and the presses are the separate <id>.log.jsonl.
type ImageStudio struct {
	ID    string           `json:"id"`
	Title string           `json:"title"`
	Draft ImageStudioDraft `json:"draft"`
	// Locks are the draft fields the agent may not change (decision 4), by JSON key. A write to
	// one is dropped and the reason returned. There is no automatic lock.
	Locks []string `json:"locks,omitempty"`
	// Session is the session bound to this studio, "" for none. It is the truth of the binding;
	// the session's Meta.Studio is a copy for advertising.
	Session string `json:"session,omitempty"`
	// AgentTrial is "let the agent run a trial" (decision 3, on by default). It changes which
	// tools the session is offered.
	AgentTrial bool `json:"agent_trial"`
	// MaskStrokes are the canvas strokes of decision 11 (P1), opaque to the Agent.
	MaskStrokes json.RawMessage `json:"mask_strokes,omitempty"`
	CreatedAt   string          `json:"created_at"`
	// UpdatedAt is also the version a PUT names in If-Match, so two writers landing in the same
	// half second cannot silently overwrite each other.
	UpdatedAt string `json:"updated_at"`
}

// ImageStudioWire is GET /imagegen/studios/{id}: the stored studio plus what is derived from it.
type ImageStudioWire struct {
	ImageStudio
	// NeedsMask is op=inpaint with no mask — derived on every read, never stored (decision 4).
	NeedsMask bool `json:"needs_mask,omitempty"`
	// RecentLog is the newest edit-log entries (at most 20), newest last; the rest is paged
	// through GET …/draft-log so the studio's own answer stays small enough to poll.
	RecentLog []DraftLogEntry `json:"recent_log,omitempty"`
}

// ImageStudioSummary is one row of GET /imagegen/studios.
type ImageStudioSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Session   string `json:"session,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// ImageStudioList is GET /imagegen/studios.
type ImageStudioList struct {
	Studios []ImageStudioSummary `json:"studios"`
}

// ImageStudioCreate is POST /imagegen/studios: the pane moves its localStorage draft in (decision 2).
type ImageStudioCreate struct {
	Title string           `json:"title,omitempty"`
	Draft ImageStudioDraft `json:"draft"`
}

// ImageStudioPatch is PUT /imagegen/studios/{id}, from the pane and from set_image_draft alike. A
// merge patch over the draft: an absent key is unchanged, null clears it (decision 3). The other
// fields are the member's; set_image_draft sends only Draft.
type ImageStudioPatch struct {
	Draft map[string]json.RawMessage `json:"draft,omitempty"`
	// Author is who is writing: "human" or "agent". Locks apply to "agent" only.
	Author string `json:"author"`
	// Session is the calling session for an agent write; the store refuses it unless it is the
	// studio's bound session (the call-time check of decision 2).
	Session     string          `json:"session,omitempty"`
	Title       *string         `json:"title,omitempty"`
	Locks       *[]string       `json:"locks,omitempty"`
	AgentTrial  *bool           `json:"agent_trial,omitempty"`
	MaskStrokes json.RawMessage `json:"mask_strokes,omitempty"`
}

// ImageStudioPatchResult answers a PUT: the studio after the write, and every field that was not
// applied with its reason — kept apart so "locked" and "invalid value" read differently.
type ImageStudioPatchResult struct {
	Studio  ImageStudioWire `json:"studio"`
	Dropped []DroppedField  `json:"dropped,omitempty"`
}

// DroppedField is one field a patch did not apply.
type DroppedField struct {
	Field string `json:"field"`
	// Reason is "locked", "human_only" or "invalid".
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// ImageStudioBind is POST /imagegen/studios/{id}/bind: attach a session to the studio, or detach
// with an empty Session.
type ImageStudioBind struct {
	Session string `json:"session"`
}

// The edit-log entry kinds (decision 9).
const (
	DraftLogEdit        = "edit"
	DraftLogPress       = "press"
	DraftLogPressResult = "press_result"
	DraftLogRewind      = "rewind"
)

// DraftLogEntry is one line of <id>.log.jsonl (decision 9). An edit and a rewind carry the
// changed fields and the whole draft after them; a press carries what was known BEFORE the job
// was enqueued (the version id, the whole draft, who pressed) and a press_result what was known
// after (the jobs, or the error). Readers take the FIRST press_result per version and ignore
// the rest, and skip a line they cannot parse.
type DraftLogEntry struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`
	At   string `json:"at"`
	// Author is "agent", "human" or "rewind"; Session names the agent's session.
	Author  string            `json:"author,omitempty"`
	Session string            `json:"session,omitempty"`
	Changes []DraftChange     `json:"changes,omitempty"`
	Draft   *ImageStudioDraft `json:"draft,omitempty"`
	// RewindTo is the Seq a rewind restored.
	RewindTo int `json:"rewind_to,omitempty"`
	// Version is the press's id, reserved before the enqueue, and the key a picture's sidecar
	// and history row carry.
	Version string `json:"version,omitempty"`
	// Mode is the press's button: "trial", "enqueue" or "agent_trial".
	Mode  string   `json:"mode,omitempty"`
	Group string   `json:"group,omitempty"`
	Jobs  []string `json:"jobs,omitempty"`
	// State is a press_result's outcome: "ok", "failed", "recovered" or "lost".
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// DraftChange is one field of an edit: its JSON key and the values either side.
type DraftChange struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// DraftLogPage is GET /imagegen/studios/{id}/draft-log?before=&limit=.
type DraftLogPage struct {
	Entries []DraftLogEntry `json:"entries"`
	// Before is the Seq to ask for next, 0 when there is nothing older.
	Before int `json:"before,omitempty"`
}

// ImageStudioPress is POST /imagegen/studios/{id}/press. Session is set for an agent's trial.
type ImageStudioPress struct {
	Mode    string `json:"mode"`
	Session string `json:"session,omitempty"`
}

// ImageStudioPressResult answers a press. Recorded=false is the press_result line failing to append
// after the enqueue succeeded: the pane shows that version as "record pending".
type ImageStudioPressResult struct {
	Version  string        `json:"version"`
	Group    string        `json:"group,omitempty"`
	Jobs     []enqueuedJob `json:"jobs,omitempty"`
	Recorded bool          `json:"recorded"`
}

// ImageStudioRewind is POST /imagegen/studios/{id}/rewind: restore the draft as it was at entry Seq.
type ImageStudioRewind struct {
	To int `json:"to"`
}

// ImageStudioPersona is GET /imagegen/studios/{id}/persona: the first turn the pane sends as the new
// session's initial_prompt (decision 5), in the member's language.
type ImageStudioPersona struct {
	Prompt string `json:"prompt"`
	Lang   string `json:"lang"`
}

// HistoryItem is one picture in GET /imagegen/history?studio=&before=&limit= (decision 9).
type HistoryItem struct {
	Path      string `json:"path"`
	Studio    string `json:"studio,omitempty"`
	Version   string `json:"version,omitempty"`
	CreatedAt string `json:"created_at"`
	Trial     bool   `json:"trial,omitempty"`
}

// HistoryPage is GET /imagegen/history.
type HistoryPage struct {
	Items []HistoryItem `json:"items"`
	// Before is the cursor for the next page, "" at the end.
	Before string `json:"before,omitempty"`
}

// The knowledge document's scopes (decision 12): one file per family and one per model.
const (
	KnowledgeFamily = "family"
	KnowledgeModel  = "model"
)

// Knowledge is GET /imagegen/knowledge?scope=&key=: one document of ~/imagegen-knowledge,
// split into its four sections.
type Knowledge struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
	// Path is where the file is, for the member to open it.
	Path     string `json:"path"`
	Summary  string `json:"summary"`
	Settings string `json:"settings"`
	Prompts  string `json:"prompts"`
	Records  string `json:"records"`
	// SummaryTruncated says the summary was cut at the read limit, so the pane can say so
	// instead of hiding it.
	SummaryTruncated bool `json:"summary_truncated,omitempty"`
}

// KnowledgeAdd is POST /imagegen/knowledge — add_image_knowledge's append to the "records"
// section. Nothing else is written through this route.
type KnowledgeAdd struct {
	Scope    string `json:"scope"`
	Key      string `json:"key"`
	Note     string `json:"note"`
	Evidence string `json:"evidence,omitempty"`
	Session  string `json:"session,omitempty"`
}
