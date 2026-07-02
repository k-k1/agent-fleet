// Agent registry — the single source of truth for per-"agent kind" behavior.
//
// Historically the differences between claude / codex / opencode / shell / ssm
// were spread across ~15 `kind === "claude"` ternaries and switch chains
// (presentation, availability, launch params, capabilities). Adding a sixth kind
// meant editing every one. This registry collapses them into one descriptor per
// kind: presentation fields, an availability predicate, and a capability set the
// UI reads to decide which affordances to show. Adding an agent = adding one
// descriptor here — the rest of the app is capability-driven.
import type {
  ConnectionsStatus,
  SessionKind,
  SsmHost,
} from "../types/session.ts";
import { SESSION_KINDS } from "../types/session.ts";

// Capability flags. The UI shows an affordance iff the attached session's agent
// has the matching cap, so a new agent lights up the right controls by data alone.
export interface AgentCaps {
  chat: boolean; // shows the claude chat mirror (GET /messages, POST /input)
  transcript: boolean; // stopped session opens a read-only chat history
  model: boolean; // offers a model selector at launch
  fork: boolean; // supports fork-session from the chat
  contextBar: boolean; // shows the context-window token gauge
  ephemeral: boolean; // archiving deletes it (no keep) — shell / ssm
  runsInDir: boolean; // launches in a working dir (clone / dir source) — the agents
  launchableFromRepo: boolean; // offered in a repo row's 起動 menu (ssm is not)
  fixedAliveChip: boolean; // liveness shown as a fixed "起動中" chip (no state model) — shell
  namedByLabel: boolean; // display name comes from the session label (else repo@time) — non-shell
}

// A status chip for a session: codicon + label + color class (+ optional spinner).
export interface StateInfo {
  cls: string;
  icon: string;
  text: string;
  spin?: boolean;
}

// Context passed to a kind's availability predicate.
export interface AvailCtx {
  conns?: ConnectionsStatus | null;
  ssmHostCount?: number;
}

export interface AgentDescriptor {
  id: SessionKind;
  // presentation
  icon: string; // codicon name
  label: string; // display word
  short: string; // 2-char abbrev for tight headers (cc/cx/oc/sh/aw)
  cssClass: string; // .kind-<slug> color class slug
  // New Session dialog
  launchHint: string; // the seg-button sub-label
  // repo-launch session-name suffix (so a repo's shell/oc/cx sessions stay distinct)
  launchSuffix: string;
  caps: AgentCaps;
  // whether this kind is currently launchable given connections / SSM hosts
  available(ctx: AvailCtx): boolean;
}

// Build a caps object with sane defaults so each descriptor only lists what's true.
function caps(overrides: Partial<AgentCaps>): AgentCaps {
  return {
    chat: false,
    transcript: false,
    model: false,
    fork: false,
    contextBar: false,
    ephemeral: false,
    runsInDir: false,
    launchableFromRepo: false,
    fixedAliveChip: false,
    namedByLabel: true,
    ...overrides,
  };
}

export const AGENTS: Record<SessionKind, AgentDescriptor> = {
  claude: {
    id: "claude",
    icon: "sparkle",
    label: "claude",
    short: "cc",
    cssClass: "claude",
    launchHint: "Claude Code を起動",
    launchSuffix: "",
    caps: caps({
      chat: true,
      transcript: true,
      model: true,
      fork: true,
      contextBar: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    available: (c) => !!c.conns?.claude?.connected,
  },
  codex: {
    id: "codex",
    icon: "rocket",
    label: "codex",
    short: "cx",
    cssClass: "codex",
    launchHint: "Codex CLI を起動",
    launchSuffix: "-cx",
    caps: caps({ runsInDir: true, launchableFromRepo: true }),
    available: (c) => !!c.conns?.codex?.connected,
  },
  opencode: {
    id: "opencode",
    icon: "hubot",
    label: "opencode",
    short: "oc",
    cssClass: "opencode",
    launchHint: "opencode を起動",
    launchSuffix: "-oc",
    caps: caps({ runsInDir: true, launchableFromRepo: true }),
    // opencode is ready once it has at least one provider API key env (or the
    // connection reports connected). Unifies the two prior call-site checks.
    available: (c) =>
      (c.conns?.opencode?.envs?.length ?? 0) > 0 || !!c.conns?.opencode?.connected,
  },
  shell: {
    id: "shell",
    icon: "terminal",
    label: "shell",
    short: "sh",
    cssClass: "shell",
    launchHint: "通常のシェル (bash)",
    launchSuffix: "-sh",
    caps: caps({
      ephemeral: true,
      launchableFromRepo: true,
      fixedAliveChip: true,
      namedByLabel: false,
    }),
    available: () => true,
  },
  ssm: {
    id: "ssm",
    icon: "cloud",
    label: "ssm",
    short: "aw",
    cssClass: "ssm",
    launchHint: "AWS EC2 に SSM ログイン",
    launchSuffix: "",
    caps: caps({ ephemeral: true }),
    available: (c) => (c.ssmHostCount ?? 0) > 0,
  },
};

// agentOf resolves a kind to its descriptor, defaulting to claude for unknown
// kinds (mirrors the old ternary chains that fell back to claude).
export function agentOf(kind: string | null | undefined): AgentDescriptor {
  return (kind && AGENTS[kind as SessionKind]) || AGENTS.claude;
}

// availableKinds returns the set of kinds launchable in the given context. shell
// is always present; the rest gate on their availability predicate.
export function availableKinds(ctx: AvailCtx): Record<SessionKind, boolean> {
  const out = {} as Record<SessionKind, boolean>;
  for (const k of SESSION_KINDS) out[k] = AGENTS[k].available(ctx);
  return out;
}

// Kinds offered in a repo row's launch menu, in display order. Every entry must
// carry the launchableFromRepo cap (asserted below); the order is presentational.
export const repoLaunchKinds: SessionKind[] = ["claude", "opencode", "codex", "shell"];

// Kinds offered in the New Session dialog, in display order (shell is the default,
// left-most; the rest appear when available). Adding an agent here surfaces it.
export const newSessionKinds: SessionKind[] = ["shell", "claude", "opencode", "codex", "ssm"];

export type { SsmHost };
