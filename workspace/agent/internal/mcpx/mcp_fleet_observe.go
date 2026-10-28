package mcpx

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// SessionOutputTailBytes caps get_session_output at the LAST N bytes of the
// flattened assistant text (/output?tail= — session_io.go): ~32KiB, a few thousand to
// ten thousand tokens. A tool result stays in the operator conversation's context, so this
// cap compounds into the price of every later turn (measured 2026-07: without a cap,
// operator conversations dragged 200-400k tokens around permanently). Changeable under
// Settings > Assistant; the effective value is SessionOutputTail().
const SessionOutputTailBytes = 32 << 10

// mcpStdioFleetObserveTools — the fleet-observation tools, part of every session's surface
// (docs/log/86 stage 1; unconditional since 2026-09-09).
//
// They are written out here rather than picked out of mcpStdioTools / mcpStdioWriteTools the
// way the Chromium tools are, because the operator's descriptions answer a different question.
// The operator is told to call get_session_status "before answer_session_question /
// respond_session_plan" — tools a session does not get — and its text is Japanese, which the
// session surface deliberately is not (a session-advertised description is a fixed cost on
// every session's first turn; measured 40% cheaper in English, b367ae51). Sharing the entries
// would either mislead the session or make the operator's text worse.
//
// The handlers are NOT duplicated: mcpStdioCall dispatches these names to the same cases the
// operator reaches, so there is one implementation and one place a fix lands.
func mcpStdioFleetObserveTools() []map[string]any {
	return []map[string]any{
		{
			"name": "get_session_status",
			"description": "Agent Fleet: report the live state of one session in this workspace - yours or another. " +
				"state is working / idle / question / plan / stopped; pendingPeerMessages counts peer messages " +
				"waiting for its user's answer. " +
				"Call it when you need to know whether a peer is still busy: after send_to_peer_session, " +
				"before deciding whether to wait on work you handed over, or when your user asks what another " +
				"session is doing. " +
				"You cannot answer another session's pending question or approve its plan - those stay with the " +
				"user and the fleet operator. If a peer is stuck on one, tell your user; do not try to unblock it.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "description": "Session name from list_peer_sessions. Omit it for your own session; the result's name is your session name"},
				},
			},
		},
		{
			"name": "get_session_usage",
			"description": "Agent Fleet: report context usage and cumulative token spend per session. " +
				"Pass name for one session, omit it for all of them. " +
				"context.pct is how full the context window is now; cumulative is what the session has spent " +
				"so far. " +
				"Call it on yourself when a task is running long, to decide whether to hand the rest over " +
				"(propose_session_handoff) instead of pushing a full context further. " +
				"A high pct is the signal to summarize and hand over while you still have room to write the " +
				"summary - not after. agy and cursor report no context, so they come back without it.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Session name (optional; omit for every session)"},
				},
			},
		},
		{
			"name": "list_memos",
			"description": "Agent Fleet: list your user's memo queue - what they call their \"memo\": the notes " +
				"they collect and later send into a session in one batch. " +
				"Each memo has id / repo / category / kind (file|text) / body / refPath. " +
				"Call it before add_memo to see whether the point is already queued, and to match the repo and " +
				"category labels already in use.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "add_memo",
			"description": "Agent Fleet: add one note to your user's memo queue. It is a message to the HUMAN, " +
				"read in the Console later - not a message to another session (that is send_to_peer_session) and " +
				"not a way to give yourself a to-do. " +
				"When your user asks you to note something down - \"add this to my memo\", \"memo it\" - THIS " +
				"queue is what they mean, not your own agent memory or notes file: write there only when they " +
				"say memory, or name the file. " +
				"Use it for something worth acting on that is outside what you were asked to do: a bug you had to " +
				"step around, a stale document you noticed, follow-up work your change implies. " +
				"Say what you found and where (repo, file:line); the queue is read away from this conversation, " +
				"so nothing here is context the reader already has. " +
				"Do not log progress, completion or thanks - your report and the Console already carry those. " +
				"kind=text needs body; kind=file needs refPath (a ~/repos/... path) and takes body as a comment.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"kind":     map[string]any{"type": "string", "enum": []string{"text", "file"}, "description": "text | file"},
					"body":     map[string]any{"type": "string", "description": "The note (kind=text), or a comment on the file (kind=file)"},
					"refPath":  map[string]any{"type": "string", "description": "A ~/repos/... path (kind=file)"},
					"repo":     map[string]any{"type": "string", "description": "Which repo bucket it belongs to (optional; '' = unfiled)"},
					"category": map[string]any{"type": "string", "description": "Free-form sub-project label (optional; reuse one from list_memos)"},
				},
				"required": []string{"kind"},
			},
		},
		{
			"name": "update_memo",
			"description": "Agent Fleet: edit one memo already in your user's queue, by id from list_memos. " +
				"Only the fields you pass change. " +
				"Call it when what you queued turns out to be wrong or incomplete - the cause was something " +
				"else, the file moved, the follow-up you noted is already done - rather than adding a second " +
				"memo that contradicts the first. " +
				"The queue is your user's, and it holds notes from them and from other sessions too: correct " +
				"your own, and leave someone else's alone unless you are fixing the thing it is about. " +
				"You cannot delete a memo or send the queue; that stays with the user in the Console.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"id":       map[string]any{"type": "string", "minLength": 1, "description": "Memo id from list_memos"},
					"body":     map[string]any{"type": "string", "description": "Replacement note text (optional)"},
					"refPath":  map[string]any{"type": "string", "description": "Replacement ~/repos/... path (optional)"},
					"repo":     map[string]any{"type": "string", "description": "Move it to another repo bucket (optional; '' = unfiled)"},
					"category": map[string]any{"type": "string", "description": "Change the sub-project label (optional)"},
				},
				"required": []string{"id"},
			},
		},
	}
}

