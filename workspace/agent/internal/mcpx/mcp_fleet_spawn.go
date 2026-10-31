package mcpx

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stoppedChildExpiryClause is the part of create_session's limits sentence that promises a
// stopped child frees its slot by itself. Only true while auto-archive is on: with the user's
// setting at "off" the promise would have a caller wait for a slot that never comes back.
func stoppedChildExpiryClause() string {
	if _, ok := session.StoppedTTL(); ok {
		return ", or when one you left stopped expires"
	}
	return ""
}

// mcpStdioFleetSpawnTools — the ten session-steering tools, advertised only under
// `--self-report --fleet-spawn` (ADR 0073). Each carries a session text of its own rather than
// the operator's, for the reasons in mcpStdioFleetObserveTools: the operator's text is Japanese
// and points at tools a session does not get. The handlers are shared.
//
// The descriptions carry the refusals (children only, three at a time, no grandchildren, no
// shells) because a limit a model learns by hitting it costs a whole turn, and because these
// are the sentences that make the difference between "start a session for every thought" and
// "start one when the work genuinely splits".
func mcpStdioFleetSpawnTools() []map[string]any {
	return sessionToolsNamed(fleetSpawnSessionTools...)
}

// fleetSpawnSessionTools names the tools mcpStdioFleetSpawnTools offers, in order.
var fleetSpawnSessionTools = []string{"create_session", "list_child_sessions", "list_repos", "list_models", "get_agent_usage",
	"get_session_output", "stop_session", "stop_session_after_turn", "resume_session", "rename_child_session"}

// sessionDriveAllowed authorizes a tool that ACTS ON a named session: reading its output,
// stopping it, resuming it. The operator may drive anything; a session may drive only what it
// started itself (ADR 0073 decision 4).
//
// The predicate is BOTH origin=session AND a matching parent, deliberately narrower than the
// one guarding recursion. A fork of a child keeps the lineage but is origin=handoff — a person
// made it in the Console, and nothing about being the fork source makes it the parent's to
// stop. Everything else — peers, the user's own sessions, another session's children — is
// simply not the caller's business.
//
// The advertised tool set is still the first boundary; this is the second, because unlike the
// stage 1 read tools these name a target.
func sessionDriveAllowed(name string) error {
	// The operator surface is unrestricted — including read-only (--write absent), which still
	// advertises get_session_output. The test is which SURFACE this server is, not which
	// capability it holds: keying off writeEnabled() would put a read-only assistant through
	// the child check and then refuse it for having no session of its own.
	if !selfReportOnly() {
		return nil
	}
	self, err := mcpOwningSession()
	if err != nil {
		return err
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return fmt.Errorf("セッション %q が見つかりません", name)
	}
	if session.OriginOf(m) != session.OriginSession || m.OriginSession != self {
		return fmt.Errorf("セッション %q はこのセッションが起こした子ではないので操作できません。"+
			"自分が create_session で起こしたセッションだけを止める・再開する・出力を読むことができます", name)
	}
	// An archived child is the user's decision to fold it away, and it has no row in the
	// active list. Resuming one would put a live agent on the host that nobody can see, which
	// is also how archiving would become a way around "no deleting, no archiving" (ADR 0073
	// decision 13): archive from the Console, revive from here.
	if m.Archived {
		return fmt.Errorf("セッション %q は利用者がアーカイブ済みなので操作できません。"+
			"戻すかどうかは利用者が Console で決めます", name)
	}
	return nil
}

// listModelsArgs is list_models'.
type listModelsArgs struct {
	Kind string `json:"kind"`
}

