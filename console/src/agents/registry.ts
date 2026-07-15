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
  chat: boolean; // shows the chat mirror (GET /messages, POST /input)
  headlessChat: boolean; // can back an assistant-chat conversation via a headless CLI (docs/19)
  transcript: boolean; // stopped session opens a read-only chat history
  model: boolean; // offers a model selector at launch
  fork: boolean; // supports fork-session from the chat (claude --fork-session)
  contextBar: boolean; // shows the context-window token gauge
  imagePaste: boolean; // chat composer accepts pasted images (claude Read-tool flow)
  planMode: boolean; // chat offers a plan-mode toggle (drives the TUI's mode-cycle key)
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
  assistantName: string; // how the agent signs its chat turns ("Claude" / "Codex" / …)
  short: string; // 2-char abbrev for tight headers (cc/cx/oc/sh/aw)
  cssClass: string; // .kind-<slug> color class slug
  // New Session dialog
  launchHint: string; // the seg-button sub-label
  // repo-launch session-name suffix (so a repo's shell/oc/cx sessions stay distinct)
  launchSuffix: string;
  // the TUI key that cycles permission/collaboration mode (Shift+Tab for claude/codex,
  // Tab for opencode's agent cycle), sent by the chat's plan-mode toggle. "" when none.
  planCycleKey: string;
  // a slash command that deterministically ENTERS plan mode (claude "/plan"), sent as a
  // prompt when turning plan on — more reliable than cycling a 3-mode TUI. "" = use the
  // cycle key. Exiting plan still uses planCycleKey.
  planEnterCmd: string;
  // the agent's non-plan mode name, shown optimistically in the mode chip when leaving
  // plan (the real label follows from the next poll). claude "Bypass", codex "Default",
  // opencode "Build". "" for agents without a mode chip.
  defaultModeLabel: string;
  // managed driver（docs/27 P2/P3）: この kind が共有 runtime 駆動（paneless）のセッション
  // 作成に対応しているか。true の kind は起動 UI にドライバ選択が出て、既定が managed
  // になる（§9.2 — CLI(TUI) はユーザーの明示的なメモリトレードオフ）。
  managedDriver: boolean;
  // managed と比べた CLI(TUI) 1 セッション分のおおよその追加 RSS。起動・切替 UI は
  // kind 分岐を持たず、この実測表示を使う（docs/27 §12.2-9 / 付録B）。
  tuiMemoryCost: string;
  caps: AgentCaps;
  // whether this kind is currently launchable given connections / SSM hosts
  available(ctx: AvailCtx): boolean;
}

// Build a caps object with sane defaults so each descriptor only lists what's true.
function caps(overrides: Partial<AgentCaps>): AgentCaps {
  return {
    chat: false,
    headlessChat: false,
    transcript: false,
    model: false,
    fork: false,
    contextBar: false,
    imagePaste: false,
    planMode: false,
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
    assistantName: "Claude",
    short: "cc",
    cssClass: "claude",
    launchHint: "Claude Code を起動",
    launchSuffix: "",
    planCycleKey: "BTab", // Shift+Tab cycles normal / auto-accept / plan (used to exit)
    planEnterCmd: "/plan", // claude has a direct command to enter plan mode
    defaultModeLabel: "Bypass",
    managedDriver: false,
    tuiMemoryCost: "",
    caps: caps({
      chat: true,
      headlessChat: true, // Phase A: claude -p backs assistant chat (docs/19)
      transcript: true,
      model: true,
      fork: true,
      contextBar: true,
      imagePaste: true,
      planMode: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    available: (c) => !!c.conns?.claude?.connected,
  },
  codex: {
    id: "codex",
    icon: "rocket",
    label: "codex",
    assistantName: "Codex",
    short: "cx",
    cssClass: "codex",
    launchHint: "Codex CLI を起動",
    launchSuffix: "-cx",
    planCycleKey: "BTab", // Shift+Tab cycles the collaboration mode (used to exit plan)
    planEnterCmd: "/plan", // codex also has /plan ("switch to Plan mode")
    defaultModeLabel: "Default",
    // Chat mirror lit up (段1): turns come from codex's rollout JSONL, normalized by the
    // Agent's transcript() and windowed by the generic /messages handler. The context
    // gauge works — codex logs token counts too. Plan mode + inline request_user_input
    // questions are supported. headlessChat via `codex exec --json` (assistant chat /
    // title suggestion backend); fork via `codex fork <id>` (server ForkSource).
    // model: launch-time only, live catalog (api/agents/codex/models = `codex debug
    // models` under codex's own subscription auth) → `codex -m`.
    // imagePaste: upload + path-in-prompt (claude's flow); codex's view_image fires on
    // a plain path mention — live-verified for both the TUI and `codex exec`.
    managedDriver: true,
    tuiMemoryCost: "約230MiB",
    caps: caps({
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      fork: true,
      contextBar: true,
      imagePaste: true,
      planMode: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    available: (c) => !!c.conns?.codex?.connected,
  },
  opencode: {
    id: "opencode",
    icon: "hubot",
    label: "opencode",
    assistantName: "opencode",
    short: "oc",
    cssClass: "opencode",
    launchHint: "opencode を起動",
    launchSuffix: "-oc",
    planCycleKey: "Tab", // Tab cycles the agent (build / plan)
    planEnterCmd: "",
    defaultModeLabel: "Build",
    // Chat mirror lit up (段2): turns come from opencode's SQLite store (message+part),
    // normalized by the Agent's transcript() and windowed by the generic /messages
    // handler. Context gauge works (per-message tokens); plan mode + inline question tool.
    // headlessChat via `opencode run --format json` (assistant chat / title backend);
    // fork via `opencode --session <id> --fork` (server ForkSource).
    // model: launch-time only, live catalog (api/agents/opencode/models — reflects the
    // user's connected providers) → `opencode --model provider/model`.
    // imagePaste: upload + path-in-prompt. Vision is model-dependent: a vision model
    // reads it directly; big-pickle (free tier) either inspects it agentically (TUI,
    // live-verified) or declines honestly — never a silent failure.
    managedDriver: true,
    tuiMemoryCost: "約300MiB",
    caps: caps({
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      fork: true,
      contextBar: true,
      imagePaste: true,
      planMode: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // opencode is ready once it has at least one provider API key env (or the
    // connection reports connected). Unifies the two prior call-site checks.
    available: (c) =>
      (c.conns?.opencode?.envs?.length ?? 0) > 0 || !!c.conns?.opencode?.connected,
  },
  shell: {
    id: "shell",
    icon: "terminal",
    label: "shell",
    assistantName: "shell",
    short: "sh",
    cssClass: "shell",
    launchHint: "通常のシェル (bash)",
    launchSuffix: "-sh",
    planCycleKey: "",
    planEnterCmd: "",
    defaultModeLabel: "",
    managedDriver: false,
    tuiMemoryCost: "",
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
    assistantName: "ssm",
    short: "aw",
    cssClass: "ssm",
    launchHint: "AWS EC2 に SSM ログイン",
    launchSuffix: "",
    planCycleKey: "",
    planEnterCmd: "",
    defaultModeLabel: "",
    // Like shell: a plain login shell with no working/idle model, so its liveness is a
    // fixed 起動中 chip (not 入力待ち) and it raises no answer/question notifications.
    managedDriver: false,
    tuiMemoryCost: "",
    caps: caps({ ephemeral: true, fixedAliveChip: true }),
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

export type { SsmHost };
