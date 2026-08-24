// Session domain types. A "session" is one tmux slot the Agent runs (a coding
// agent or a plain shell / an SSM login). `kind` is the discriminator the whole
// Console keys per-agent behavior on — presentation, availability, launch params,
// and capabilities all derive from it via the agent registry (src/agents/registry).
//
// NOTE: this session `kind` is a DIFFERENT axis from a *pane's* kind (which VIEW
// renders — terminal/file/scm/doc/diff, see layout/types PaneContent). Keep distinct.

export type SessionKind = "claude" | "codex" | "cursor" | "copilot" | "kiro" | "opencode" | "agy" | "shell" | "ssm";

// The canonical session kinds in display order (New Session buttons, etc.).
export const SESSION_KINDS: SessionKind[] = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "shell", "ssm"];

// Live run state, reported by per-agent hooks/plugins. "" (empty) = idle. claude
// emits question/plan/permission; codex/opencode emit working/idle; agy emits
// question/permission only (conversation-DB probe — no working/idle hooks);
// shell/ssm emit nothing (their liveness is shown from `alive`).
export type SessionState = "" | "working" | "idle" | "question" | "plan" | "permission";

// A session as returned by GET /api/sessions and used across the left pane, the
// terminal header, and the chat mirror. Optional fields may be absent per kind.
export interface Session {
  name: string; // auto-allocated unique slug ("s" + 6 base32 chars, e.g. "sukbq4s") — the session's immutable identity
  kind: SessionKind;
  // Control route (docs/27): "managed" = 共有 runtime＋構造化 RPC（AF が唯一の
  // writer・tmux pane なし）。absent/"tui" = 従来の tmux 内 TUI。pane の有無は kind
  // でなくこの軸で決まる — 分岐は必ず isManagedSession() を介す。
  driver?: "tui" | "managed" | string;
  title?: string; // user-supplied display title (optional, any kind); "" = auto
  color?: string; // terminal background hue (hex); SSM sessions carry their host color
  label?: string; // claude --name (with an "[AF] " tag); absent for shell
  repo?: string | null; // working-copy folder the (agent) session runs in
  workingCopyId?: string;
  path?: string; // absolute working dir
  dir?: string; // working dir shown in the row tooltip
  // Folder BENEATH dir the agent actually runs in, slash-relative ("" / absent = dir
  // itself). Sessions stay grouped by `dir` (the working copy) — this is only the
  // extra "where inside it" detail.
  subdir?: string;
  remoteUrl?: string; // clone URL (agent sessions with a repo)
  state?: SessionState | string; // live hook/plugin state ("" = idle)
  alive?: boolean; // tmux session is running
  resumable?: boolean; // a stopped session whose dir still exists (false = archive only)
  backgroundBusy?: boolean; // idle by hook but a run_in_background task is still running
  backgroundBusyReason?: string;
  // state === "limited"（利用上限のリセット待ち）のときだけ入る、予約済み自動再開の時刻
  // （RFC3339）。空 = 再開は仕込まれていない（自動再開 OFF／時刻を決める材料が無い／
  // モデル別上限）。チップに「いつ動くか」を出すためだけの表示用（docs/47 §4-9）。
  rateLimitResumeAt?: string;
  createdAt?: string; // ISO timestamp
  model?: string; // claude model
  context?: SessionContextUsage; // claude context-window usage (Agent 側 session.ContextUsage)
  branch?: string; // git branch the working copy was on when the session started
  currentBranch?: string; // working copy's branch now, set only when it differs from `branch`
  branchDrift?: boolean; // true = the working tree was switched off `branch` under the session
  worktree?: boolean; // session runs in a linked git worktree (offers branch rename)
  // Why a STOPPED session's agent process ended, when it ended abnormally: "oom"
  // (memory-killed), "killed" (SIGKILL, non-OOM) or "crashed" (fault / non-zero exit).
  // Absent for live sessions, clean quits, and deliberate stops. exitCode is the raw
  // pane wait status (137 = OOM SIGKILL); exitSignal the derived signal number.
  exitReason?: "oom" | "killed" | "crashed" | string;
  exitCode?: number;
  exitSignal?: number;
  // 畳まれたときに答えを待っていた対話の種類（docs/75）。停止中の行にだけ入り、
  // 一覧のバッジを 停止中・質問あり にする。稼働中は state が同じことを語る。
  carried?: "question" | "plan" | "permission" | string;
  // 削除ロック（docs/45）: true の間、削除系（削除＝メタ忘却・完全削除・停止中の
  // 7日自動prune・作業コピー削除の巻き添え）を Agent が 403 で拒否する。停止/
  // アーカイブは可逆なので通る。行の鍵バッジと削除項目の無効化はこれを見る。
  locked?: boolean;
  // 停止しないピン（docs/75）: この時刻までアイドル自動停止の対象外。過去/空 = 掛かっていない。
  keepAwakeUntil?: string;
}