// renameChildSessionArgs is rename_child_session's.
type renameChildSessionArgs struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// createSessionArgs is create_session's.
type createSessionArgs struct {
	Dir           string `json:"dir"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	InitialPrompt string `json:"initial_prompt"`
	// Worktree is a POINTER because the default differs by surface: false for the
	// operator, true for a session (ADR 0073 decision 7). A plain bool cannot tell
	// "worktree=false, work right here" from "not mentioned".
	Worktree *bool `json:"worktree"`
	// AllowSharedWorkingCopy is a session's explicit opt-in to share its own working copy
	// with the child (needs worktree=false; the Agent enforces which directory).
	AllowSharedWorkingCopy bool   `json:"allow_shared_working_copy"`
	Branch                 string `json:"branch"`
	NewBranch              string `json:"new_branch"`
	Subdir                 string `json:"subdir"`
	// ReportBack asks a spawned child to send one intent=answer peer message home when it
	// finishes (ADR 0073 decision 9). A pointer: omitted means on, and a plain bool would
	// silently turn the report off for every caller that did not think about it.
	ReportBack *bool `json:"report_back"`
	// SpendCapUSD is the child's per-child budget (#1054). A pointer: omitted means the
	// user's default, which the Agent applies; an explicit 0 means none.
	SpendCapUSD *float64 `json:"spend_cap_usd"`
	// WorkItem is the ticket the child is started for (#3134): recorded as its launch item, so
	// its row and the item show the link and branch_name renders from it. Omitted, a child of a
	// session watches its parent's items instead.
	WorkItem *spawnWorkItem `json:"work_item"`
}

type spawnWorkItem struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	// Type is the tracker's item type (Bug, Story, ...): branch naming picks the prefix from it.
	Type string `json:"type"`
}

// spawnWorkItemOf validates and normalizes create_session's work_item; nil when none was given.
func spawnWorkItemOf(in *spawnWorkItem) (*spawnWorkItem, error) {
	if in == nil || strings.TrimSpace(in.Key) == "" {
		return nil, nil
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	key, ok := session.NormalizeLinkKey(provider, in.Key, "")
	if !ok {
		return nil, fmt.Errorf("work_item: provider must be github, jira or bitbucket, and key owner/repo#N or PROJ-12 (got %q %q)", in.Provider, in.Key)
	}
	return &spawnWorkItem{Provider: provider, Key: key, Title: strings.TrimSpace(in.Title), Type: strings.TrimSpace(in.Type)}, nil
}

// fleetSpawnTools are the tools that start sessions, and the two only a session's own children
// can be steered with. create_session is the operator's under --write and a session's under
// --fleet-spawn, one handler for both.
func fleetSpawnTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_models",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_models",
					"description": "指定エージェントで現在選べるモデル一覧を返す。model 指定で create_session する前には必ず呼び、返った id を使うこと（一覧は利用者が「使わないモデル」で除外したものを除いてある — 記憶や過去の会話にあるモデル名を推測で渡さないこと。除外モデルを渡した create_session は拒否される）。claude は固定の最新ティア別名と、利用者がエージェント設定で登録した完全モデル ID を返す。Claude Code OAuth にはアカウント連動カタログがないため、登録モデルの可否は起動時に判定される。codex／opencode／agy／copilot／cursor／kiro は接続状態を反映したライブカタログ（copilot はプラン反映 — Free は Auto のみで空になる。cursor は effort をモデル id に畳んだアカウント連動カタログ。kiro は Free でも named 指定可・既定は auto。未指定は auto ルーティング）。lcpp はログイン不要で、自前エンジンの目録に載っているモデルをそのまま返す（エンジンが無い配備では空になる）。muse はログイン済みのアカウント連動カタログを返す（既定は「製品改善に使われうる」条項の付かない最新モデル）。利用者が terra のような略称で指定した場合も、一覧から対応する完全な id（例: gpt-5.6-terra）を選ぶ。opencode は同じモデルが 2 つの課金経路で並ぶことがある（opencode-go/… = Go サブスクの範囲内、opencode/… = Zen の従量課金）。同名が両方にある場合は先に並んでいる opencode-go/… を選ぶこと（一覧の並びは利用者の設定で整形済み）。利用者が Zen を明示した場合だけ opencode/… を使う。", // model-id-lint:allow an example id; it names no default.
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind": map[string]any{"type": "string", "description": joinKinds(modelKinds(), " / ", "")},
						},
						"required": []string{"kind"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "list_models",
					"description": "Agent Fleet: list the model ids currently selectable for one agent kind. " +
						"Call it before passing model to create_session and use an id from the answer: the list already " +
						"has the user's excluded models removed, and a create with an excluded or invented id is refused. " +
						"For opencode the same model can appear on two billing routes - prefer the one listed first.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"kind": map[string]any{"type": "string", "description": joinKinds(modelKinds(), " | ", "")},
						},
						"required": []string{"kind"},
					},
				}
			},
			// Advertised in the write set, so the call is refused at the same boundary: the
			// advertised set is the scope boundary (docs/log/19 Q2). --fleet-spawn advertises it
			// too (ADR 0073 §3-b) — create_session's own description sends the caller here first,
			// so a gate left at writeEnabled() breaks the spawn path at its first step.
			refuse: func() string {
				if !writeEnabled() && !mcpFleetSpawnEnabled {
					return "このアシスタントはモデル一覧の取得を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *listModelsArgs) []byte {
				if !slices.Contains(kindNames(modelKinds()), a.Kind) {
					return mcpToolErr(req.ID, "kind には "+joinKinds(modelKinds(), " / ", "")+" のいずれかを指定してください")
				}
				out, err := agentGET("/agents/" + url.PathEscape(a.Kind) + "/models")
				if err != nil {
					return mcpToolErr(req.ID, "モデル一覧の取得に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "create_session",
			operator: func() map[string]any {
				return map[string]any{
					"name": "create_session",
					"description": "新しいコーディングセッションを起こす。dir（作業ディレクトリ）で指定したリポジトリで claude 等を起動する。" +
						"worktree=true なら dir のリポジトリから新しい独立 worktree を作って起動する（branch は基点、省略時は現在の HEAD。new_branch は新規ブランチ名、省略時は仮ブランチを自動生成）。" +
						"initial_prompt を渡すと、起動後に最初のタスクとして自動で送信される（別コールの send_to_session は不要）。" +
						"用途例：あるセッションの内容を引き継いで別セッションで続ける（先に get_session_output で文脈を読み、要約を initial_prompt に入れる）／壁打ちで固めた作業を新規セッションで開始する。" +
						"dir は list_my_sessions の dir（走っているセッションと同じ場所）か list_repos の path から選ぶ。新規セッションはリソースを消費するので、起こす前に利用者へ一言確認すること。" +
						managedKindsSentence() + "model を指定する前には必ず list_models で利用可能な id を確認し、その id を渡す。作成したセッションが入力待ちになる／異常終了すると、この会話に自動で報告が届くのでポーリングは不要。報告が届いたら内容（必要なら get_session_output）を確認して次の行動を決める。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"dir":            map[string]any{"type": "string", "description": "作業ディレクトリ（リポジトリの作業コピー等）。省略時はホーム。list_my_sessions の dir か list_repos の path を渡す。"},
							"title":          map[string]any{"type": "string", "description": "セッションの表示名（任意）。何のタスクかが分かる短い名前。"},
							"kind":           map[string]any{"type": "string", "description": "エージェント種別（任意）。" + joinKinds(operatorCreateKinds(), " | ", "（既定）") + "。agy は Antigravity CLI（接続済みのときのみ起動可）。copilot は GitHub Copilot CLI（GitHub 連携＋Copilot サブスクが前提）。cursor は Cursor CLI（接続済みのときのみ起動可）。kiro は Kiro CLI（接続済みのときのみ起動可・既定は managed ドライバ）。lcpp は自前の llama.cpp ハーネス（ログイン不要・常に managed・Terminal (CLI) 経路は無い）で、model は list_models(kind=\"lcpp\") が返す自前エンジンの目録から選ぶ。muse は Meta の Muse Code（オンデマンド導入＋ログイン済みのときのみ起動可・常に managed・Terminal (CLI) 経路は無い）。shell は生のシェルで initial_prompt/送信文字列がそのままコマンド実行される（エージェントのガードレール無し）ため、起動前に実行内容を利用者へ確認すること。"},
							"model":          map[string]any{"type": "string", "description": "モデル上書き（任意）。"},
							"effort":         map[string]any{"type": "string", "description": "推論 effort（任意）。model と一緒に指定し、list_models が返すそのモデルの efforts から選ぶ（それ以外や model 省略は拒否される）。省略時はモデルの defaultEffort。opencode と kiro は一覧に efforts が無く、ここでは値を確かめない（誤った値は子の最初のターンで失敗する）。agy と cursor は effort がモデル id に含まれるので指定しない。"},
							"initial_prompt": map[string]any{"type": "string", "description": "起動後に自動送信する最初のタスク/引き継ぎ文（任意）。"},
							"worktree":       map[string]any{"type": "boolean", "description": "dir から新しい独立 worktree を作成して起動する（任意、既定 false）。"},
							"branch":         map[string]any{"type": "string", "description": "worktree の基点ブランチ（任意、省略時は現在の HEAD）。"},
							"new_branch":     map[string]any{"type": "string", "description": "worktree に作る新規ブランチ名（任意、省略時は仮ブランチを自動生成）。"},
							"subdir":         map[string]any{"type": "string", "description": "作業ディレクトリ内の相対パス（任意）。エージェントをその配下で起動する（例: console／apps/web）。worktree=true のときは新しく作られた worktree 内の相対パスとして解釈される。存在しないパスは拒否される。"},
						},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "create_session",
					"description": "Agent Fleet: start a new session in this workspace and give it a task. It runs in " +
						"parallel with you, with its own agent, model and context. " +
						"Use it when work genuinely splits: a long independent subtask, a second repository, something " +
						"that would fill your context and starve the rest. Do not use it for work you could just do - " +
						"a session holds a whole agent's memory on a host shared with every other session. " +
						"Tell the user you are starting one, and what for. " +
						"It starts in a NEW worktree by default, so it never shares your working copy; pass " +
						"worktree=false only for a directory nobody is working in. " +
						"To run a child in YOUR OWN working copy on purpose (e.g. a reviewer on what you are implementing), " +
						"pass worktree=false and allow_shared_working_copy=true: it shares your checkout, index and branch, so tell it " +
						"(and keep to) no checkout/switch/stash/branch changes and stage and commit explicitly by path. " +
						"It works only while you are running in a canonical path (no symlink in the one you were started with; " +
						"naming the target dir through an alias is fine) and only for your own copy: another session's working " +
						"copy cannot be shared this way, and if the refusal repeats with the flag, do not retry - use a worktree. " +
						"Limits: at most " + strconv.Itoa(session.SpawnChildLimit()) + " children at a time (a slot frees when the user deletes or " +
						"archives that child" + stoppedChildExpiryClause() + " - list_child_sessions shows what " +
						"you have), a session you started cannot start its own, and shell sessions cannot be started " +
						"from here. " +
						"You are NOT told when it finishes: poll get_session_status, or leave report_back on and it " +
						"sends you one message when it is done. " +
						"Children outlive you, so before your last turn list them and tell your user which ones you " +
						"left and what state they are in - only they can delete one. " +
						"Use list_repos for dir and list_models for model and effort - do not guess either. " +
						"The child's permission mode is not yours to choose: it follows the user's per-kind default in Settings > Agents.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"dir":                       map[string]any{"type": "string", "description": "Working directory (a path from list_repos, or your own). Default: home"},
							"title":                     map[string]any{"type": "string", "description": "Short display name saying what the task is (optional)"},
							"kind":                      map[string]any{"type": "string", "description": "Agent kind: " + joinKinds(modelKinds(), " | ", " (default)") + ". shell/ssm are refused"},
							"model":                     map[string]any{"type": "string", "description": "Model id from list_models for that kind (optional)"},
							"effort":                    map[string]any{"type": "string", "description": "Reasoning effort (optional; default: the model's defaultEffort). Needs model: use one of that model's efforts from list_models; anything else, or an effort without model, is refused. opencode and kiro list none: their value is not checked here, and a wrong one fails the child's first turn. agy and cursor fold effort into the model id instead"},
							"initial_prompt":            map[string]any{"type": "string", "description": "The task, delivered as the child's first instruction. Write it for someone with none of your context: what to do, where, what done looks like. If it has to wait for a message (from you or another session), tell it to end its turn while it waits: a message to a busy session can be held until its turn ends, and waiting inside a tool (a sleep loop, a blocking wait) keeps that turn from ending"},
							"worktree":                  map[string]any{"type": "boolean", "description": "Start in a new worktree off dir. Default TRUE from a session - two agents in one working copy corrupt each other's work"},
							"allow_shared_working_copy": map[string]any{"type": "boolean", "description": "Opt in to starting the child in YOUR OWN working copy while you are running in it (default false). Needs worktree=false, dir = your working copy (an alias spelling of it is fine) and that you were started in a canonical path (no symlink); any other directory in use is still refused, and a repeated refusal means it cannot work - do not retry. You and the child then share one checkout, index and branch: no checkout/switch/stash/branch changes, stage and commit explicitly by path"},
							"branch":                    map[string]any{"type": "string", "description": "Base branch for the worktree (optional; default: current HEAD)"},
							"new_branch":                map[string]any{"type": "string", "description": "Name of the branch to create in the worktree (optional; default: generated)"},
							"subdir":                    map[string]any{"type": "string", "description": "Relative path inside the working copy to start in, e.g. console (optional)"},
							"report_back":               map[string]any{"type": "boolean", "description": "Ask the child to send you one message when it finishes. Default true. Turn it off when you will read the result in the Console instead"},
							"spend_cap_usd":             map[string]any{"type": "number", "description": "The child's own spend budget in USD, an estimate at list price (optional; default: the user's default budget; 0 = none). Past it the child stops after its turn. It is not charged to yours"},
							"work_item": map[string]any{
								"type": "object", "additionalProperties": false,
								"description": "The issue or ticket the child works on (optional). Omitted, the child watches the items you were started for or work on",
								"properties": map[string]any{
									"provider": map[string]any{"type": "string", "enum": []string{"github", "jira", "bitbucket"}},
									"key":      map[string]any{"type": "string", "description": "owner/repo#45 or PROJ-12"},
									"title":    map[string]any{"type": "string"},
									"type":     map[string]any{"type": "string", "description": "The tracker's item type, e.g. Bug or Story (picks the branch prefix)"},
								},
								"required": []string{"provider", "key"},
							},
						},
					},
				}
			},
			refuse: func() string {
				if !writeEnabled() && !mcpFleetSpawnEnabled {
					return "このアシスタントはセッションの作成を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *createSessionArgs) []byte {
				// Before the shell approval gate, the effort check and the idempotency key: an unknown
				// kind is refused here rather than after those have treated it as something it is not.
				if err := validateCreateKind(a.Kind); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				// Who is launching decides the provenance, the idempotency scope, the report route and
				// the worktree default. Everything below branches on this one answer (ADR 0073).
				parent := ""
				if selfReportOnly() {
					self, err := mcpOwningSession()
					if err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
					parent = self
				}
				// P3: a raw shell session executes arbitrary commands (no agent guardrails) — gate
				// its creation on a Discord approval when this is an unattended operator turn. A
				// session never reaches it: the Agent refuses kind=shell for origin=session outright,
				// because this gate is a no-op without a conversation (ADR 0073 decision 8).
				if a.Kind == "shell" && parent == "" {
					if err := bridgeApprovalGate(approvalLabel("create_session_shell"), shellCreateTarget(a.Dir, a.InitialPrompt)); err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
				}
				driver := ""
				// An MCP caller has no way to drive a tui pane, so it asks for managed wherever the
				// registry says that is the default (sessionx.ManagedDefaultKind). claude and agy are
				// absent because no managed route exists for them. lcpp and muse have no tui route
				// at all; the Agent's create handler defaults them to managed anyway, so naming it
				// here is the same launch spelled out.
				if managedDefaultKind(a.Kind) {
					driver = "managed"
				}
				// The worktree default differs by surface: the operator's is false (a person can say
				// "work right here"), a session's is true. Two agents in one working copy is the
				// accident the workspace policy forbids by name, and a session launching with defaults
				// would otherwise put its child in its own checkout.
				worktree := a.Worktree != nil && *a.Worktree
				if parent != "" && a.Worktree == nil {
					worktree = true
				}
				// The opt-in only means something against a worktree=false launch from a session; with
				// the default worktree it would be silently ignored, so say so instead.
				shared := parent != "" && a.AllowSharedWorkingCopy
				if shared && worktree {
					return mcpToolErr(req.ID, "allow_shared_working_copy は worktree=false と一緒に指定してください（既定の worktree=true では作業コピーは共有されません）")
				}
				initialPrompt := a.InitialPrompt
				if parent != "" {
					initialPrompt = spawnPromptFor(parent, initialPrompt, a.ReportBack)
				}
				// Deterministic idempotency key (caller + launch intent): an LLM re-issuing the same
				// create_session reproduces it, so a timed-out-then-retried create collapses onto the
				// first session instead of spawning a duplicate. The caller scope is the conversation
				// for an operator and the session's own name for a session — never empty, or two
				// sessions launching the same thing would collapse into one (ADR 0073 decision 2).
				scope := convID()
				origin, originConv := session.OriginOperator, convID()
				if parent != "" {
					scope, origin, originConv = parent, session.OriginSession, ""
				}
				// Checked before anything is created: the Agent stores an effort without validating it,
				// and an unknown value would otherwise surface only when the child's first turn fails.
				// Trimmed once and used everywhere below: the check resolves a padded id to its row, so
				// forwarding the raw value would launch a model string the check never saw, and a
				// blank model would slip past its "effort needs model" refusal.
				model, effort := strings.TrimSpace(a.Model), strings.TrimSpace(a.Effort)
				if effort != "" {
					if err := checkCreateEffort(a.Kind, model, effort); err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
				}
				// The child's ticket is checked before anything is created and stored in the inbox's
				// key shape, or its launch item would never match the item it names (#3134). A bare
				// number is refused: the child's repository is not settled yet.
				wi, err := spawnWorkItemOf(a.WorkItem)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				idemKey := CreateSessionKey(scope, a.Dir, a.Subdir, a.Kind, model, effort, initialPrompt, worktree, a.Branch, a.NewBranch, shared)
				if wi != nil {
					// The same launch for another ticket is another child, not a retry of this one.
					idemKey = CreateSessionKey(idemKey, "work_item", wi.Provider, wi.Key, "", "", "", false, "", "", false)
				}
				body := map[string]any{
					"dir":             a.Dir,
					"subdir":          a.Subdir,
					"title":           a.Title,
					"kind":            a.Kind,
					"model":           model,
					"effort":          effort,
					"initial_prompt":  initialPrompt,
					"worktree":        worktree,
					"branch":          a.Branch,
					"new_branch":      a.NewBranch,
					"driver":          driver,
					"report_to":       convID(), // docs/log/30: send the completion report to this conversation (empty = off)
					"idempotency_key": idemKey,
					// ADR 0029 §6: record explicitly who started this session. That is the axis usage
					// accounting needs to separate unattended spend (autopilot, schedules and now
					// session-spawned children) from sessions a human opened. origin_session names the
					// parent and is filled from the server's own $AF_SESSION_NAME, never from an
					// argument — the model cannot claim a parent it is not (ADR 0073 decision 1).
					"origin":         origin,
					"origin_conv":    originConv,
					"origin_session": parent,
				}
				if shared {
					body["allow_shared_working_copy"] = true
				}
				if a.SpendCapUSD != nil {
					body["spend_cap_usd"] = *a.SpendCapUSD
				}
				if wi != nil {
					body["work_item"] = map[string]any{"provider": wi.Provider, "key": wi.Key, "title": wi.Title, "type": wi.Type}
				}
				reqBody, _ := json.Marshal(body)
				// A create costs 40s + 45s at worst, over opencode's 60s per-call ceiling. The
				// heartbeat resets that clock (measured, ADR 0069); claude ignores progress for
				// timeouts and codex has tool_timeout_sec=600.
				stop := startProgressHeartbeat(req, "セッションを起動しています…")
				out, err := agentCreateSession(reqBody, idemKey)
				stop()
				if err != nil {
					return mcpToolErr(req.ID, "セッションの作成に失敗しました: "+agentErrDetail(err))
				}
				if parent != "" && strings.TrimSpace(a.InitialPrompt) == "" {
					out = withNoTaskWarning(out)
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "list_child_sessions",
			session: func() map[string]any {
				return map[string]any{
					"name": "list_child_sessions",
					"description": "Agent Fleet: list the sessions YOU started, each one's state and when its last turn " +
						"ended, and how many child slots you have left. " +
						"Call it whenever you need a child's name: create_session hands it out once and a compaction takes " +
						"it away, leaving stop_session / resume_session / get_session_output nothing to name. Call it " +
						"before your last turn too, to tell your user which children you left and how they stand. " +
						"lastTurnEndAt separates a child that FINISHED from one that never started - idle means both. It " +
						"is absent while a child is mid-turn and after a restart. " +
						"Archived sessions are not listed and hold no slot.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			// The only one of the nine that is NOT also an operator tool: the operator has
			// list_my_sessions, which sees every session and needs no lineage filter. So the gate
			// is the flag alone, refused on the call side for the same reason as the peer and
			// image tools — a guessed name in tools/call must not reach a route this server does
			// not advertise.
			refuse: func() string {
				if !mcpFleetSpawnEnabled {
					return "このセッションはセッションの起動・操縦を許可されていません（設定 > エージェント）"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
				self, err := mcpOwningSession()
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpListChildSessions(req.ID, self)
			}),
		},
		{
			name: "rename_child_session",
			session: func() map[string]any {
				return map[string]any{
					"name": "rename_child_session",
					"description": "Agent Fleet: retitle a child you started, so its name still says what it is doing. " +
						"Only your own children, and it is refused once the user has renamed that child themselves.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name":  map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
							"title": map[string]any{"type": "string", "minLength": 1, "description": "The new display title"},
						},
						"required": []string{"name", "title"},
					},
				}
			},
			// The flag ALONE, like list_child_sessions and unlike the stop/resume pair: those are
			// also operator tools, this one is not advertised on the operator surface at all. An
			// assistant that guessed the name would otherwise reach a route this server does not
			// advertise — and rename every session in the workspace, since sessionDriveAllowed waves
			// the operator through by design.
			refuse: func() string {
				if !mcpFleetSpawnEnabled {
					return "このセッションはセッションの起動・操縦を許可されていません（設定 > エージェント）"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *renameChildSessionArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				// An empty title means "revert to the auto label", which is the user's affordance in the
				// rename dialog and not something a parent asks for. Refused here as well as in the Agent
				// so the caller is told what to send instead of reading it back as an API error.
				if strings.TrimSpace(a.Title) == "" {
					return mcpToolErr(req.ID, "title（新しい題名）が必要です")
				}
				if _, ok := cleanTitle(a.Title); !ok {
					return mcpToolErr(req.ID, fmt.Sprintf("title は %d 文字以内・改行なしにしてください", sessionTitleMaxRunes))
				}
				if err := sessionDriveAllowed(a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				// title_set_by is what separates this write from the Console's on the same endpoint, and
				// the refusal that reads it (a title the user chose) lives in the Agent — not here, where
				// replacing this layer would walk around it.
				renameBody, _ := json.Marshal(map[string]string{"title": a.Title, "title_set_by": session.TitleSetByParent})
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/title/set", renameBody)
				if err != nil {
					return mcpToolErr(req.ID, "題名の変更に失敗しました: "+agentErrDetail(err))
				}
				return mcpTextResult(req.ID, out)
			}),
		},
	}
}

// operatorCreateKinds is every kind the operator's create_session offers: all of them but ssm,
// which needs an ssm_target the tool has no parameter for.
func operatorCreateKinds() []KindInfo {
	return toolKinds(func(k KindInfo) bool { return k.Kind != session.KindSSM })
}

// managedKindsSentence tells the operator which kinds create_session starts on the managed
// driver: the managed-default kinds that also have a Terminal route, then the managed-only ones.
func managedKindsSentence() string {
	byDefault := kindNames(toolKinds(func(k KindInfo) bool { return k.ManagedDefault && !k.ManagedOnly }))
	only := kindNames(toolKinds(func(k KindInfo) bool { return k.ManagedOnly }))
	return strings.Join(byDefault, "／") + " は既定で managed で開始し（TUI は使わない）、" +
		strings.Join(only, " と ") + " は常に managed で開始する。"
}
