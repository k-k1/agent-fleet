package main

// セッション API の HTTP ハンドラ（一覧/作成/フォーク/停止/中断/アーカイブ/復元/作り直し）。
// session.go からの機械的分割（docs/log/23 P1-W4）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// managedAlive reports a managed session's liveness — the runtime-handle
// counterpart of tmuxx.HasSession（docs/log/27 P2/P3。kind ごとの実装は各縦割りパッケージ）。
func managedAlive(m session.Meta) bool {
	switch m.Kind {
	case session.KindOpencode:
		return opencode.ManagedAlive(m.Name)
	case session.KindCodex:
		return codex.ManagedAlive(m.Name)
	case session.KindCopilot:
		return copilot.ManagedAlive(m.Name)
	case session.KindCursor:
		return cursor.ManagedAlive(m.Name)
	case session.KindKiro:
		return kiro.ManagedAlive(m.Name)
	}
	return false
}

// managedBusy reports a managed session has a turn running or queued — 排他切替
// （/driver）の拒否条件（docs/log/27 §2: 切替は必ず stop→drain→resume 経由。busy の
// 間は切り替えない＝drain を「idle まで待つのはユーザー」に倒した最小形）。
func managedBusy(m session.Meta) bool {
	switch m.Kind {
	case session.KindOpencode:
		return opencode.ManagedBusy(m.Name)
	case session.KindCodex:
		return codex.ManagedBusy(m.Name)
	case session.KindCopilot:
		return copilot.ManagedBusy(m.Name)
	case session.KindCursor:
		return cursor.ManagedBusy(m.Name)
	case session.KindKiro:
		return kiro.ManagedBusy(m.Name)
	}
	return false
}

// dropManagedRuntime detaches a managed session from its runtime (stop / halt /
// archive / recreate の tmux kill-session に相当): 実行中 turn を abort し handle を
// 忘れる。会話の正本（SQLite / rollout）はそのまま残り、再開（Resume）で再接続できる。
func dropManagedRuntime(m session.Meta) {
	switch m.Kind {
	case session.KindOpencode:
		opencode.DropHandle(m.Name)
	case session.KindCodex:
		codex.DropHandle(m.Name)
	case session.KindCopilot:
		copilot.DropHandle(m.Name)
	case session.KindCursor:
		cursor.DropHandle(m.Name)
	case session.KindKiro:
		kiro.DropHandle(m.Name)
	}
}

// removeManagedLedger drops the ClientMessageID ledger on /stop（スロットの
// アイデンティティごと破棄 — halt/archive は再開があるので呼ばない）。
func removeManagedLedger(m session.Meta) {
	switch m.Kind {
	case session.KindOpencode:
		opencode.RemoveLedger(m.Name)
	case session.KindCodex:
		codex.RemoveLedger(m.Name)
	case session.KindCopilot:
		copilot.RemoveLedger(m.Name)
	case session.KindCursor:
		cursor.RemoveLedger(m.Name)
	case session.KindKiro:
		kiro.RemoveLedger(m.Name)
	}
}

// handleListSessions returns the live claude_* tmux sessions.
// We query names and each session's cwd separately rather than packing both
// into one -F line: a tab/control-char delimiter is mangled by some tmux
// builds (e.g. Debian bookworm 3.3a), so a single delimited format is fragile.
func handleListSessions(w http.ResponseWriter, r *http.Request) {
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		metas[m.Name] = m
	}

	live := tmuxx.LiveSessionNames()
	// managed セッション（docs/log/27 P2）は tmux を持たない — 生存は runtime handle が
	// 基準（daemon 死や Agent 再起動で handle が落ちれば 停止中 に見え、reconcile /
	// 再開クリックで復帰する。tui の「tmux がある＝生きている」と同型の規約）。
	for name, m := range metas {
		if m.DriverKind() == session.DriverManaged && managedAlive(m) {
			live[name] = true
		}
	}

	now := time.Now()
	ttl := session.StoppedTTL()
	sessions := []session.Session{}
	for name, m := range metas {
		if m.Archived {
			continue // hidden from the active list; restorable via the archive modal
		}
		if live[name] {
			// Running: clear any prior stopped mark so resume resets the clock.
			if m.StoppedAt != "" {
				m.StoppedAt = ""
				m = writeSessionMetaKeepingLock(m)
			}
			sessions = append(sessions, wireSession(m, true))
			continue
		}
		// Stopped (exited): stamp when first noticed, prune once older than the TTL,
		// otherwise keep it listed as resumable.
		if m.StoppedAt == "" {
			// ここが「ペインが消えているのを af が初めて見つけた」唯一の点で、
			// Workspace 停止・コンテナの SIGKILL・claude のクラッシュ・利用者の /exit を
			// まとめて拾う。モーダルを出したまま消えたなら、ここで持ち越しへ昇格させる
			// （docs/log/75 §75.6.3 の契機 2）。再開の boot フックがペイロードを消す前に
			// 通る — 停止中のセッションを一覧に出す時点で必ずこちらが先に走るため。
			promoteCarriedFor(m)
			m.StoppedAt = now.Format(time.RFC3339)
			m = writeSessionMetaKeepingLock(m)
		} else if t, e := time.Parse(time.RFC3339, m.StoppedAt); e == nil && now.Sub(t) > ttl && !m.Locked {
			// 削除ロック（docs/log/45）は自動削除にも効く — locked な行は TTL を過ぎても
			// prune せず、停止中のまま一覧に残す。
			finalizeSessionUsage(m) // 使用量台帳へ確定してから忘れる（docs/log/46 §3-b）
			status.RemoveCarried(session.UUID(m.Dir, name))
			session.RemoveMeta(name)
			gitx.MaybePruneWorktree(m.Dir) // last reference expired → clean up its worktree if clean
			continue
		}
		sessions = append(sessions, wireSession(m, false))
	}
	// Surface ORPHAN sessions: a live claude_* tmux session with no meta. These are
	// invisible to the meta-driven list above, so the auto-namer would reuse their
	// name and handleCreateSession then fails with "session already running" — a
	// confusing dead end (the session can't be seen or archived). List them so they
	// show up, count toward name uniqueness, and can be attached/archived. We can't
	// recover dir/model without a meta; kind is sniffed from the pane command.
	for name := range live {
		if _, ok := metas[name]; ok {
			continue
		}
		sessions = append(sessions, session.Session{
			Name: name, Tmux: session.TmuxName(name), Kind: tmuxx.PaneKind(name), Repo: name,
			Alive: true, Resumable: true,
		})
	}
	// Enrich rows from their working copy (worktree flag + branch-drift). One git call
	// per unique dir.
	annotateSessions(sessions, func(dir string) dirInfo {
		b, wt := gitx.GitDirInfo(dir)
		return dirInfo{branch: b, worktree: wt}
	})
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	// repoJobs は「セッションは無いが Workspace は仕事中」を CP に伝える唯一の口
	// （docs/log/78）。取り込みは分〜時間かかるのに GET のポーリングは活動と数えない規約
	// なので、これが無いと idle-stop が走行中の clone / checkout を殺す。reaper は
	// 毎スイープでこの一覧を読むので、専用のリクエストは増やさない。
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "repoJobs": repoJobsRunning()})
}