// memoWriteAllowed authorizes the memo writers a session may reach: add_memo and update_memo.
// Both surfaces reach them — the operator under --write, and any session (observation is part
// of the session surface).
//
// delete_memo and flush_memos keep the bare writeEnabled() check. The line is not "read vs
// write" but what a mistake costs the user: adding a note and correcting one are recoverable
// from the queue itself, while emptying it destroys what the user had not read yet and flushing
// it sends work into a session on their behalf.
func memoWriteAllowed() bool {
	return writeEnabled() || selfReportOnly()
}

// outputCursorScope is what get_session_output's "continue from where I left off" is remembered
// under. The operator has a conversation; a session does not, and --conv is not passed to the
// session-side server — so keyed on convID() alone the session surface silently had NO cursor
// and re-read the whole tail on every poll, which is the one surface whose description promises
// otherwise and the one with no reply budget to absorb it (docs/log/86 §86.2).
//
// The session's own name is the right key for the same reason the conversation is the operator's:
// it is the thing doing the reading. An unresolvable name yields "", i.e. no cursor memory,
// which is the pre-existing behaviour rather than a wrong one.
func outputCursorScope() string {
	if !selfReportOnly() {
		return convID()
	}
	self, err := mcpOwningSession()
	if err != nil {
		return ""
	}
	return self
}

// OutputCursors remembers, per conversation, the last /output cursor returned for
// each session (file name = conversation id, contents = session name -> cursor). It is the
// default when since is omitted: an operator re-reads the same session on every report, and
// re-fetching the trailing 32KiB each time piles all of it back into the context, whereas
// returning only the delta keeps later turns cheap. mcp-stdio is a short-lived per-turn
// process, so this is a file rather than memory (chat.go deletes it with the conversation).
var OutputCursors = fstore.JSON[map[string]int64](paths.AgentStateDir, "mcp-output-cursor", ".json")

// SessionOutputTail is the effective get_session_output tail cap: the session-output limit
// under Settings > Assistant (ui-prefs assistantOutputTailKiB), defaulting to
// SessionOutputTailBytes.
func SessionOutputTail() int {
	if v, ok := readUIPrefs()["assistantOutputTailKiB"].(float64); ok && v > 0 {
		n := int(v) << 10
		if n < 4<<10 {
			n = 4 << 10
		}
		if n > 1<<20 {
			n = 1 << 20
		}
		return n
	}
	return SessionOutputTailBytes
}

