// Package session はセッションのモデル（ワイヤ構造体・永続メタ・kind 定数）と
// その付帯ヘルパ（tmux 名前規約・UUID・メタ永続化）。package main からの抽出
// （docs/23 残① Wave A）。JSON タグ／ディスク上のレイアウトは main 時代と
// バイト同一を維持すること。
package session

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Canonical kind list. These are the persisted Meta.Kind / wire Session.Kind
// values; keep them in sync with the agent registry (package main) and the
// Console's Session type.
const (
	KindClaude   = "claude"
	KindOpencode = "opencode"
	KindCodex    = "codex"
	KindCursor   = "cursor"
	KindKiro     = "kiro"
	KindAgy      = "agy"
	KindCopilot  = "copilot"
	KindShell    = "shell"
	KindSSM      = "ssm"
)

// Driver 方式（docs/27 §2・§9.2、ADR 0015）: セッションの制御経路。tui は従来の
// tmux 内 TUI（AF は send-keys で書き・スクレイプ/hooks で読む）。managed は共有
// runtime＋構造化 RPC — AF が唯一の writer で、tmux pane を持たない（P2 で opencode、
// P3 で codex）。kind は分けない — transcript / settings / auth / models を tui と
// 共有するため、driver は Meta のフィールドで持つ（ADR 0015-決定 9.2）。
const (
	DriverTUI     = "tui"
	DriverManaged = "managed"
)

// セッションの出自（docs/46 §2-c・ADR 0029 §6）: 誰が始めたセッションの消費か。
// ターン注入元（transcript.Turn.Source）とは別の軸で、「自分で開いたセッション」と
// 「オペレーターが勝手に立てたセッション」を使用量集計で分けるために持つ — 後者は
// 自動走行・定時実行と組み合わさると無人で増える。
const (
	OriginUser     = "user"     // Console の起動導線から人が開始（既定）
	OriginOperator = "operator" // af_write アシスタントの create_session（＋作成元の会話）
	OriginSchedule = "schedule" // 定時実行が起こした（docs/38）
	OriginHandoff  = "handoff"  // 引き継ぎ（旧 fork）で生えた
	// OriginUnknown はこの機能より前に作られた既存セッション。0 でも user でもない、を守る。
	OriginUnknown = "unknown"
)

// ValidOrigin は外部から届いた出自を記録可能な語彙へ丸める。create のワイヤ項目は
// どのクライアントからも到達しうるので、未知の値は user（ラベル無しで通る人の操作）へ
// 縮退させ、任意の文字列が集計の次元に混ざらないようにする。
func ValidOrigin(s string) string {
	switch s {
	case OriginUser, OriginOperator, OriginSchedule, OriginHandoff, OriginUnknown:
		return s
	}
	return OriginUser
}

// OriginOf は集計用の出自。フィールドを持たない既存メタは unknown（推定で user に
// 寄せると「人が開いた分」を過大に見せてしまう）。
func OriginOf(m Meta) string {
	if m.Origin == "" {
		return OriginUnknown
	}
	return m.Origin
}