// handleSessionCatalog is the sharing inventory. Unlike the ordinary list it
// includes archived rows, but only kinds with a structured transcript.
func handleSessionCatalog(w http.ResponseWriter, r *http.Request) {
	live := tmuxx.LiveSessionNames()
	items := []session.Session{}
	for _, m := range session.ListMetas() {
		if !agentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		alive := live[m.Name]
		if m.DriverKind() == session.DriverManaged {
			alive = managedAlive(m)
		}
		items = append(items, wireSession(m, alive))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

type createReq struct {
	// Name is IGNORED: the server auto-allocates a unique slug as the session's
	// identity. Kept in the wire struct only so older clients that still send it
	// don't error. Title is the optional user-facing display name (→ claude --name).
	Name  string `json:"name"`
	Title string `json:"title"`
	Color string `json:"color"` // terminal background hue (hex); SSM host color, else empty
	Dir   string `json:"dir"`
	// Subdir starts the agent in a folder BENEATH the resolved working copy
	// (slash-relative, e.g. "console/src"). Applied last, so it composes with every
	// way Dir gets resolved — plain dir, clone-then-start, worktree-then-start (where
	// it points inside the FRESH worktree). The session's Dir keeps recording the
	// working copy itself; only the launched process starts deeper (Meta.CWD).
	Subdir string `json:"subdir"`
	// IdempotencyKey dedupes a retried/concurrent create so a client that times out
	// (but whose request the backend actually completed) can't spawn a duplicate on
	// retry. The stdio MCP create_session tool derives it deterministically from the
	// conversation id + launch args, so an LLM re-issuing the same call reproduces the
	// same key. Empty => the server falls back to a report_to-scoped intent fingerprint
	// (createIdempotencyKey); interactive Console launches send neither and aren't deduped.
	IdempotencyKey string `json:"idempotency_key"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	Mode           string `json:"mode"` // "plan" | "normal"
	// SkipPermissions は「権限確認をスキップするか」（docs/log/76）。**3 値**で、未指定(null)は
	// 「設定 > エージェントの kind 毎の既定に従う」。Console はトグルを触ったときだけ送る。
	// 未指定を false と同じに畳むと、設定でオンにしている kind まで承認ありで立ってしまう。
	SkipPermissions *bool  `json:"skip_permissions"`
	Kind            string `json:"kind"` // "claude" (default) | "opencode" | "codex" | "shell"
	// Driver selects the control route（docs/log/27 §9.2）: "" | "tui"（従来の tmux 内
	// TUI、既定）| "managed"（共有 runtime＋構造化 RPC・pane なし）。managed の起動は
	// P2（opencode serve）/ P3（codex app-server）で解禁。未対応 kind は明示拒否する。
	Driver string `json:"driver"`
	// InitialPrompt, when set, is typed into the session once its agent CLI has booted
	// and then submitted (deliverInitialPrompt) — the server-side launch-task delivery an
	// orchestrator (フリート・オペレーター / create_session MCP tool) uses to spawn a session
	// AND hand it the first task in one call. The Console delivers its own launch prompt
	// client-side (open.ts) and leaves this empty.
	InitialPrompt string `json:"initial_prompt"`
	// ReportTo (docs/log/30) links the new session to an assistant conversation and arms a
	// one-shot completion report: the first awaiting-input / abnormal-exit event after
	// launch posts a report message into that conversation. Set by the af_write MCP's
	// create_session (which knows its own conversation id via --conv); Console creates
	// leave it empty.
	ReportTo string `json:"report_to"`
	// Source attributes the initial_prompt injection's origin for the mirror badge
	// (docs/log/38): "schedule" / "schedule-manual" from the CP scheduler; anything else
	// (incl. empty — the operator MCP) records as "operator". Whitelisted server-side.
	Source string `json:"source"`
	// Origin / OriginConv record who STARTED this session (docs/log/46 §2-c, ADR 0029 §6) —
	// a different axis from Source (which attributes one injected prompt). The MCP
	// create_session sends "operator" plus its own conversation slug; the Console sends
	// nothing and defaults to "user". Schedule creates are resolved from Source, so the
	// CP scheduler needs no new wire field. Whitelisted server-side (session.ValidOrigin).
	Origin     string `json:"origin"`
	OriginConv string `json:"origin_conv"`
	// Optional clone-then-start: when remote_url is set, the repo is cloned
	// (or reused) under ~/repos and its path becomes the session CWD, ignoring dir.
	// RepoName overrides the target folder so two branches of the same repo can
	// be cloned side by side (empty => derived from remote_url, the legacy name).
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	RepoName  string `json:"repo_name"`
	// NewBranch, when set, is created off Branch (the base) right after the clone and
	// switched to, so the session starts on a fresh branch. Empty => no new branch.
	NewBranch string `json:"new_branch"`
	// Worktree switches to worktree-then-start: instead of cloning, spin a git worktree
	// off an EXISTING working copy (Dir = the parent, e.g. the main/develop 壁打ち clone)
	// at ~/repos/<repo>@<branch> and use it as CWD. Branch is the base, NewBranch (opt)
	// the fresh branch to create off it. Lets a decided task branch off into its own
	// directory + session without touching the parent. RemoteURL is ignored when set.
	Worktree bool `json:"worktree"`
	// UseExisting (worktree only) checks out an EXISTING branch (Branch) into the
	// worktree instead of creating a new one — the "work on the existing branch" answer
	// to a name collision. NewBranch is ignored; a remote-only branch is DWIM-tracked.
	UseExisting bool `json:"use_existing"`
	// Folder (worktree only) overrides the ~/repos/<repo>@<seg> folder segment so it can
	// diverge from the branch — e.g. an auto branch temp/<x> in a wip-<x> folder. Empty
	// => the folder is derived from the branch name.
	Folder string `json:"folder"`
	// SSM (kind=ssm) coordinates, resolved and forwarded by the Control Plane from a
	// host bookmark (control-plane/ssm.go). No secrets — SSO login happens in-pane.
	// SSMAlias: the host bookmark's alias (CP disambiguates it with the profile when it
	// collides with another host). Used to default a kind=ssm session's Title to
	// "{alias} @MMDD-HHMM" when the client sent no title.
	SSMAlias     string `json:"ssm_alias"`
	SSMProfile   string `json:"ssm_profile"`
	SSMTarget    string `json:"ssm_target"`
	SSMDocument  string `json:"ssm_document"`
	SSMRegion    string `json:"ssm_region"`
	SSOStartURL  string `json:"sso_start_url"`
	SSORegion    string `json:"sso_region"`
	SSOAccountID string `json:"sso_account_id"`
	SSORoleName  string `json:"sso_role_name"`
	// SSMForceLogin: run `aws sso logout` + `aws sso login` unconditionally at launch
	// (skip the cached-token short-circuit) so the user re-authenticates. One-shot.
	SSMForceLogin bool `json:"ssm_force_login"`
}

// resolveLiveModel turns a picker label or a short, unambiguous family name into
// the exact live catalog slug.  Orchestrators often receive a human request such
// as "terra" rather than the CLI spelling "gpt-5.6-terra"; passing the former to
// Codex defers a simple validation error until after the session has been created.
// An unavailable/ambiguous requested model is rejected before clone/worktree side
// effects.  If the live catalog cannot be read (offline/CLI not installed), retain
// the requested value: the normal launch path remains usable in that degraded mode.
func resolveLiveModel(requested string, choices []agents.ModelChoice) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || len(choices) == 0 {
		return requested, nil
	}
	norm := func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}
	want := norm(requested)
	// An exact id/label match wins outright, even when it also happens to be a
	// prefix of another choice (e.g. "sakana/fugu" vs "sakana/fugu-ultra"): the
	// fuzzy family-name matching below is only for when nothing matched exactly.
	for _, choice := range choices {
		if want == norm(choice.ID) || want == norm(choice.Label) {
			return choice.ID, nil
		}
	}
	var matches []string
	for _, choice := range choices {
		id, label := norm(choice.ID), norm(choice.Label)
		if strings.HasSuffix(id, "-"+want) || strings.HasSuffix(label, "-"+want) ||
			strings.HasPrefix(id, want+"-") || strings.HasPrefix(label, want+"-") {
			matches = append(matches, choice.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("モデル %q は曖昧です。利用可能なモデルから完全名を指定してください: %s",
			requested, joinModelIDs(matches, modelAmbiguousLimit))
	}
	return "", fmt.Errorf("モデル %q は利用できません。近い候補: %s。"+
		"一覧は起動ダイアログのモデル選択（アシスタントは list_models）で確認してください。",
		requested, joinModelIDs(nearestModels(want, choices), modelSuggestLimit))
}

// retiredModelError は「id は正しいが提供が終わった」場合の文言。opencode だけが持つ
// 状況で（カタログに status を持つ唯一の種）、利用者は退役を知らずに再指定を繰り返す。
func retiredModelError(requested string, choices []agents.ModelChoice) string {
	return fmt.Sprintf("モデル %q は提供終了しています（opencode.ai 側で退役）。近い候補: %s。",
		requested, joinModelIDs(nearestModels(strings.ToLower(requested), choices), modelSuggestLimit))
}

// Rejection messages are read in a Console toast and in a phone notification, so the
// id list has to fit there: the live opencode catalog alone runs to ~60 entries, and
// spelling all of them out pushed the actual reason off the top of the notification
// （実測: opencode の起動失敗通知が全文モデル名で埋まった）。So name a few ids and say
// how many were left out — the full list already has a home (起動ダイアログ / list_models).
const (
	modelSuggestLimit   = 5  // 不明なモデル: 近い候補だけ
	modelAmbiguousLimit = 10 // 曖昧: 利用者はこの中から選ぶので多めに出す
)

// joinModelIDs renders at most limit ids, appending how many were dropped.
func joinModelIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s（ほか %d 件）", strings.Join(ids[:limit], ", "), len(ids)-limit)
}

// nearestModels reorders the catalog by rough similarity to what was requested, so a
// truncated message names plausible ids rather than whichever ones sort first. The
// scoring is deliberately crude — shared dot/dash/slash tokens, then a longest-common-
// prefix tie-break — because it only has to ORDER the list; resolveLiveModel has
// already decided that nothing here matches. Returns every id (the caller truncates)
// so the "ほか N 件" count stays the size of the real catalog.
func nearestModels(want string, choices []agents.ModelChoice) []string {
	wanted := make(map[string]bool)
	for _, tok := range modelTokens(want) {
		wanted[tok] = true
	}
	type scored struct {
		id             string
		shared, prefix int
	}
	rows := make([]scored, 0, len(choices))
	for _, choice := range choices {
		row := scored{id: choice.ID, prefix: commonPrefixLen(strings.ToLower(choice.ID), want)}
		for _, tok := range modelTokens(choice.ID) {
			if wanted[tok] {
				row.shared++
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].shared != rows[j].shared {
			return rows[i].shared > rows[j].shared
		}
		return rows[i].prefix > rows[j].prefix
	})
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.id)
	}
	return out
}

// modelTokens splits an id into its comparable parts: "opencode-go/glm-5.2" =>
// opencode, go, glm, 5, 2。英数字以外はすべて区切り扱い — 種ごとに / - . _ の流儀が
// 違うので、どれを使っていても同じ粒度に割れるほうが都合がよい。
func modelTokens(id string) []string {
	return strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	})
}

// commonPrefixLen is the byte-wise tie-break for nearestModels: 同じ数のトークンを
// 共有するなら、指定された文字列に長く一致するほうが近い（＝同じ課金経路の id が先に出る）。
func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// createOrigin resolves a new session's origin (ADR 0029 §6) from the create request.
// Precedence — most specific caller wins:
//  1. an explicit origin field (the af_write MCP's create_session sends "operator" plus
//     the conversation slug it belongs to);
//  2. Source="schedule"/"schedule-manual", which the CP scheduler ALREADY sends for the
//     mirror badge (docs/log/38) — deriving from it keeps the scheduler free of a new field;
//  3. "user" — the Console launch flow, the only path a human drives, and the only one
//     that legitimately arrives unlabeled.
//
// The conversation slug is only meaningful for operator-started sessions ("which operator
// conversation made this expensive purchase"), so it is dropped otherwise.
func createOrigin(req *createReq) (origin, conv string) {
	if req.Origin != "" {
		origin = session.ValidOrigin(req.Origin)
	} else if s := injectionSource(req.Source); s == turnSourceSchedule || s == turnSourceScheduleManual {
		origin = session.OriginSchedule
	} else {
		origin = session.OriginUser
	}
	if origin == session.OriginOperator {
		conv = req.OriginConv
	}
	return origin, conv
}

// handleCreateSession launches a claude session inside a detached tmux session.
func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// Idempotency guard (session_idempotency.go): collapse a retried / concurrent create
	// onto the first one so a client that timed out mid-launch can't spawn a duplicate.
	idemKey := createIdempotencyKey(&req)
	committed := false
	if idemKey != "" {
		if prev, dup := createLedger.begin(idemKey); dup {
			if prev.state == createDone {
				// The identical create already succeeded — replay that session verbatim.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(prev.body)
			} else {
				// The first create is still launching (the timed-out client's retry, or a
				// genuine double-fire). Refuse with a distinct code the MCP tool reconciles
				// into the eventual session rather than surfacing as a retryable failure.
				httpx.WriteErr(w, http.StatusConflict, "create_in_progress",
					"同じ内容のセッションを作成中です。完了までお待ちください")
			}
			return
		}
		// We own the inflight entry: clear it on any non-committed exit so a real failure
		// doesn't wedge the key (a completed entry is kept for replay by ledger.fail).
		defer func() {
			if !committed {
				createLedger.fail(idemKey)
			}
		}()
	}
	// writeCreated finalizes a successful launch: cache the session for idempotent replay,
	// mark the key committed (so the defer above won't clear it), then write the response.
	writeCreated := func(m session.Meta) {
		body := wireSession(m, true)
		if idemKey != "" {
			if b, err := json.Marshal(body); err == nil {
				createLedger.complete(idemKey, b)
				committed = true
			}
		}
		httpx.WriteJSON(w, http.StatusCreated, body)
	}
	title, ok := cleanTitle(req.Title)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return
	}
	if req.Mode != "" && req.Mode != "normal" && req.Mode != "plan" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return
	}
	// 「権限確認を出す」は承認待ちを Console から答えられる kind でしか受けない（docs/log/76）。
	// 黙って無視すると、頼んだ側は承認ありで走っていると思い込む — 断る方が正直。
	// スキップ側（true）は従来の既定なのでどの kind でも通す。
	if req.SkipPermissions != nil && !*req.SkipPermissions && !agentOf(normalizeKind(req.Kind)).Caps().PermissionChoice {
		httpx.WriteErr(w, http.StatusBadRequest, "permission_choice_unsupported",
			"this agent kind cannot surface tool approvals to the Console; skip_permissions must stay true")
		return
	}
	// Driver validation up-front（副作用 — clone / worktree — より前に落とす）。既定の
	// tui は "" へ正規化して永続化し、既存メタとバイト同一を保つ。
	driver := strings.TrimSpace(req.Driver)
	switch driver {
	case "", session.DriverTUI:
		driver = ""
	case session.DriverManaged:
		// docs/log/27 P2/P3: driverOf に登録済みの kind（opencode / codex）だけ。
		// claude は対象外（ADR 0015）。kind はこの後 normalizeKind で claude に
		// 化け得るので、正規化後の値でなく生の req.Kind で判定してはいけない —
		// ここで normalize して以降もその値を使う。
		if _, ok := managedDrivers[normalizeKind(req.Kind)]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
				"managed ドライバはこの kind では利用できません")
			return
		}
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_driver", "unknown driver: "+req.Driver)
		return
	}
	// 使わないモデル（ui-prefs hiddenModels — model_deny.go）は kind を問わず、副作用
	// （clone / worktree）より前に断る。ここを通る経路は Console の起動導線だけでなく、
	// 定時実行（CP scheduler → この create）と MCP create_session も含む — カタログを
	// 絞るだけでは明示指定が素通りするので、このガードが本体。
	if kind := normalizeKind(req.Kind); modelHidden(kind, req.Model) {
		httpx.WriteErr(w, http.StatusBadRequest, "model_hidden", hiddenModelError(strings.TrimSpace(req.Model)))
		return
	}
	// ライブカタログの kind は候補集合からも除外しておく。resolveLiveModel は略称を
	// 一意なら完全 id へ広げるので、絞らないと "fab" のような略称が除外モデルへ
	// 解決してしまう。
	if normalizeKind(req.Kind) == session.KindCodex && strings.TrimSpace(req.Model) != "" {
		model, err := resolveLiveModel(req.Model, filterVisibleModels(session.KindCodex, codex.Models()))
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
	} else if normalizeKind(req.Kind) == session.KindCopilot && strings.TrimSpace(req.Model) != "" {
		model, err := resolveLiveModel(req.Model, filterVisibleModels(session.KindCopilot, copilot.Models()))
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
	} else if normalizeKind(req.Kind) == session.KindOpencode && strings.TrimSpace(req.Model) != "" {
		ids := visibleModelIDs(session.KindOpencode, opencode.Models())
		choices := make([]agents.ModelChoice, 0, len(ids))
		for _, id := range ids {
			choices = append(choices, agents.ModelChoice{ID: id, Label: id})
		}
		model, err := resolveLiveModel(req.Model, choices)
		if err != nil {
			// 退役したモデルは「打ち間違い」と同じ文言だと原因に辿り着けない — opencode.ai が
			// 提供をやめただけで、id も課金経路も合っているので、そう言う（models.go Retired）。
			if requested := strings.TrimSpace(req.Model); opencode.Retired(requested) {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_model", retiredModelError(requested, choices))
				return
			}
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
	}
	// Worktree-then-start: spin a git worktree off an existing working copy (req.Dir =
	// the parent) and use it as the CWD, so a decided task branches into its own dir +
	// session without touching the parent's running sessions.
	if req.Worktree {
		parent := strings.TrimSpace(req.Dir)
		if parent == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "worktree requires dir (the parent working copy)")
			return
		}
		if !filepath.IsAbs(parent) {
			parent = filepath.Join(homeDir(), parent)
		}
		// SVN has no worktree analog (docs/log/41): isolate parallel work by checking out a
		// different path into its own folder instead. Refuse a worktree launch here with a
		// clear message rather than letting ensureWorktree fail on the missing .git.
		if isSvnRepo(parent) {
			httpx.WriteErr(w, http.StatusBadRequest, "svn_no_worktree",
				"svn working copies have no worktree; check out a different path into its own folder instead")
			return
		}
		var dir string
		var err error
		folderSeg := strings.TrimSpace(req.Folder) // "" => folder derives from the branch
		if req.UseExisting {
			// "Work on the existing branch": check out req.Branch (local or DWIM-tracked
			// remote) into the worktree. Reached from the launch dialog's 既存ブランチ mode
			// and from the SCM branch actions, as well as from a name collision.
			base := strings.TrimSpace(req.Branch)
			// git holds a branch in one worktree at a time. Refuse BEFORE any directory or
			// fetch side effect, naming the copy that has it — worktree add would fail
			// halfway through otherwise, with a message that doesn't say what to do next.
			if occ := gitx.WorktreeBranches(parent)[base]; occ != "" {
				gitx.WriteBranchInUse(w, base, occ)
				return
			}
			gitx.EnsureBranchRef(parent, base) // branch pushed since our last fetch => fetch once
			dir, err = gitx.EnsureWorktree(parent, base, "", "")
			if err == nil {
				gitx.FastForwardWorktree(dir) // start at the branch's current tip, not a stale one
			}
		} else {
			// Branch naming is deferred: unless the client sends an explicit name we
			// start on a throwaway branch temp/<slug> in a wip-<slug> folder (same
			// slug). The user (or the LLM suggestion) renames the branch later — the
			// folder stays put, so the session id holds.
			nb := strings.TrimSpace(req.NewBranch)
			if nb == "" {
				slug := randSlug() // random → skip the collision check
				nb = "temp/" + slug
				folderSeg = "wip-" + slug
			} else if local, remote := gitx.BranchNameStatus(parent, nb); local {
				// A same-named local branch: -b would fail anyway, but stop with a clear
				// message rather than git's raw error, and let the user pick another name.
				httpx.WriteErr(w, http.StatusConflict, "branch_exists",
					fmt.Sprintf("branch %q already exists locally; choose another name", nb))
				return
			} else if remote {
				// A same-named PAST remote branch: -b would silently create a divergent
				// local branch that collides at push. Refuse and let the user rename or
				// work on the existing one (use_existing).
				httpx.WriteErr(w, http.StatusConflict, "branch_exists_remote",
					fmt.Sprintf("a remote branch %q already exists; rename it or work on the existing branch", nb))
				return
			}
			dir, err = gitx.EnsureWorktree(parent, req.Branch, nb, folderSeg)
			if err == nil {
				// 起点を origin の先端に合わせる（既存ブランチ経路の fastForwardWorktree と
				// 同じ狙い）。base 未指定＝親の HEAD が起点なので、その現在ブランチ名で引く。
				base := strings.TrimSpace(req.Branch)
				if base == "" {
					// gitCurrentBranch は detached を "(detached)" と答えるので、そのときは
					// 引ける相手が無い＝何もしない（ブランチ名として投げると紛らわしいログが出る）。
					if b := gitx.GitCurrentBranch(parent); b != "(detached)" {
						base = b
					}
				}
				gitx.FastForwardNewWorktreeToOrigin(dir, base)
			}
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "worktree_failed", err.Error())
			return
		}
		req.Dir = dir
	} else if strings.TrimSpace(req.RemoteURL) != "" {
		// Clone-then-start: ensure the repo exists and use it as the working dir.
		dir, err := gitx.EnsureRepo(req.RemoteURL, req.Branch, req.NewBranch, req.RepoName)
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "clone_failed", err.Error())
			return
		}
		req.Dir = dir
	}
	if req.Dir == "" {
		req.Dir = homeDir()
	} else if !filepath.IsAbs(req.Dir) {
		// A relative dir (e.g. "projects/x" from the New Session directory picker) is
		// resolved against home, mirroring the fs/tree browser's home-relative paths.
		req.Dir = filepath.Join(homeDir(), req.Dir)
	}
	if fi, err := os.Stat(req.Dir); err != nil || !fi.IsDir() {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "dir does not exist: "+req.Dir)
		return
	}
	// Subdir (optional): the CWD narrows to a folder beneath the resolved working copy.
	// Validated AFTER Dir so a worktree launch checks the path inside the fresh worktree
	// — the parent may well have a folder the new branch doesn't (or vice versa).
	subdir, subdirOK := session.CleanSubdir(req.Subdir)
	if !subdirOK {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir",
			"subdir must be a relative path inside the working copy: "+req.Subdir)
		return
	}
	if subdir != "" && !session.DirExists(filepath.Join(req.Dir, filepath.FromSlash(subdir))) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir",
			"subdir does not exist: "+filepath.Join(req.Dir, filepath.FromSlash(subdir)))
		return
	}

	// Identity is a freshly allocated random slug — NOT the client's name. It (and the
	// sid it derives) can't collide with an archived/pruned session's jsonl, so a new
	// session never accidentally --resumes a past conversation.
	name := allocSessionName(req.Dir)

	kind := normalizeKind(req.Kind)
	// An SSM session with no client title defaults to "{host alias} @MMDD-HHMM" — a
	// human-meaningful "接続先＋日時" name (vs the generic {home-basename} @… fallback).
	if kind == session.KindSSM && title == "" {
		title = ssmDefaultTitle(req.SSMAlias, req.SSMTarget, time.Now())
	}
	label := ""
	if agentOf(kind).Caps().UsesLabel {
		label = sessionLabelFor(req.Dir, title, name)
	}
	var ssm *session.SSMMeta
	if kind == session.KindSSM {
		if strings.TrimSpace(req.SSMTarget) == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_ssm", "ssm_target (instance id) is required")
			return
		}
		ssm = &session.SSMMeta{
			Profile: req.SSMProfile, Target: req.SSMTarget, Document: req.SSMDocument,
			Region: req.SSMRegion, StartURL: req.SSOStartURL, SSORegion: req.SSORegion,
			AccountID: req.SSOAccountID, RoleName: req.SSORoleName,
		}
		if ssm.Region == "" {
			ssm.Region = ssm.SSORegion
		}
	}
	origin, originConv := createOrigin(&req)
	meta := session.Meta{
		Name: name, Dir: req.Dir, Subdir: subdir, Model: req.Model, Effort: req.Effort, Mode: req.Mode, Kind: kind, Driver: driver, Title: title, Color: req.Color, Label: label,
		SkipPermissions: req.SkipPermissions,
		Repo:            filepath.Base(req.Dir), Branch: gitx.GitCurrentBranch(req.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: ssm,
		Origin: origin, OriginConv: originConv,
	}
	// docs/log/51 Phase 3 §自己申告ファストパス: 起動タスクにも「終わったら af_report を
	// 呼べ」を1行足す（report_to 付き＝報告義務のある指示のときだけ）。managed と tui の
	// 分岐より前に置いて、どちらの起動経路でも同じ1行が乗るようにする。
	if req.ReportTo != "" {
		req.InitialPrompt = withSelfReportHint(req.InitialPrompt, meta)
	}
	if meta.DriverKind() == session.DriverManaged {
		// managed（docs/log/27 P2）: tmux pane を作らず、driver が共有 runtime に thread
		// を起こす。初回プロンプトは boot 画面スクレイプ不要でそのまま Send できる
		// （§10.2-9 — ClientMessageID で冪等）。
		d, _ := driverOf(meta)
		h, err := mcpx.StartManagedSession(d, meta)
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
			return
		}
		session.WriteMeta(meta)
		if p := strings.TrimSpace(req.InitialPrompt); p != "" {
			if err := h.Send(agents.TurnInput{Prompt: p}); err != nil {
				log.Printf("managed initial prompt %s: %v", name, err)
			} else {
				markSessionWorking(name)
			}
		}
		// docs/log/51 Phase 2: 指示台帳へ1行追加する（旧 arm の1bit）。managed は
		// session-status hook を持たないが、完了は notify seam → リコンサイラで拾われる。
		if req.ReportTo != "" {
			chatx.AddInstruction(name, req.ReportTo, injectionSource(req.Source))
			recordInjection(name, req.InitialPrompt, injectionSource(req.Source)) // orchestrated start (docs/log/30 ② / docs/log/38)
		} else if s := scheduleInjectionSource(req.Source); s != "" {
			// 完了報告 OFF の定時実行が作ったセッション: 台帳は立てない（報告先が無い）が、
			// 最初のプロンプトの由来は覚える — でないと initial_prompt のターンだけ
			// バッジが落ちる（docs/log/38）。
			recordInjection(name, req.InitialPrompt, s)
		}
		writeCreated(meta)
		return
	}
	if err := startSessionTmux(meta, req.SSMForceLogin); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)

	// Optional launch task: deliver it once the CLI has booted (async — the create
	// response returns the session immediately so the caller can start polling).
	if strings.TrimSpace(req.InitialPrompt) != "" {
		go deliverInitialPrompt(name, req.InitialPrompt)
	}
	// docs/log/51 Phase 2: 起動元の会話宛に指示行を1件立てる。initial_prompt が無くても立てる
	// — オペレーターがこの後 send_to_session で手動 steer することがある。
	// The initial_prompt, when present, is an orchestrated injection (docs/log/30 ② /
	// docs/log/38) — remember it with its origin so the mirror badges its user turn.
	if req.ReportTo != "" {
		chatx.AddInstruction(name, req.ReportTo, injectionSource(req.Source))
		recordInjection(name, req.InitialPrompt, injectionSource(req.Source))
	} else if s := scheduleInjectionSource(req.Source); s != "" {
		recordInjection(name, req.InitialPrompt, s) // 報告 OFF の定時実行（managed 側と同じ）
	}

	writeCreated(meta)
}

// handleIdempotencyLookup lets a client that lost the create response (a mid-launch
// timeout) find out what became of its request without risking a duplicate: it returns
// 200 + the created session once done, 202 while still launching, or 404 if no such
// create is (or is still) on record. The stdio MCP create_session tool polls this to
// reconcile a timed-out POST instead of retrying it. GET /sessions-idempotency/{key}.
func handleIdempotencyLookup(w http.ResponseWriter, r *http.Request) {
	e, ok := createLedger.lookup(r.PathValue("key"))
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no create on record for this key")
		return
	}
	if e.state == createDone {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.body)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "creating"})
}

// handleForkSession forks a session's conversation into a NEW session of the same
// kind (POST /sessions/{name}/fork). The fork shares the source's history up to now
// but then diverges independently — each CLI's native fork copies the conversation,
// leaving the source running/intact (claude --fork-session / opencode --session
// --fork / codex fork). The per-kind source id comes from agents.Forker; the fork's
// first launch (via ForkFrom) materializes the copy, and later launches resume it.
//
// An optional body `{"at": "<anchorId>"}` narrows it to a POINT fork (docs/log/55): the new
// session gets the history up to — but not including — that turn, so the user can retake
// it. The anchor is a transcript.Turn.AnchorID the mirror handed out; the kind's
// ForkAtResolver validates it and translates it into that engine's inclusivity. No body
// (or no `at`) keeps the original whole-conversation behaviour, so old clients are
// unaffected.
func handleForkSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// The body is optional: an empty one (the pre-docs/log/55 clients, and the "fork the
	// whole thing" path) decodes to EOF, which is not an error here. Anything else that
	// fails to parse IS one — silently forking the whole conversation because a typo'd
	// body was ignored is exactly the outcome §55 refuses to produce.
	var req struct {
		At string `json:"at"`
		// include=true は「この発言と、それが得た回答まで引き継ぐ」（続きから）。
		// 既定（false）は「この発言の直前まで」＝その発言を打ち直せる形。
		Include bool `json:"include"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid fork request body")
		return
	}
	req.At = strings.TrimSpace(req.At)
	src, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	ag := agentOf(src.Kind)
	forker, canFork := ag.(agents.Forker)
	if !canFork || !ag.Caps().CanFork {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkUnsupportedKind, "this session type does not support forking")
		return
	}
	// 「そもそも地点分岐という機能があるか」は要求だけで決まるので、会話の状態を見る前に
	// 答える。ここを後ろに置くと、対応していない kind へ at を投げたとき「会話がまだ無い」
	// のような無関係な理由が返り、導線の設計ミスが状態の問題に見えてしまう。
	// 起動方式（managed か CLI か）まで見るのは kind の仕事 — 条件が kind ごとに違う
	// （opencode/codex は runtime API 必須、claude は TUI しか無い）ので、ここで一律に
	// managed を要求すると claude が永久に弾かれる。resolver が ErrForkAtRoute で答える。
	resolver, hasResolver := ag.(agents.ForkAtResolver)
	if req.At != "" && (!hasResolver || !ag.Caps().CanForkAt) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkAtUnsupported,
			"this session type cannot fork at a past message")
		return
	}
	if !session.DirExists(src.Dir) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkMissingDir, "cannot fork: the working folder does not exist")
		return
	}
	// 分岐点の解決は ForkSource より前。どの resolver も ForkSource の結果には依存せず
	// 自分で会話を引くので順番は自由で、**先に答えたほうが理由が具体的になる**: 起動方式が
	// 合っていない TUI セッションに対して「分岐できる会話がまだありません」と返しても、
	// ユーザーは会話を増やそうとするだけで永久に直らない。
	// ここで失敗したら止める — 「地点を指したのに会話まるごと分岐された」は、それらしい
	// 履歴が付いてくるぶんユーザーが気づけない壊れ方になる。
	var forkAt string
	if req.At != "" {
		at, err := resolver.ResolveForkAt(src, agents.ForkPoint{Anchor: req.At, Include: req.Include})
		if err != nil {
			code := errCodeForkBadAnchor
			if errors.Is(err, agents.ErrForkAtRoute) {
				code = errCodeForkAtUnsupported // 分岐点ではなく起動方式の問題
			}
			httpx.WriteErr(w, http.StatusBadRequest, code, err.Error())
			return
		}
		forkAt = at
	}
	forkFrom, err := forker.ForkSource(src)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "not_resumable", err.Error())
		return
	}
	forkName := allocSessionName(src.Dir)
	title, _ := cleanTitle(forkTitle(src))
	// Driver は継承する — managed セッションの分岐は managed のまま（runtime の
	// fork API で複製、docs/log/27 P2）。tui は従来の CLI fork 起動。
	meta := session.Meta{
		Name: forkName, Dir: src.Dir, Subdir: src.Subdir, Model: src.Model, Effort: src.Effort, Mode: src.Mode,
		Kind: src.Kind, Driver: src.Driver, Title: title, SkipPermissions: src.SkipPermissions,
		Repo:      filepath.Base(src.Dir),
		Branch:    gitx.GitCurrentBranch(src.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), ForkFrom: forkFrom, ForkAt: forkAt,
		// 引き継ぎで生えたセッションは出自 handoff（ADR 0029 §6）。元の出自を継ぐと
		// 「人が開いた数」に紛れ、引き継ぎで増えた消費が見えなくなる。作成元の会話は
		// 親から引き継ぐ（オペレーター発のセッションからの引き継ぎも同じ系列で追える）。
		Origin: session.OriginHandoff, OriginConv: src.OriginConv,
	}
	if ag.Caps().UsesLabel {
		meta.Label = sessionLabelFor(src.Dir, title, meta.Name)
	}
	if meta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(meta)
		if !ok {
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		if _, err := mcpx.StartManagedSession(d, meta); err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
			return
		}
		session.WriteMeta(meta)
		httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
		return
	}
	if err := startSessionTmux(meta, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)
	httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
}