// mcpListChildSessions answers list_child_sessions: the caller's own children, the state each
// one is in, and how much of the budget is left (docs/log/89, ADR 0073 decision 6).
//
// It reads GET /sessions rather than the metas, for three reasons that all come from the
// listing already doing the work. The live state (working / idle / stopped) is computed there
// per kind and cannot be derived from a meta at all. The archived rows and the stopped children
// whose StoppedTTL has expired are filtered — and the expiry is not merely filtered: that
// handler archives them (ADR 0097), which is what releases the slot, so the count this returns
// matches what the next create_session will reserve against instead of being one poll behind it.
// And `origin` / `originSession` are on the wire for exactly this question.
//
// The predicate is countChildren's and sessionDriveAllowed's: origin=session AND the lineage is
// the caller. Deliberately not the wider one the recursion limit uses — a fork of a child keeps
// the lineage but a person made it in the Console, it is not this session's to steer, and it
// does not spend this session's budget.
func mcpListChildSessions(id json.RawMessage, self string) []byte {
	body, err := agentGET("/sessions")
	if err != nil {
		return mcpToolErr(id, "子セッション一覧の取得に失敗しました: "+err.Error())
	}
	var wire struct {
		Sessions []session.Session `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return mcpToolErr(id, "子セッション一覧を読めませんでした: "+err.Error())
	}
	limit := session.SpawnChildLimit()
	rows := make([]any, 0, limit)
	for _, s := range wire.Sessions {
		if s.Origin != session.OriginSession || s.OriginSession != self {
			continue
		}
		// Only what a parent decides on. Every key here is re-read on every poll for the rest
		// of the conversation, so the rest of the DTO (context fill, colours, branch drift,
		// exit codes) stays off: get_session_status and get_session_usage answer those when
		// they are actually asked.
		row := map[string]any{"name": s.Name, "kind": s.Kind, "dir": s.Dir, "createdAt": s.CreatedAt}
		// The listing leaves State empty for a session that is not alive — "stopped" is
		// carried by the Alive flag, and the drive endpoints spell it out (DriveState's first
		// branch). Spell it out here too: a blank state next to a name reads as "unknown",
		// which is the one thing this row must not say when it does know.
		switch {
		case !s.Alive:
			row["state"] = "stopped"
		case s.State != "":
			row["state"] = s.State
		}
		if s.Title != "" {
			row["title"] = s.Title
		}
		if s.LastTurnEndAt != "" {
			row["lastTurnEndAt"] = s.LastTurnEndAt
		}
		rows = append(rows, row)
	}
	left := limit - len(rows)
	if left < 0 {
		left = 0
	}
	return mcpStructuredResult(id, map[string]any{
		"sessions": rows, "slotsLeft": left, "slotLimit": limit,
	})
}

// mcpSessionOutput handles get_session_output: it always passes the tail cap, and when
// since is omitted it continues from the cursor remembered for this conversation. An
// explicit since — including 0 — always wins.
func mcpSessionOutput(id json.RawMessage, name string, since *int64) []byte {
	eff := int64(-1)
	fromStore := false
	scope := outputCursorScope()
	if since != nil {
		eff = *since
	} else if scope != "" {
		if cur, ok := OutputCursors.Read(scope); ok {
			if v, ok2 := cur[name]; ok2 {
				eff, fromStore = v, true
			}
		}
	}
	path := "/sessions/" + url.PathEscape(name) + "/output?tail=" + strconv.Itoa(SessionOutputTail())
	if eff >= 0 {
		path += fmt.Sprintf("&since=%d", eff)
	}
	body, err := agentGET(path)
	if err != nil {
		return mcpToolErr(id, "Agent への問い合わせに失敗しました: "+err.Error())
	}
	var resp map[string]any
	if json.Unmarshal([]byte(body), &resp) == nil {
		// Remember the returned cursor per SCOPE (conversation, or the reading session) as the
		// next default since. Only the conversation side is ever cleaned up; a session's entry
		// outlives it, holding one integer offset per session it read.
		if cursor, ok := resp["cursor"].(float64); ok && scope != "" {
			cur, _ := OutputCursors.Read(scope)
			if cur == nil {
				cur = map[string]int64{}
			}
			if cur[name] != int64(cursor) {
				cur[name] = int64(cursor)
				_ = OutputCursors.Write(scope, cur)
			}
		}
		// A default continue-read with no new output: returning an empty string reads as
		// "this session produced nothing", so say what it means in the body.
		if s, _ := resp["output"].(string); fromStore && strings.TrimSpace(s) == "" {
			resp["output"] = fmt.Sprintf(
				"（前回取得（since=%d）以降の新しい出力はありません。過去の出力を読み直す場合は since を明示してください（例: since=0）。）", eff)
			if b, err := json.Marshal(resp); err == nil {
				body = string(b)
			}
		}
	}
	return mcpTextResult(id, body)
}
