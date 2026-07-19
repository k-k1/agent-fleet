// Session domain types. A "session" is one tmux slot the Agent runs (a coding
// agent or a plain shell / an SSM login). `kind` is the discriminator the whole
// Console keys per-agent behavior on — presentation, availability, launch params,
// and capabilities all derive from it via the agent registry (src/agents/registry).
//
// NOTE: this session `kind` is a DIFFERENT axis from a *pane's* kind (which VIEW
// renders — terminal/file/scm/doc/diff, see types/layout PaneKind). Keep distinct.

export type SessionKind = "claude" | "codex" | "opencode" | "agy" | "shell" | "ssm";

// The canonical session kinds in display order (New Session buttons, etc.).
export const SESSION_KINDS: SessionKind[] = ["claude", "codex", "opencode", "agy", "shell", "ssm"];

// Live run state, reported by per-agent hooks/plugins. "" (empty) = idle. Only
// claude emits question/plan/permission; codex/opencode emit working/idle only;
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
  path?: string; // absolute working dir
  dir?: string; // working dir shown in the row tooltip
  remoteUrl?: string; // clone URL (agent sessions with a repo)
  state?: SessionState | string; // live hook/plugin state ("" = idle)
  alive?: boolean; // tmux session is running
  resumable?: boolean; // a stopped session whose dir still exists (false = archive only)
  backgroundBusy?: boolean; // idle by hook but a run_in_background task is still running
  backgroundBusyReason?: string;
  createdAt?: string; // ISO timestamp
  model?: string; // claude model
  context?: unknown; // claude context-window usage (shape owned by the chat view)
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
}

// isManagedSession: a managed (paneless) session has no tmux pane — the chat
// mirror is its primary UI, no terminal view exists, and its inputs go through
// the semantic /turn・/respond ops instead of TUI key driving (docs/27 §10).
export const isManagedSession = (s?: { driver?: string } | null): boolean =>
  s?.driver === "managed";

// Provider connection status for one agent, from GET /api/connections.
export interface ProviderConn {
  connected?: boolean;
  envs?: unknown[]; // opencode: configured provider API-key envs
  // agy: host capability (docs/32 Track B — RDRAND ガード)。false = this host
  // cannot run agy ("no_rdrand" / "not_installed"); absent = supported.
  supported?: boolean;
  reason?: string;
}

// The full connections bag. Known agents are named; git providers (github /
// bitbucket) and any future entries ride the index signature.
export interface ConnectionsStatus {
  claude?: ProviderConn;
  codex?: ProviderConn;
  opencode?: ProviderConn;
  agy?: ProviderConn;
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