// handleStopSession kills the tmux session and forgets its meta so it stops
// appearing in the list. Tolerates an already-exited session (meta only).
func handleStopSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	tn := session.TmuxName(name)
	meta, hadMeta := session.ReadMeta(name)
	live := tmuxx.HasSession(tn)
	if !live && !hadMeta {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// /stop FORGETS the meta — it is the Console's 削除. A locked session (docs/log/45)
	// refuses it; stopping without losing the row is /halt, which stays open.
	if hadMeta && meta.Locked {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLocked,
			"session is locked against deletion; unlock it first (or use /halt to stop it and keep the row)")
		return
	}
	if hadMeta {
		status.Remove(session.UUID(meta.Dir, name))
		status.RemoveExit(name)
		dropManagedRuntime(meta) // managed: 実行中 turn を abort し handle を忘れる
		removeManagedLedger(meta)
	}
	if live {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	if hadMeta {
		// fold-on-delete（docs/log/46 §3-b）: /stop は Console の「削除」で、この後 meta を
		// 忘れる＝ListMetas から消えて二度と折り込まれない（転写が残っていても対象外）。
		// 通常の折り込みは開いている末尾ターンを残すので、ここで確定させないと最後の
		// 1ターンが永久に台帳へ入らない。tmux を落とした後に呼ぶのは、終了時に書かれる
		// 最後のイベントまで転写に乗せてから読むため。
		finalizeSessionUsage(meta)
	}
	status.RemoveCarried(session.UUID(meta.Dir, name))
	session.RemoveMeta(name)
	removeTerminalHistory(name)
	// Stopping forgets the session; if it was the last one in a worktree and that
	// worktree is clean, auto-remove it so worktrees don't pile up (no-op otherwise).
	if hadMeta {
		gitx.MaybePruneWorktree(meta.Dir)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

// handleHaltSession stops a RUNNING session into the 停止中 (resumable) state: it
// kills the live tmux but KEEPS the meta visible (Archived stays false), so the row
// stays listed and the user can resume it later (claude --resume). This is the
// button counterpart of quitting in the terminal — distinct from /stop (which also
// forgets the meta = removes it from the list) and /archive (which hides it).
// An optional JSON body {"disarm_report":true} additionally cancels a pending
// one-shot operator report (docs/log/30) — sent by the MCP stop_session tool, whose stop
// means "instruction cancelled"; the Console halt sends no body and keeps the arm.
func handleHaltSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	var body struct {
		DisarmReport bool `json:"disarm_report"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional (Console sends none)
	if body.DisarmReport {
		// Disarm even when the session turns out to be already stopped below: the
		// operator's intent (cancel the instruction) does not depend on liveness.
		chatx.DisarmSessionReport(name)
	}
	if m.DriverKind() == session.DriverManaged {
		// ★持ち越しは **DropHandle より前**（docs/log/75 P5）。保留中の Interaction は
		// runtime handle の中にしか無く、handle を落とした瞬間に消える — 後で呼んでも
		// managedAlive が false になって何も取れない。
		promoteCarriedFor(m)
		// managed の halt = runtime handle を落とす（daemon は共有なので止めない）。
		// メタは残るので row は 停止中（再開可能）になる — tui の kill-session と同じ
		// 意味論。実行中 turn は DropHandle が abort する。
		dropManagedRuntime(m)
		status.Remove(session.UUID(m.Dir, name))
		m.StoppedAt = time.Now().Format(time.RFC3339)
		// Re-merge the on-disk lock: the meta snapshot above is seconds old by now and a
		// blind WriteMeta would roll back a lock the user flipped meanwhile.
		m = writeSessionMetaKeepingLock(m)
		httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
		return
	}
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		// Already stopped — nothing to do; report the current (stopped) wire.
		httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
		return
	}
	// Best-effort: disconnect any active Remote Control bridge before killing the
	// pane, so a later resume's autoconnect registers fresh under the current
	// title instead of resuming the stale one (see disconnectRemoteControl).
	disconnectRemoteControl(name, m)
	// ★持ち越しは **ペインを殺す前**（docs/log/75 P5）。claude の保留はディスク上の
	// pending-* なので後でも読めるが、kiro の承認パネルは**ペインの文字列にしか無い**
	// ので kill-session の後には何も残らない。claude 側は冪等で、ここで昇格しても
	// status.Remove（下）が持ち越しを消すことはない。
	promoteCarriedFor(m)
	// Kinds that only flush their resume state on a graceful exit (agy) get a
	// chance to quit on their own; true = the pane already ended, skip the kill.
	stopped := false
	if gs, ok := agentOf(m.Kind).(agents.GracefulStopper); ok {
		stopped = gs.GracefulStop(m)
	}
	if !stopped {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	status.Remove(session.UUID(m.Dir, name))
	// Stamp StoppedAt now so the prune TTL starts here (handleListSessions would
	// otherwise stamp it on the next poll; doing it here keeps the wire consistent).
	// GracefulStop/kill above can take seconds, so re-merge the on-disk lock instead
	// of writing back the stale snapshot (lost-update guard, same as list).
	m.StoppedAt = time.Now().Format(time.RFC3339)
	m = writeSessionMetaKeepingLock(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// handleArchiveSession hides a session from the active list but KEEPS its meta (and
// jsonl), so it can be restored later. Kills the live tmux session if any. This is
// the non-destructive counterpart to stop (which forgets the meta).
func handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// ★持ち越しは **ペインを殺す前**（docs/log/75 P5・ADR 0055 決定 12）。アーカイブも
	// halt と同じく「保留中のモーダルを抱えたまま畳む」経路であり、cursor の ACP 要求・
	// kiro の承認パネル・managed の Interaction はプロセスの中にしか無いので、
	// kill-session / DropHandle の後に呼んでも何も取れない（=質問が無言で失われる）。
	promoteCarriedFor(m)
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: pane の代わりに runtime handle を落とす
	status.Remove(session.UUID(m.Dir, name))
	status.RemoveExit(name)
	m.Archived = true
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"archived": name})
}

// handleRestoreSession brings an archived session back into the active list as a
// stopped session (the user clicks it to resume). The conversation (jsonl) is intact.
func handleRestoreSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.Archived = false
	m.StoppedAt = "" // re-stamped on next list, resetting the prune clock
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// handleListArchived returns archived sessions (for the restore modal).
func handleListArchived(w http.ResponseWriter, r *http.Request) {
	sessions := []session.Session{}
	for _, m := range session.ListMetas() {
		if m.Archived {
			sessions = append(sessions, wireSession(m, false))
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleRecreateSession starts a fresh session in the slot while PRESERVING the old
// one: the old session is archived (hidden from the active list but kept + restorable,
// its jsonl intact), NOT discarded, and a new session (fresh slug/sid, same
// title/dir/model/kind) is minted and pre-launched live. Allocating a new slug (hence
// a new sid) rather than reusing the old id lets the fresh session survive detached
// until the browser attaches (a reused id would exit first), so we pre-launch here
// like create — which lets the Console open it straight into chat. Non-destructive:
// the past conversation stays recoverable from the archive. Returns the new (alive)
// session.
func handleRecreateSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// Archive the old identity: kill its tmux, clear the live status cache, hide it from
	// the active list. Keep the meta + jsonl (and any captured resume id) so it restores.
	// ★持ち越しは **ペインを殺す前**（docs/log/75 P5・ADR 0055 決定 12）。作り直しは古い
	// セッションを畳む操作でもあり、保留中の質問 / 承認要求は kill-session /
	// DropHandle の後には残っていない — 後で呼んでも何も取れない。
	promoteCarriedFor(m)
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: pane の代わりに runtime handle を落とす
	status.Remove(session.UUID(m.Dir, m.Name))
	status.RemoveExit(m.Name)
	m.Archived = true
	session.WriteMeta(m)

	// Fresh identity, same slot. No ForkFrom — recreate means "start empty", not
	// "re-copy the fork source". Driver は引き継ぐ（managed で作った枠は managed の
	// まま作り直す — docs/log/27 P2）。
	newMeta := session.Meta{
		Name: allocSessionName(m.Dir), Dir: m.Dir, Subdir: m.Subdir, Model: m.Model, Effort: m.Effort, Mode: m.Mode,
		Kind: m.Kind, Driver: m.Driver, SkipPermissions: m.SkipPermissions,
		Title: m.Title, Color: m.Color, Repo: m.Repo, Branch: gitx.GitCurrentBranch(m.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: m.SSM,
		// recreate は「同じ枠を空で作り直す」なので出自は引き継ぐ（ADR 0029 §6）。
		Origin: session.OriginOf(m), OriginConv: m.OriginConv,
	}
	if agentOf(newMeta.Kind).Caps().UsesLabel {
		newMeta.Label = sessionLabelFor(newMeta.Dir, newMeta.Title, newMeta.Name)
	}
	if newMeta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(newMeta)
		if !ok {
			// fork と同じ扱い: driver 不在で起動を黙って飛ばすと alive=true の偽装になる。
			m.Archived = false
			session.WriteMeta(m)
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		if _, err := mcpx.StartManagedSession(d, newMeta); err != nil {
			m.Archived = false
			session.WriteMeta(m)
			httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
			return
		}
		session.WriteMeta(newMeta)
		httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
		return
	}
	if err := startSessionTmux(newMeta, false); err != nil {
		// Un-archive the old session so a launch failure doesn't silently drop it from
		// the active list.
		m.Archived = false
		session.WriteMeta(m)
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(newMeta)
	httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
}