// tmux session naming: friendly name "slot01" <-> tmux "claude_slot01".
const TmuxPrefix = "claude_"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// ValidName reports whether s is a well-formed session name (slug).
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Session is the wire representation of a Claude session (one tmux session).
type Session struct {
	Name string `json:"name"`
	Tmux string `json:"tmux"`
	Dir  string `json:"dir"`
	Kind string `json:"kind"` // "claude" | "opencode" | "codex" | "shell"
	// Driver mirrors Meta.Driver on the wire（"" = tui）。managed のセッションは
	// tmux pane を持たないので、Console はこれを見てターミナルビューを出さず
	// ミラー（チャット）を主 UI として描画する（docs/27 §10）。
	Driver string `json:"driver,omitempty"`
	// Subdir mirrors Meta.Subdir: the folder beneath Dir the agent actually runs in
	// ("" = Dir itself). Dir stays the working copy, so the Console keeps grouping
	// sessions by copy and only shows this as extra "where inside it" detail.
	Subdir        string `json:"subdir,omitempty"`
	Repo          string `json:"repo"` // working dir basename (display)
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	Title         string `json:"title"`     // user-supplied display title (optional, any kind)
	Display       string `json:"display"`   // human-readable name (title → claude label → repo@time); never the slug alone
	Color         string `json:"color"`     // terminal background hue (hex); SSM carries its host color
	Label         string `json:"label"`     // claude --name display name (claude only)
	Started       string `json:"started"`   // "01/02 15:04" local time, for the list
	CreatedAt     string `json:"createdAt"` // RFC3339
	RemoteUrl     string `json:"remoteUrl"` // claude.ai Remote Control URL, when RC is bridged
	State         string `json:"state"`     // claude live state: working | idle | question | ""
	Alive         bool   `json:"alive"`     // true = live tmux session; false = stopped
	Resumable     bool   `json:"resumable"` // false = stopped claude whose working dir is gone
	// BackgroundBusy: state is idle (turn done) but a run_in_background task is still
	// running under the pane. Lets the Console mark 入力待ち as "still working in bg".
	BackgroundBusy bool `json:"backgroundBusy"`
	// RateLimitResumeAt: State == agents.StateLimited のとき**だけ**入る、予約済み自動再開の
	// 時刻（RFC3339）。空 = 上限では止まっているが再開は仕込まれていない（自動再開 OFF、
	// リセット時刻を決める材料が無い、モデル別上限 — docs/47 §4-5）。チップに「いつ動くか」を
	// 出すためだけの表示用で、待ち合わせ自体は CP の定時実行が持つ。
	RateLimitResumeAt string `json:"rateLimitResumeAt,omitempty"`
	// Context: current context-window fill (newest assistant turn's prompt tokens),
	// claude only, nil when none recorded yet. Drives the Console's ContextBar in
	// both the terminal and chat heads without a separate transcript poll.
	Context *ContextUsage `json:"context,omitempty"`
	// Branch is the session's start branch (Meta.Branch). CurrentBranch is the
	// working copy's branch right now; it is set ONLY when it differs from Branch, at
	// which point BranchDrift is true — the working tree was switched under the session
	// (a checkout that bypassed the guard). The Console badges the row so the mishap is
	// visible even though it can't be prevented at the git layer.
	Branch        string `json:"branch,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	BranchDrift   bool   `json:"branchDrift,omitempty"`
	// Worktree marks a session running in a linked git worktree — the Console offers
	// branch rename (deferred naming) only for these, since renaming a standalone
	// clone's branch is a different, rarer intent.
	Worktree bool `json:"worktree,omitempty"`
	// ExitReason explains why a STOPPED session's agent process terminated, when the
	// pane exit recorder caught an abnormal end: "oom" (memory-killed), "killed"
	// (SIGKILL, non-OOM), or "crashed" (fault / non-zero exit). Empty for live sessions,
	// clean quits, and deliberate stops — those show the plain 停止中 chip. ExitCode is
	// the raw pane wait status (128+signal on a kill; 137 = OOM SIGKILL) and ExitSignal
	// the derived signal number, both surfaced in the row tooltip.
	ExitReason string `json:"exitReason,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	ExitSignal int    `json:"exitSignal,omitempty"`
	// Carried は「畳まれたときに画面に出ていた対話」の種類（"question" | "plan" |
	// "permission"）。停止中の行にだけ入る（docs/75 §75.6.5）。
	//
	// なぜ一覧に要るか: 停止中セッションの状態は 停止中 の 1 語しか無く、答えを待って
	// いる質問があることは**どこにも出ていなかった**。人待ちを畳めるようにした以上
	// （docs/75 P2）、畳まれた質問が一覧から見えないのは「静かに失われた」のと区別が
	// つかない。ミラーを開けばカードは出るが、開く理由が無ければ開かない。
	Carried string `json:"carried,omitempty"`
	// Locked mirrors Meta.Locked: the user pinned this session against deletion, so
	// every removal path (stop=forget meta / delete / TTL prune / a working-copy
	// delete that would take it down with it) refuses until it is unlocked. The
	// Console badges the row and disables its delete item off this flag.
	Locked   bool `json:"locked,omitempty"`
	Archived bool `json:"archived,omitempty"`
	// KeepAwakeUntil mirrors Meta.KeepAwakeUntil: while it is in the future the
	// idle-stop reaper leaves this session AND its workspace alone (docs/75)。
	// 停止中の行にも載せる — 掛かっているピンは、切れる前に見えていないと外せない。
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
}

