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
  // Control route (docs/log/27): "managed" = shared runtime + structured RPC (AF is the only
  // writer, no tmux pane). absent/"tui" = the classic TUI inside tmux. Whether a pane exists
  // is decided by this axis, not by kind — always branch through isManagedSession().
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
  // The reserved auto-resume instant (RFC3339), present only while state === "limited"
  // (waiting for a usage limit to reset). Empty = no resume is scheduled (auto-resume off,
  // nothing to derive a time from, or a per-model limit). Display-only, so the chip can say
  // when the session moves again (docs/log/47 §4-9).
  rateLimitResumeAt?: string;
  createdAt?: string; // ISO timestamp
  model?: string; // claude model
  context?: SessionContextUsage; // claude context-window usage (the Agent's session.ContextUsage)
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
  // Which kind of interaction was still awaiting an answer when the session was stopped
  // (docs/log/75). Only set on stopped rows, where it turns the list badge into "stopped,
  // question pending". While the session is alive, `state` says the same thing.
  carried?: "question" | "plan" | "permission" | string;
  // Deletion lock (docs/log/45): while true, the Agent answers 403 to anything that deletes
  // (delete = forget the metadata, purge, the 7-day auto-prune of stopped sessions, and
  // removal as a side effect of deleting the working copy). Stop and archive are reversible
  // and still go through. The row's lock badge and the disabled delete items read this.
  locked?: boolean;
  // Keep-awake pin (docs/log/75): exempt from the idle auto-stop until this instant.
  // Past or empty = no pin is held.
  keepAwakeUntil?: string;
}

// A session's current context fill — the wire shape of the Agent's
// session.ContextUsage (internal/session/session.go). Field names deliberately match
// the ContextBar props so the terminal/chat heads can spread it straight in.
export interface SessionContextUsage {
  read: number; // cache_read_input_tokens (cache reuse)
  create: number; // cache_creation_input_tokens (new cache)
  fresh: number; // input_tokens (uncached)
  model?: string;
}

// isManagedSession: a managed (paneless) session has no tmux pane — the chat
// mirror is its primary UI, no terminal view exists, and its inputs go through
// the semantic /turn and /respond ops instead of TUI key driving (docs/log/27 §10).
export const isManagedSession = (s?: { driver?: string } | null): boolean =>
  s?.driver === "managed";

// Provider connection status for one agent, from GET /api/connections.
export interface ProviderConn {
  connected?: boolean;
  envs?: string[]; // opencode: configured provider API-key env names (auth.go)
  // agy: host capability (docs/log/32 Track B — the RDRAND guard). false = this host
  // cannot run agy ("no_rdrand" / "not_installed"); absent = supported.
  supported?: boolean;
  reason?: string;
  // Chat integrations (discord / slack): the display form of the notification master switch
  // (the inverse of notifyOff). OFF only when false is explicit — unset (an older
  // connection) counts as ON.
  notify?: boolean;
  // claude (docs/log/47 §4-8): the OAuth credential's expiry. `claude auth status` never
  // reports an expiry, so the Agent reads refreshTokenExpiresAt out of the credential itself
  // and puts it here. expired = both the refresh and the access token are past (no turn can
  // start any more); days_left = an advance warning, present only within 3 days of expiry.
  // Their absence means there is nothing to judge from (API-key operation, not connected, a
  // changed format) — it does NOT mean expired.
  expires_at?: string;
  expired?: boolean;
  days_left?: number;
  // opencode: the selected billing route (docs/log/54). "free" is a tier that launches with
  // no authentication at all, so the launch gate reads this and allows opencode even when
  // not connected. "off" is the opposite: an explicit disable that closes the launch gate
  // even when a key or OAuth exists (connected becomes false).
  usage?: "off" | "free" | "go" | "zen";
  // opencode: an opencode account connection, which coexists with API keys (envs) (docs/log/54).
  oauth?: boolean; // connected through the Console account
  oauth_label?: string; // organisation name of the connection (the label opencode returns)
  oauth_known?: boolean; // false = unverified because the serve daemon is not up (not necessarily disconnected)
  oauth_disabled?: boolean; // managed opencode is disabled, so no sign-in route can be offered
  // opencode: a link to the usage page (opencode.ai/workspace/{id}/go). There is no API for
  // the numbers, so this holds only the ID, the URL, and whatever quota information could be
  // observed when a limit was hit.
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
  // cursor (docs/log/40): its own login flow. connected = ~/.config/cursor/auth.json exists;
  // supported=false = an older image without the CLI baked in.
  cursor?: ProviderConn;
  opencode?: ProviderConn;
  agy?: ProviderConn;
  // copilot rides on the GitHub integration (docs/log/36): connected = a gh token exists;
  // supported=false = an older image without the CLI baked in.
  copilot?: ProviderConn;
  // kiro (docs/log/43): device-flow login. connected = credentials exist (whoami exit 0);
  // supported=false = the CLI is not installed (before on-demand install; ~855MB goes to the
  // per-user home).
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
}