// A session's current context fill — the wire shape of the Agent's
// session.ContextUsage (internal/session/session.go). Field names deliberately match
// the ContextBar props so the terminal/chat heads can spread it straight in.
export interface SessionContextUsage {
  read: number; // cache_read_input_tokens（再利用キャッシュ）
  create: number; // cache_creation_input_tokens（新規キャッシュ）
  fresh: number; // input_tokens（非キャッシュ）
  model?: string;
}

// isManagedSession: a managed (paneless) session has no tmux pane — the chat
// mirror is its primary UI, no terminal view exists, and its inputs go through
// the semantic /turn・/respond ops instead of TUI key driving (docs/27 §10).
export const isManagedSession = (s?: { driver?: string } | null): boolean =>
  s?.driver === "managed";

// Provider connection status for one agent, from GET /api/connections.
export interface ProviderConn {
  connected?: boolean;
  envs?: string[]; // opencode: configured provider API-key env names (auth.go)
  // agy: host capability (docs/32 Track B — RDRAND ガード)。false = this host
  // cannot run agy ("no_rdrand" / "not_installed"); absent = supported.
  supported?: boolean;
  reason?: string;
  // チャット連携（discord / slack）: 通知マスタの表示形（notifyOff の反転）。
  // false 明示のときだけ OFF — 未設定（旧接続）は ON 扱い。
  notify?: boolean;
  // claude（docs/47 §4-8）: OAuth 資格情報の期限。`claude auth status` は期限を一切
  // 返さないので、Agent が資格情報の refreshTokenExpiresAt を直接読んで載せている。
  // expired = 更新トークンもアクセストークンも過ぎた（＝もうターンを開始できない）、
  // days_left = 期限まで 3 日以内のときだけ入る予告。無ければ判断材料が無い
  // （APIキー運転・未接続・形式変更）で、**期限切れではない**。
  expires_at?: string;
  expired?: boolean;
  days_left?: number;
  // opencode: 選択中の課金経路（docs/54）。"free" は認証ゼロで起動できる枠なので、
  // 起動ゲートはこれを見て未接続でも opencode を許す。"off" は逆に、鍵や OAuth が
  // あっても起動ゲートを閉じる明示的な無効化（connected は false になる）。
  usage?: "off" | "free" | "go" | "zen";
  // opencode: APIキー（envs）と併存する opencode アカウント接続（docs/54）。
  oauth?: boolean; // Console アカウントで接続済みか
  oauth_label?: string; // 接続先の組織名（opencode が返すラベル）
  oauth_known?: boolean; // false = serve daemon 未起動で未確認（未接続とは限らない）
  oauth_disabled?: boolean; // マネージド opencode が無効でサインイン導線を出せない
  // opencode: 利用枠ページ（opencode.ai/workspace/{id}/go）への導線。数値は API が無く
  // 取り込めないので、ID と URL、上限に当たったときに観測できた枠情報だけを持つ。
  workspace_id?: string;
  workspace_id_source?: "manual" | "learned";
  workspace_url?: string;
  last_limit?: { name?: string; reset_at?: string };
}

// The full connections bag. Known agents are named; git providers (github /
// bitbucket) and any future entries ride the index signature.
export interface ConnectionsStatus {
  claude?: ProviderConn;
  codex?: ProviderConn;
  // cursor（docs/40）: 専用ログインフロー型。connected = ~/.config/cursor/auth.json
  // あり、supported=false = CLI 未焼き込みの旧イメージ。
  cursor?: ProviderConn;
  opencode?: ProviderConn;
  agy?: ProviderConn;
  // copilot は GitHub 連携相乗り（docs/36）: connected = gh トークンあり、
  // supported=false = CLI 未焼き込みの旧イメージ。
  copilot?: ProviderConn;
  // kiro（docs/43）: device-flow ログイン型。connected = 資格情報あり（whoami exit 0）、
  // supported=false = CLI 未導入（オンデマンド導入前・~855MB は per-user home 行き）。
  kiro?: ProviderConn;
  [provider: string]: ProviderConn | undefined;
}

// A registered SSM host bookmark, from GET /api/ssm/hosts.
export interface SsmHost {
  id: string;
  alias: string;
  profileId: string;
  region: string;
  instanceId: string;
  documentName: string;
  accountId?: string;
}