// ContextUsage is a claude session's current context fill — the newest assistant
// turn's prompt token breakdown. It is serialized into Session.Context so the
// Console can render the /context-like gauge in BOTH the terminal and chat heads
// straight off the sessions list, with no separate transcript poll. The field
// names match the Console's ContextBar props (read / create / fresh / model).
type ContextUsage struct {
	Read   int    `json:"read"`   // cache_read_input_tokens (reused cache)
	Create int    `json:"create"` // cache_creation_input_tokens (newly cached)
	Fresh  int    `json:"fresh"`  // input_tokens (uncached)
	Model  string `json:"model"`
}

func TmuxName(name string) string { return TmuxPrefix + name }

// ExactTarget returns a tmux target that matches NAME exactly. Without the leading
// '=', tmux's -t resolution prefix-matches, so a target like "claude_agent-fleet"
// would match an unrelated "claude_agent-fleet-sh" — wrongly reporting "already
// running" (blocking session creation) or killing the sibling on
// stop/archive/recreate.
func ExactTarget(tn string) string { return "=" + tn }

// Meta records how to (re)launch a session. tmux destroys a session when
// its program exits (e.g. the user quits claude), losing the kind/dir/model we
// need to relaunch. We persist it in the home volume so the session stays listed
// and clicking it re-runs claude --resume in the SAME session id (derived from
// dir+name). Home survives Stop→Start, so a stopped session remains listed and
// resumable across a Workspace restart (claude --resume reads the jsonl, also
// persisted). The dir is denylisted in the file browser. "作り直す"(recreate)
// wipes home, intentionally clearing sessions too.
type Meta struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Subdir narrows the agent's CWD to a folder BENEATH Dir (slash-relative, e.g.
	// "console/src"), chosen at launch. Dir stays the working copy root so everything
	// that reasons about the copy — worktree pruning, the checkout guard, cleanup
	// grouping, the Console's per-repo grouping — keeps working unchanged; only the
	// launched process starts deeper (see CWD). "" = start at Dir, the default.
	Subdir string `json:"subdir,omitempty"`
	Model  string `json:"model"`
	// Effort / Mode are the desired managed-thread settings. They live beside Model
	// so a successful dynamic change survives Agent/workspace restarts and is inherited
	// by fork/recreate. TUI sessions leave both empty.
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Kind   string `json:"kind"`
	// Driver selects the control route（docs/27）: "" | "tui" = tmux 内 TUI（従来）、
	// "managed" = 共有 runtime＋構造化 RPC（pane なし。P2 で opencode から解禁）。
	// 既定の tui は "" で永続化し、既存メタとディスク上バイト同一を保つ。
	Driver string `json:"driver,omitempty"`
	Title  string `json:"title"` // user-supplied display title (optional); "" = auto
	// SuggestedTitle is a headless-LLM-generated candidate the Console offers via a
	// dismissible banner once the session has had a couple of exchanges and has no
	// user title yet. "" = none pending (not generated yet, already accepted into
	// Title, or dismissed).
	SuggestedTitle string `json:"suggestedTitle,omitempty"`
	// SuggestedTitleDismissed latches true once the user accepts OR dismisses a
	// suggestion, so a session is offered one at most once (v1: no re-suggestion loop).
	SuggestedTitleDismissed bool   `json:"suggestedTitleDismissed,omitempty"`
	Color                   string `json:"color"` // terminal background hue (hex); set at create (SSM host color)
	Label                   string `json:"label"` // claude --name (display); derived from Title at create/recreate
	Repo                    string `json:"repo"`  // working dir basename
	// Branch is the git branch the working copy (Dir) was on when this session was
	// created/recreated. Compared against Dir's current branch on each list to flag
	// drift — a `git checkout` that slipped past the checkout guard (agent/manual
	// shell inside the session). "" when Dir isn't a git working tree, or for
	// pre-existing sessions minted before this field. Never rewritten after create,
	// so the drift comparison stays meaningful.
	Branch    string `json:"branch,omitempty"`
	CreatedAt string `json:"createdAt"` // RFC3339, set at create
	StoppedAt string `json:"stoppedAt"` // RFC3339, set lazily when first seen exited; "" while live
	Archived  bool   `json:"archived"`  // true = hidden from the active list, restorable (jsonl kept)
	// Locked pins the session against deletion: /stop（メタ忘却）・DELETE /sessions/{name}・
	// 停止中 TTL の自動 prune・作業コピー削除の巻き添え、いずれも locked の間は拒否される
	// （アーカイブは可逆なので許可）。解除は POST /sessions/{name}/lock {"locked":false}。
	// 保護は Agent の REST 層で効くので、Console・オペレーター（MCP）・ブリッジのどこから
	// 来た削除でも同じように止まる。
	Locked bool `json:"locked,omitempty"`
	// KeepAwakeUntil pins the session (and with it the workspace) against the idle-stop
	// reaper until this instant (RFC3339). Empty / past = not pinned.
	//
	// なぜ「時刻」で、真偽値ではないのか（docs/75 §75.5）: 消し忘れたピンは、閉じ忘れた
	// 端末タブと同じ「黙って課金し続けるもの」になる。止めない理由が本物なら数時間で済み、
	// 本物でなければ勝手に切れてほしい。延長は押し直せばよい。
	//
	// なぜ要るのか: shell / ssm のセッションは「今ジョブが走っているか」を af から見分けられ
	// ない（前景コマンド名では放置された less とビルドが同じに見え、ssm は常に aws を張る）。
	// 推測で守るのではなく、利用者に宣言してもらう側に倒した判断。
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
	// ForkFrom is the SOURCE conversation id this session was forked from, in the
	// kind's own id space: claude = the source slot's sid (jsonl), opencode = its
	// ses_… id, codex = its session uuid. It only affects the FIRST launch — each
	// kind's BuildLaunch turns it into the CLI's fork invocation (claude --resume
	// <id> --fork-session --session-id <ownsid> / opencode --session <id> --fork /
	// codex fork <id>), which copies the source history into this session's own
	// conversation. Once that exists, later launches resume normally and ForkFrom
	// is ignored — a restart never re-forks. Empty for non-forked sessions.
	ForkFrom string `json:"forkFrom,omitempty"`
	// ForkAt narrows ForkFrom to a POINT in the source conversation: this session
	// carries the source's history up to — but NOT including — the anchored turn
	// (docs/55 §55.3). The value is whatever the kind's ForkAtResolver produced from the
	// Console's anchor, already translated into that engine's inclusivity (opencode =
	// the exclusive messageID, codex = the inclusive lastTurnId of the PREVIOUS turn).
	// Empty = whole-conversation fork, the pre-existing behaviour. Like ForkFrom it only
	// affects the FIRST launch; afterwards the session resumes its own conversation.
	ForkAt string `json:"forkAt,omitempty"`
	// Origin / OriginConv はこのセッションの出自（Origin* 定数・ADR 0029 §6）。使用量
	// 集計で「人が始めた消費」と「オペレーター/定時が無人で回した消費」を分ける軸。
	// 未設定＝この機能より前のセッションで、OriginOf が unknown に読み替える（既定値の
	// user へ寄せない）。OriginConv は origin=operator のとき作成元のアシスタント会話 slug。
	// recreate は元の出自を継承し、handoff は handoff を立てる。
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"originConv,omitempty"`
	// SSM holds the (non-secret) coordinates for a kind=ssm session: which instance,
	// run-as document, region, and the SSO profile to authenticate with. Persisted so
	// a relaunch regenerates ~/.aws/config and re-runs `aws sso login` (if the cached
	// token expired) before start-session. No AWS credentials are stored anywhere —
	// the aws CLI obtains them via SSO at launch and caches them in the home volume.
	SSM *SSMMeta `json:"ssm,omitempty"`
}

// SSMMeta is the persisted, non-secret description of an SSM login target.
type SSMMeta struct {
	Profile   string `json:"profile"`   // ~/.aws/config profile name (derived from alias)
	Target    string `json:"target"`    // EC2 instance id (i-...)
	Document  string `json:"document"`  // run-as SSM document ("" = default shell)
	Region    string `json:"region"`    // instance region ("" = profile default)
	StartURL  string `json:"startUrl"`  // SSO access-portal start URL ("" = use existing ~/.aws)
	SSORegion string `json:"ssoRegion"` // SSO region
	AccountID string `json:"accountId"` // SSO account id
	RoleName  string `json:"roleName"`  // SSO permission-set role name
}

// DriverKind normalizes Meta.Driver（"" → tui）。分岐は必ずこれを介す — 生の
// Meta.Driver 比較だと既定（空文字）の扱いが呼び出し毎にぶれる。
func (m Meta) DriverKind() string {
	if m.Driver == "" {
		return DriverTUI
	}
	return m.Driver
}

// StoppedTTL is how long a stopped (exited) session stays listed/resumable before
// it is pruned. Configurable; default 7d (metas now persist across Stop→Start, so
// the window spans restarts). A session running at shutdown is marked stopped on
// the next list after restart, starting its TTL then.
func StoppedTTL() time.Duration {
	if v := os.Getenv("AF_SESSION_STOPPED_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 7 * 24 * time.Hour
}

// Display derives a human-readable session name, mirroring the Console's
// displayName (lib/sessionview.ts): the user title if set; else a claude session's
// --name label (minus the "[AF] " tag); else "{repo}@MMDD-HHMM". The random slug
// (Name) is never surfaced alone — it's an opaque id users don't recognize, so callers
// (e.g. the Fleet Operator) should report Display, not Name.
func Display(m Meta) string {
	if m.Title != "" {
		return m.Title
	}
	if m.Label != "" {
		return strings.TrimLeft(strings.TrimPrefix(m.Label, "[AF]"), " ")
	}
	base := m.Repo
	if base == "" {
		base = m.Name
	}
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			return base + " @" + t.Local().Format("0102-1504")
		}
	}
	return base
}

// DirExists reports whether p is an existing directory.
func DirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// CWD is the directory the session's agent process actually starts in: Dir, or the
// Subdir beneath it when one was chosen at launch. A Subdir that no longer exists
// (deleted, or a branch switch that removed the folder) falls back to Dir rather than
// failing the launch — a session must stay startable, and the working copy root is
// always a defensible place to land.
func (m Meta) CWD() string {
	if m.Subdir == "" {
		return m.Dir
	}
	p := filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))
	if !DirExists(p) {
		return m.Dir
	}
	return p
}

// CleanSubdir normalizes a launch-time subdir into the slash-relative form Meta
// stores, and reports whether it is acceptable at all. Absolute paths and any ".."
// escape are rejected outright: the field means "beneath the working copy", and a
// caller that wants another copy passes a different dir.
func CleanSubdir(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	if s == "" {
		return "", true
	}
	// Rejected BEFORE the slashes are trimmed: "/x" is almost always someone pasting an
	// absolute path, and quietly reading it as repo-relative would land them elsewhere.
	if strings.HasPrefix(s, "/") || filepath.IsAbs(s) || strings.HasPrefix(s, "~") {
		return "", false
	}
	if s = strings.Trim(s, "/"); s == "" {
		return "", true
	}
	c := path.Clean(s)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}
