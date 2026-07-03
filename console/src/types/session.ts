// Session domain types. A "session" is one tmux slot the Agent runs (a coding
// agent or a plain shell / an SSM login). `kind` is the discriminator the whole
// Console keys per-agent behavior on — presentation, availability, launch params,
// and capabilities all derive from it via the agent registry (src/agents/registry).
//
// NOTE: this session `kind` is a DIFFERENT axis from a *pane's* kind (which VIEW
// renders — terminal/file/scm/doc/diff, see types/layout PaneKind). Keep distinct.

export type SessionKind = "claude" | "codex" | "opencode" | "shell" | "ssm";

// The canonical session kinds in display order (New Session buttons, etc.).
export const SESSION_KINDS: SessionKind[] = ["claude", "codex", "opencode", "shell", "ssm"];

// Live run state, reported by per-agent hooks/plugins. "" (empty) = idle. Only
// claude emits question/plan/permission; codex/opencode emit working/idle only;
// shell/ssm emit nothing (their liveness is shown from `alive`).
export type SessionState = "" | "working" | "idle" | "question" | "plan" | "permission";

// A session as returned by GET /api/sessions and used across the left pane, the
// terminal header, and the chat mirror. Optional fields may be absent per kind.
export interface Session {
  name: string; // auto-allocated unique slug ("s7") — the session's immutable identity
  kind: SessionKind;
  title?: string; // user-supplied display title (optional, any kind); "" = auto
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
}

// Provider connection status for one agent, from GET /api/connections.
export interface ProviderConn {
  connected?: boolean;
  envs?: unknown[]; // opencode: configured provider API-key envs
}

// The full connections bag. Known agents are named; git providers (github /
// bitbucket) and any future entries ride the index signature.
export interface ConnectionsStatus {
  claude?: ProviderConn;
  codex?: ProviderConn;
  opencode?: ProviderConn;
  [provider: string]: ProviderConn | undefined;
}

// A registered SSM host bookmark, from GET /api/ssm/hosts.
export interface SsmHost {
  id: string;
  alias: string;
  accountId?: string;
}
