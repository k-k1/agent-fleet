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
// Each has a session schema of its own rather than reusing the operator's the way the Chromium
// tools do, because the operator's descriptions answer a different question.
// The operator is told to call get_session_status "before answer_session_question /
// respond_session_plan" — tools a session does not get — and its text is Japanese, which the
// session surface deliberately is not (a session-advertised description is a fixed cost on
// every session's first turn; measured 40% cheaper in English, b367ae51). Sharing the entries
// would either mislead the session or make the operator's text worse.
//
// The handlers are NOT duplicated: each tool is one toolSpec carrying both texts, so there is
// one implementation and one place a fix lands.
func mcpStdioFleetObserveTools() []map[string]any {
	return sessionToolsNamed(fleetObserveSessionTools...)
}

// fleetObserveSessionTools names the tools mcpStdioFleetObserveTools offers, in order.
var fleetObserveSessionTools = []string{"get_session_status", "get_session_usage", "list_memos", "add_memo", "update_memo"}

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

// fleetObserveTools is the operator's read-only view of the sessions, the repositories and
// their cleanup. A session is offered some of the same tools in its own words
// (mcpStdioFleetObserveTools, mcpStdioFleetSpawnTools) and reaches the same handlers.
func fleetObserveTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_my_sessions",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_my_sessions",
					"description": "利用者自身のワークスペースで稼働中のセッション一覧（名前・種別・状態・作業ディレクトリ）を返す。「今どのセッションが動いている?」等に答える時に呼ぶ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				path = "/sessions"
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "get_session_status",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_session_status",
					"description": "指定セッションのライブ状態（working/idle/question/plan 等）を返す。保留中の質問と選択肢は questions、承認待ちのプラン本文は plan として付く（claude）。特定セッションが動作中か聞かれた時や、質問への回答（answer_session_question）・プランへの応答（respond_session_plan）の前に呼ぶ。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "セッション名（例: s7）"},
						},
						"required": []string{"name"},
					},
				}
			},
			session: func() map[string]any {
				return map[string]any{
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
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				name := a.Name
				if name == "" && selfReportOnly() {
					// A session asking about itself: this is also how a Managed model, which has no
					// $AF_SESSION_NAME in its shell, learns its own name.
					resolved, err := mcpOwningSession()
					if err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
					name = resolved
				}
				if name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				path = "/sessions/" + url.PathEscape(name) + "/status"
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "get_session_output",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_session_output",
					"description": "指定セッションの端末出力を返す。あるセッションの最近の出力/結果を要約・確認する時に呼ぶ。長い出力は末尾のみ返す（clipped=true・先頭は省略される — 直近の結果を読むにはそれで足りる）。since を省略すると、この会話で前回取得した続き（差分）から返す（同じ内容を二度読まない）。過去の出力を読み直したい時だけ since を明示する（例: since=0 で先頭から）。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":  map[string]any{"type": "string", "description": "セッション名"},
							"since": map[string]any{"type": "integer", "description": "この出力オフセット以降のみ取得（任意）"},
						},
						"required": []string{"name"},
					},
				}
			},
			session: func() map[string]any {
				return map[string]any{
					"name": "get_session_output",
					"description": "Agent Fleet: read the recent terminal output of a session YOU started. " +
						"Only your own children; for another session use peek_session_output where it is offered. " +
						"Call it when get_session_status says a child is idle or stopped and you need to know what came " +
						"of the task. Long output is clipped to the tail; omit since to continue from where you last read.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name":  map[string]any{"type": "string", "minLength": 1, "description": "Child session name (from create_session's result)"},
							"since": map[string]any{"type": "integer", "description": "Read from this output offset instead of continuing (optional; 0 = from the start)"},
						},
						"required": []string{"name"},
					},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				// Unlike the other reads a session is given, this one names a target and returns
				// another session's raw output — so it is children only (ADR 0073 decision 4).
				if err := sessionDriveAllowed(a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpSessionOutput(req.ID, a.Name, a.Since)
			},
		},
		{
			name: "list_cleanup_candidates",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_cleanup_candidates",
					"description": "溜まったセッション・worktree の掃除候補を点検して返す。各候補は type（session|worktree）・action（archive_session|delete_worktree|空=手動のみ）・safety（safe=マージ済みクリーン等で安全／review=停止中セッションや未マージ worktree で要確認／keep=稼働中や未コミット・未pushで触らない）・reason を持つ。『リポジトリが散らかってきた・不要なものを片付けたい』時にまず呼び、safe/review の候補を利用者に提示してから archive_session / delete_worktree で実行する。keep は掃除せず、必要なら利用者が Console で対応する。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				path = "/sessions/cleanup"
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "list_cleanup_archives",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_cleanup_archives",
					"description": "掃除で退避したアーカイブ（削除したセッションやブランチの gz 安全網）の一覧を返す。delete_session / delete_branch は消す前に必ずここへ退避するので、消しすぎた時は restore_cleanup_archive で復元、不要になったら purge_cleanup_archive で完全削除して容量を回収できる。各アーカイブは id・日時・理由・含まれるセッション/ブランチを持つ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				path = "/cleanup/archives"
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "get_session_usage",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_session_usage",
					"description": "各セッションのコンテキスト使用量と累積消費トークンを返す。name 指定で1セッション、省略で transcript を持つ全セッション（shell / SSM は対象外）。context は現在のコンテキスト量（tokens と read/create/fresh の内訳、window に対する pct%。最初の応答が返るまでは無く、自動圧縮後は圧縮後の値）。cumulative は累積消費（論理ターン数 turns、inTok/outTok/cacheRead/cacheCreate、spend=inTok+cacheCreate+outTok の合計）。注意: agy / cursor は一覧に含まれるが transcript にトークン情報が無いため context は空・cumulative は全て 0 になる（消費ゼロの意味ではない。agy の残枠は get_agent_usage を見る）。kiro は転写にトークンが無いが、managed（ACP）セッションが稼働中は _kiro.dev/metadata のライブ値から context（pct＋実 window に対する概算 tokens）と cumulative.credits（消費クレジット）を返す（停止中や TUI 実行は context 空）。copilot は outTok のみ記録され inTok/cache は 0・context は無い。muse は cumulative を持つが subagent / observer の呼び出しが上流のワイヤに載らないため過少（measured=partial）で、context は無い。『どのセッションがコンテキスト逼迫か』『どれだけ消費したか』を聞かれた時や、引き継ぎ・圧縮・新セッション分割の判断材料に呼ぶ。サブスクリプション枠の残量は get_agent_usage（別ツール）。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "セッション名（任意。省略で全セッション）"},
						},
					},
				}
			},
			session: func() map[string]any {
				return map[string]any{
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
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				path = "/sessions/usage"
				if a.Name != "" {
					path += "?name=" + url.QueryEscape(a.Name)
				}
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "get_agent_usage",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_agent_usage",
					"description": "各エージェント CLI のサブスクリプション使用量とレート制限を返す（claude / codex / agy / muse。opencode / copilot / cursor / kiro は使用量ソースが無いため含まれない）。claude / codex は fiveHour（5時間利用枠）と sevenDay（週間利用枠）の pct が使用率（0–100）、resetsAt が解除日時（ISO 8601）で、codex は planType や resetCredits も付く。agy は形が異なり、account / plan と groups（クォータ枠ごとに label・remainingPct・resetsAt。実験枠 Starter 等）を返す。muse は claude / codex と同じ形だが、値はホストが最後に観測したもので、稼働中の muse セッションがターンを1つも終えていない間は ok=false になる（使用量ゼロではなく観測が無いという意味）。windowMins は現在の利用枠の長さ（分）。authed=false はその CLI に未ログイン、ageSec は計測の古さ（秒）。『あとどれくらい使える?』『制限はいつ解除?』と聞かれた時や、大きなタスクをセッションに振る前の判断材料に呼ぶ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			session: func() map[string]any {
				return map[string]any{
					"name": "get_agent_usage",
					"description": "Agent Fleet: report each agent CLI's subscription usage and rate limits (claude / codex / agy / muse). " +
						"pct is how much of a window is used, resetsAt when it lifts, authed=false means that CLI is not " +
						"signed in. " +
						"Call it before handing a long task to a kind you do not normally use - starting a session on a " +
						"CLI that is out of quota or signed out wastes the launch. Not the same as get_session_usage, " +
						"which is about context, not the plan.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				// Read-only merge of the two WsBar usage endpoints (5h/weekly windows captured
				// locally from statusline / rollout — no network call). opencode has no usage
				// source, so it is intentionally absent from the result (said in the tool desc).
				cl, err := agentGET("/claude/usage")
				if err != nil {
					return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
				}
				cx, err := agentGET("/codex/usage")
				if err != nil {
					return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
				}
				// agy usage lives under the connections path and has a different shape
				// ({account, plan, groups}); it self-reports authed=false when signed out
				// and never 500s, so a merge is safe. Absent when unsupported on this host.
				ag, err := agentGET("/connections/agy/usage")
				if err != nil {
					return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
				}
				// muse's reading is the host's own last observation off the wire (ADR 0095 decision
				// 10), so unlike the three above it can be authed with nothing to report — a
				// workspace whose muse sessions have all just started has seen no completion yet.
				// It answers {ok:false, authed:...} in that case and never 500s, so the merge is safe.
				ms, err := agentGET("/muse/usage")
				if err != nil {
					return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
				}
				b, _ := json.Marshal(map[string]any{
					"claude": json.RawMessage(cl),
					"codex":  json.RawMessage(cx),
					"agy":    json.RawMessage(ag),
					"muse":   json.RawMessage(ms),
				})
				return mcpTextResult(req.ID, string(b))
			},
		},
		{
			name: "list_repos",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_repos",
					"description": "利用者のワークスペースにある git 作業コピー（~/repos 配下）の一覧を返す。新規セッションをどのディレクトリ（リポジトリ）で起こすか決める時に、まだセッションが動いていないリポジトリも含めて選ぶために呼ぶ。返る各リポジトリの path を create_session の dir に渡す。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			session: func() map[string]any {
				return map[string]any{
					"name": "list_repos",
					"description": "Agent Fleet: list the working copies in this workspace (path, repo, branch). " +
						"Call it before create_session to pick dir - a path you remember may not exist here.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				path = "/repos"
				return mcpAgentGETRelay(req, p, path)
			},
		},
	}
}

// mcpAgentGETRelay answers a read tool with the Agent's GET of path, as is.
func mcpAgentGETRelay(req mcpReq, p toolCallParams, path string) []byte {
	body, err := agentGET(path)
	if err != nil {
		return mcpToolErr(req.ID, "Agent への問い合わせに失敗しました: "+err.Error())
	}
	if p.Name == "get_session_status" && selfReportOnly() {
		body = withoutPendingInteraction(body)
	}
	return mcpTextResult(req.ID, body)
}
