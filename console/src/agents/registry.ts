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
import type { MsgKey } from "../lib/i18n/index.ts";

// Capability flags. The UI shows an affordance iff the attached session's agent
// has the matching cap, so a new agent lights up the right controls by data alone.
export interface AgentCaps {
  chat: boolean; // shows the chat mirror (GET /messages, POST /input)
  headlessChat: boolean; // can back an assistant-chat conversation via a headless CLI (docs/log/19)
  transcript: boolean; // stopped session opens a read-only chat history
  model: boolean; // offers a model selector at launch
  effort: boolean; // offers a reasoning-effort selector when the chosen driver supports it
  tuiEffort: boolean; // the TUI launch command can pin effort (managed uses Driver capabilities)
  tuiStartMode: boolean; // the TUI launch command can start deterministically in plan/normal
  contextBar: boolean; // shows the context-window token gauge
  imagePaste: boolean; // chat composer accepts pasted images (claude Read-tool flow)
  // composer offers the skill/command picker (GET /sessions/{name}/skills — docs/log/50).
  // Native listings: claude/codex/opencode scan filesystem conventions; cursor serves
  // the CLI-advertised ACP list. Every chat kind additionally gets "foreign" entries
  // (other conventions' SKILL.md, fired via prompt injection — docs/log/50 §8), which is
  // why kiro/copilot/agy are on too (foreign-only; no verified native mechanism).
  slashSkills: boolean;
  // …and NATIVE entries also show for MANAGED (paneless) sessions. Off for opencode:
  // its /commands are a TUI feature and firing them over the server API is unverified
  // (docs/log/50 §7) — foreign (injection) entries are exempt from this gate, being plain
  // prompts. cursor is verified (measured: ACP session/prompt "/cmd" fires);
  // codex "$skill" is a plain text mention, channel-agnostic by construction.
  slashSkillsManaged: boolean;
  planMode: boolean; // chat offers a plan-mode toggle (drives the TUI's mode-cycle key)
  // Launch offers the choice of skipping permission prompts (docs/log/76). What qualifies is not
  // that the flag can be dropped but that a pending approval can be answered from the Console;
  // turning prompts on for a kind that cannot answer them looks to the user like a silent hang.
  // Paired with Caps.PermissionChoice on the Go side, where the server refuses create by the
  // same rule (permission_choice_unsupported).
  permissionChoice: boolean;
  // the mirror offers a fork-from-here action on a past user turn — a new session carrying the
  // history up to just before it (docs/log/55). Needs the kind to send transcript anchors
  // (Turn.anchorId); the mirror also checks the turn itself (canBranchFrom).
  forkAt: boolean;
  // …and whether that needs a MANAGED session. True for the kinds whose only fork-point
  // API is the runtime's (opencode's serve fork, codex's thread/fork) — their CLI launch
  // command has no argument for it. False for claude, which has no managed driver at all
  // and cuts its own transcript instead: gating it on managed would hide the affordance
  // forever. The server enforces the same per-kind rule (agents.ErrForkAtRoute).
  forkAtManagedOnly: boolean;
  ephemeral: boolean; // archiving deletes it (no keep) — shell / ssm
  runsInDir: boolean; // launches in a working dir (clone / dir source) — the agents
  launchableFromRepo: boolean; // offered in a repo row's launch menu (ssm is not)
  fixedAliveChip: boolean; // liveness shown as a fixed "running" chip (no state model) — shell
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
  label: string; // display word — the compact one, used by every chip/header (kindLabel)
  // Full product name for the roomy launch pickers ("start work" / "start") only.
  // Optional: falls back to `label` (kindDisplayName), so only kinds whose full name
  // differs from the compact label set it — claude "Claude Code", copilot "GitHub
  // Copilot". The tight chips (LayoutMap, kt-full) always show `label`.
  displayName?: string;
  assistantName: string; // how the agent signs its chat turns ("Claude" / "Codex" / …)
  short: string; // 2-char abbrev for tight headers (cc/cx/oc/sh/aw)
  cssClass: string; // .kind-<slug> color class slug
  // New Session dialog
  launchHintKey: MsgKey; // the seg-button sub-label (i18n key; resolved at render via t())
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
  // plan (the real label follows from the next poll) and as the "normal" option in the
  // launch dialogs. claude "Bypass", codex "Default", opencode "Build". "" for agents
  // without a mode chip. For claude this names the state of a session launched with permission
  // prompts skipped, so never show it directly: pass it through nonPlanModeLabel() (docs/log/76).
  defaultModeLabel: string;
  // the head-of-input character that opens the skill picker while typing ("/" for
  // slash-command kinds, "$" for codex skill mentions). "" = the picker opens from
  // its button only (kinds whose entries are all injection prompts — no slash form).
  skillTrigger: string;
  // managed driver (docs/log/27 P2/P3): whether this kind supports creating paneless sessions
  // driven by the shared runtime. Kinds with it true show the driver choice in the launch UI and
  // default to managed (§9.2 — Terminal (CLI) is the user's explicit memory trade-off).
  managedDriver: boolean;
  // Approximate extra RSS of one Terminal (CLI) session over a managed one. The launch and
  // switch UI carry no per-kind branch and just show this measured value (docs/log/27 §12.2-9,
  // appendix B).
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
    effort: false,
    tuiEffort: false,
    tuiStartMode: false,
    contextBar: false,
    imagePaste: false,
    slashSkills: false,
    slashSkillsManaged: false,
    planMode: false,
    permissionChoice: false,
    forkAt: false,
    forkAtManagedOnly: true,
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
    label: "Claude",
    displayName: "Claude Code",
    assistantName: "Claude",
    short: "cc",
    cssClass: "claude",
    launchHintKey: "agent.launch_hint.claude",
    launchSuffix: "",
    planCycleKey: "BTab", // Shift+Tab cycles normal / auto-accept / plan (used to exit)
    planEnterCmd: "/plan", // claude has a direct command to enter plan mode
    defaultModeLabel: "Bypass",
    skillTrigger: "/",
    managedDriver: false,
    tuiMemoryCost: "",
    caps: caps({
      permissionChoice: true, // approvals are answerable: status-hook permission state + the mirror's permission card
      chat: true,
      headlessChat: true, // Phase A: claude -p backs assistant chat (docs/log/19)
      transcript: true,
      model: true,
      effort: true,
      tuiEffort: true, // claude --effort
      tuiStartMode: true, // --permission-mode plan / bypassPermissions
      contextBar: true,
      imagePaste: true,
      slashSkills: true,
      slashSkillsManaged: true,
      planMode: true,
      // Fork from a past turn (docs/log/55): claude alone has no usable official fork-point API
      // (`--resume-session-at` is print-mode only), so the Agent builds the fork by truncating
      // the transcript jsonl. The kind has no managed driver, hence managedOnly is false.
      forkAt: true,
      forkAtManagedOnly: false,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    available: (c) => !!c.conns?.claude?.connected,
  },
  codex: {
    id: "codex",
    icon: "rocket",
    label: "Codex",
    assistantName: "Codex",
    short: "cx",
    cssClass: "codex",
    launchHintKey: "agent.launch_hint.codex",
    launchSuffix: "-cx",
    planCycleKey: "BTab", // Shift+Tab cycles the collaboration mode (used to exit plan)
    planEnterCmd: "/plan", // codex also has /plan ("switch to Plan mode")
    defaultModeLabel: "Default",
    skillTrigger: "$",
    // Chat mirror lit up (stage 1): turns come from codex's rollout JSONL, normalized by the
    // Agent's transcript() and windowed by the generic /messages handler. The context
    // gauge works — codex logs token counts too. Plan mode + inline request_user_input
    // questions are supported. headlessChat via `codex exec --json` (assistant chat /
    // title suggestion backend); fork via `codex fork <id>` (server ForkSource).
    // forkAt: forking from a past turn (docs/log/55) is the app-server's `thread/fork` with
    // lastTurnId (inclusive). Offered only for managed, since the CLI route has no way to pass
    // a fork point.
    // model: launch-time only, live catalog (api/agents/codex/models = `codex debug
    // models` under codex's own subscription auth) → `codex -m`.
    // imagePaste: upload + path-in-prompt (claude's flow); codex's view_image fires on
    // a plain path mention — live-verified for both the TUI and `codex exec`.
    managedDriver: true,
    tuiMemoryCost: "230MiB",
    caps: caps({
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      effort: true,
      tuiEffort: true, // -c model_reasoning_effort=…
      contextBar: true,
      imagePaste: true,
      slashSkills: true, // $CODEX_HOME/skills + .codex/skills — "$name" mention (docs/log/50 §7)
      slashSkillsManaged: true, // a text mention works on any channel
      planMode: true,
      forkAt: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    available: (c) => !!c.conns?.codex?.connected,
  },
  cursor: {
    id: "cursor",
    icon: "inspect", // codicon pointer/cursor — closest nod to "Cursor"; no brand codicon exists
    label: "Cursor",
    assistantName: "Cursor",
    short: "cu",
    cssClass: "cursor",
    launchHintKey: "agent.launch_hint.cursor",
    launchSuffix: "-cu",
    planCycleKey: "", // the TUI's Shift+Tab cycles 3 modes (agent/plan/ask) — no key-driven toggle
    planEnterCmd: "",
    defaultModeLabel: "Agent",
    skillTrigger: "/",
    // Cursor CLI (docs/log/40, ADR 0023). Managed is the default: the driver runs a per-session
    // child of `cursor-agent acp` (ACP JSON-RPC over stdio, Track A2). The TUI writes the same
    // Claude Code-compatible JSONL transcript, so mirror and state work on both drivers.
    // model: launch-time only (`cursor-agent models` is an account-linked live catalog ->
    // `--model`). Effort is folded into the model id itself, so no separate effort cap; ACP has
    // no per-session model argument and no change while running, so DynamicModel is false.
    // No fork (cursor's /fork is TUI-only). No contextBar (v1 does not put per-turn tokens in
    // the mirror — docs/log/40 Track D). imagePaste is off: ACP advertises image:true but v1
    // does not wire it, and an unverified cap is not declared (same rule as copilot).
    // Auth is a dedicated login flow (~/.config/cursor/auth.json; API-key registration is Track D).
    managedDriver: true,
    // A per-session child either way, so choosing Terminal (CLI) does not change the number of
    // resident processes (one cursor process per session) — no extra cost shown, as for copilot.
    tuiMemoryCost: "",
    caps: caps({
      permissionChoice: true, // approvals are answerable: ACP session/request_permission -> Interaction
      chat: true,
      headlessChat: true, // `cursor-agent -p --mode ask` backs assistant chat, read-only (docs/log/40 Track D)
      transcript: true,
      model: true,
      tuiStartMode: true, // --plan (start in plan mode)
      slashSkills: true, // the ACP-advertised list (builtin skill + global + project) is authoritative — docs/log/50 §7
      slashSkillsManaged: true, // measured: ACP session/prompt "/cmd" fires
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden when the image lacks the CLI (supported === false) or cursor is not
    // signed in; an unfetched conns bag stays visible (same policy as agy/copilot).
    available: (c) => c.conns?.cursor?.supported !== false && !!c.conns?.cursor?.connected,
  },
  agy: {
    id: "agy",
    icon: "magnet",
    label: "Antigravity",
    assistantName: "Antigravity",
    short: "ag",
    cssClass: "agy",
    launchHintKey: "agent.launch_hint.agy",
    launchSuffix: "-ag",
    planCycleKey: "",
    planEnterCmd: "",
    defaultModeLabel: "",
    skillTrigger: "",
    // Antigravity CLI (docs/log/32, ADR 0008). v1.1.4 has no structured output, so
    // Terminal (CLI) is the only driver — no managed mode until agy grows an
    // event stream. The chat mirror works: the agent reads the per-conversation
    // brain/…/transcript_full.jsonl (written live) and normalizes it into
    // transcript() turns; input is pasted into the TUI like claude's mirror.
    // model: launch-time only, live catalog (api/agents/agy/models = `agy
    // models` display names) → `agy --model`; effort variants are baked into
    // the model names, so no separate effort cap. No fork, no plan hooks, no
    // context gauge (the transcript records no token counts).
    // imagePaste: upload + path-in-prompt (claude's and codex's way). agy reads an image from a
    // plain path mention alone — live-verified, OCR included for Japanese. Starter Quota =
    // experimental pool: the launch hint carries the experimental-quota tag, the WS bar
    // shows the quota gauge.
    // headlessChat: `agy -p` backs assistant-chat conversations (resume via
    // --conversation; plain-text output — no working steps, no context gauge).
    managedDriver: false,
    tuiMemoryCost: "",
    caps: caps({
      permissionChoice: true, // approvals are answerable through pending.go's permission state
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      imagePaste: true,
      slashSkills: true, // foreign (injection) entries only — docs/log/50 §8
      slashSkillsManaged: true, // injection is a plain prompt (agy has no managed mode; stated anyway)
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden on hosts that cannot run agy (supported === false — the RDRAND guard,
    // docs/log/32 Track B); an unfetched conns bag falls back to visible
    // (the rail treats null conns as show-all before the predicate runs).
    available: (c) => c.conns?.agy?.supported !== false && !!c.conns?.agy?.connected,
  },
  copilot: {
    id: "copilot",
    icon: "copilot",
    label: "Copilot",
    displayName: "GitHub Copilot",
    assistantName: "Copilot",
    short: "cp",
    cssClass: "copilot",
    launchHintKey: "agent.launch_hint.copilot",
    launchSuffix: "-cp",
    planCycleKey: "", // the TUI's Shift+Tab cycles 3 modes (including autopilot) — no key-driven toggle
    planEnterCmd: "",
    defaultModeLabel: "Default",
    skillTrigger: "",
    // GitHub Copilot CLI (docs/log/36). Managed is the default: the driver runs a per-session
    // child of `copilot --acp` (ACP JSON-RPC over stdio). The TUI writes the same events.jsonl,
    // so mirror and state share one implementation across both drivers.
    // model/effort: launch-time only (a plan-aware live catalog scraped from the TUI's /model
    // over the PTY -> `--model` / `--effort`; Free has Auto only, so only the default appears).
    // ACP has no dynamic change — while running, only the mode in the managed settings modal.
    // No fork (the CLI has no fork entry point). No contextBar (events.jsonl carries outTok only
    // and no context size). imagePaste is off in v1 because it is unmeasured: an unverified cap
    // is not declared. Auth rides on the GitHub connection and assumes a Copilot subscription.
    managedDriver: true,
    // A per-session child either way, so choosing Terminal (CLI) does not change the number of
    // resident processes (one copilot process per session) — no extra cost shown.
    tuiMemoryCost: "",
    caps: caps({
      permissionChoice: true, // approvals are answerable: ACP session/request_permission -> Interaction
      chat: true,
      transcript: true,
      model: true,
      effort: true,
      tuiEffort: true, // --effort
      tuiStartMode: true, // --mode plan
      slashSkills: true, // foreign (injection) entries only — docs/log/50 §8
      slashSkillsManaged: true,
      // Fork from a past turn (docs/log/55): copilot has no official fork entry point either, so
      // the session-state directory is copied and events.jsonl truncated (measured: events.jsonl
      // is what restore reads). The Agent prepares this on both the TUI and the managed route,
      // so managedOnly is false.
      forkAt: true,
      forkAtManagedOnly: false,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden when the image lacks the CLI (supported === false) or GitHub is not
    // connected; an unfetched conns bag stays visible (same policy as agy).
    available: (c) => c.conns?.copilot?.supported !== false && !!c.conns?.copilot?.connected,
  },
  kiro: {
    id: "kiro",
    icon: "compass", // codicon — neutral shape nodding to Kiro's spec/guide bent (kiro_guide mode); no clash with the other 8
    label: "Kiro",
    assistantName: "Kiro",
    short: "ki",
    cssClass: "kiro",
    launchHintKey: "agent.launch_hint.kiro",
    launchSuffix: "-ki",
    planCycleKey: "", // the TUI cycles 3 modes (kiro_default/planner/guide) — no key-driven toggle, as for cursor
    planEnterCmd: "",
    defaultModeLabel: "Agent",
    skillTrigger: "",
    // Kiro CLI (kiro-cli, formerly the Amazon Q Developer CLI; docs/log/43, ADR 0026 planned).
    // Both Terminal (CLI) and Managed are supported (Track A2): managed is a per-session ACP
    // child (`kiro-cli acp`, like cursor and copilot), with cross-process resume through
    // session/load and context retention measured; the launch UI offers the driver choice.
    // chat/transcript: the authoritative read source is the v2 JSONL
    // (~/.kiro/sessions/cli/<sid>.jsonl), written append-only by the new TUI and by ACP, and
    // carrying toolUse inputs and toolResult outputs — so tool output, impossible for cursor,
    // renders here. State detection uses the TUI's explicit text contract (state.go: "Kiro is
    // working" / "requires approval" / "ask a question or describe a task"), because 2.14.1 has
    // no Stop hook (measured).
    // model: launch-time only (`kiro-cli chat --list-models -f json` is an account-linked live
    // catalog -> `--model`; a named model is selectable on Free too, default auto). ACP has
    // set_model, but Track A2 ruled out changing it while running, so DynamicModel stays false.
    // effort: kiro does have a separate `--effort` flag and program.go passes it, but the model
    // catalog carries no effort metadata and per-model support is unverified, so v1 shows no
    // picker — an unverified cap is not declared, as for copilot and cursor.
    // contextBar: on (Track D). The v2 JSONL transcript has no token counts, so the live
    // contextUsagePercentage carried in managed ACP's _kiro.dev/metadata is converted to
    // approximate tokens against the real window and wired to the mirror's ContextBar through
    // ContextReporter(ContextFill) — the same session-level fallback path as agy. Shown only
    // while managed and running; hidden for the TUI and when stopped.
    // planMode: off (a 3-mode cycle is not a clean binary — same as cursor). imagePaste is off:
    // ACP advertises image:true but v1 does not wire it. headlessChat: off (decision §4-3, kiro
    // is not added to ASSISTANT_AGENT_KINDS — title suggestion works through the generic read
    // layer). Auth is a device-flow login (Builder ID / free; API keys are Track D).
    managedDriver: true,
    // A per-session child either way, so choosing Terminal (CLI) does not change the number of
    // resident processes (one kiro process per session) — no extra cost shown, as for
    // cursor and copilot.
    tuiMemoryCost: "",
    caps: caps({
      permissionChoice: true, // approvals are answerable: ACP request_permission / the TUI's requires-approval
      chat: true,
      transcript: true,
      model: true,
      contextBar: true, // Track D: live context% from managed ACP's _kiro.dev/metadata, via ContextFill
      tuiStartMode: true, // program.go drops --trust-all-tools when mode=plan (state.go picks up the pending approval as "question")
      slashSkills: true, // foreign (injection) entries only — docs/log/50 §8
      slashSkillsManaged: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden when the CLI is not installed yet (supported === false, before on-demand install) or
    // kiro is not signed in; an unfetched conns bag stays visible (same policy as cursor/copilot).
    available: (c) => c.conns?.kiro?.supported !== false && !!c.conns?.kiro?.connected,
  },
  opencode: {
    id: "opencode",
    icon: "hubot",
    label: "OpenCode",
    assistantName: "OpenCode",
    short: "oc",
    cssClass: "opencode",
    launchHintKey: "agent.launch_hint.opencode",
    launchSuffix: "-oc",
    planCycleKey: "Tab", // Tab cycles the agent (build / plan)
    planEnterCmd: "",
    defaultModeLabel: "Build",
    skillTrigger: "/",
    // Chat mirror lit up (stage 2): turns come from opencode's SQLite store (message+part),
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
    tuiMemoryCost: "300MiB",
    caps: caps({
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      effort: true,
      tuiStartMode: true, // --agent plan|build
      contextBar: true,
      imagePaste: true,
      slashSkills: true, // .opencode/command(s) + ~/.config/opencode/command — docs/log/50 §7
      // slashSkillsManaged stays false: /command is a TUI feature and firing it over the server
      // API is unverified, and an unverified cap is not declared.
      planMode: true,
      // Fork from a past turn (docs/log/55): serve's `POST /session/{id}/fork` takes a messageID
      // and cuts the copied history just before that message (measured on 1.18.14).
      forkAt: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // opencode is ready with any of the three billing routes: a stored provider key,
    // an account connection, or the free tier — the zero-auth free tier really does answer
    // without credentials (measured), so a workspace that chose it must be able to
    // launch. supported === false (binary missing / old image) still hides the kind,
    // the same guard cursor and agy use. usage === "off" is checked FIRST and vetoes
    // the rest unconditionally — it is the explicit, tamper-resistant disable (Agent's
    // opencode.Connected(), auth.go): even a stored key or a live OAuth login must not
    // re-admit opencode while off is selected, the same override the Agent applies to
    // headlessAgentAvailable for assistant chat.
    available: (c) =>
      c.conns?.opencode?.supported !== false &&
      c.conns?.opencode?.usage !== "off" &&
      ((c.conns?.opencode?.envs?.length ?? 0) > 0 ||
        !!c.conns?.opencode?.connected ||
        c.conns?.opencode?.usage === "free"),
  },
  shell: {
    id: "shell",
    icon: "terminal",
    label: "Shell",
    assistantName: "Shell",
    short: "sh",
    cssClass: "shell",
    launchHintKey: "agent.launch_hint.shell",
    launchSuffix: "-sh",
    planCycleKey: "",
    planEnterCmd: "",
    defaultModeLabel: "",
    skillTrigger: "",
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
    label: "SSM",
    assistantName: "SSM",
    short: "aw",
    cssClass: "ssm",
    launchHintKey: "agent.launch_hint.ssm",
    launchSuffix: "",
    planCycleKey: "",
    planEnterCmd: "",
    defaultModeLabel: "",
    skillTrigger: "",
    // Like shell: a plain login shell with no working/idle model, so its liveness is a
    // fixed "running" chip (never "waiting for input") and it raises no answer/question
    // notifications.
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

// nonPlanModeLabel is the mode label for the non-plan side (docs/log/76).
//
// Claude's default label "Bypass" names the state of a session launched with permission prompts
// skipped. A session launched with approvals on starts in claude's own default mode, manual
// (measured on 2.1.241: the status line reads "⏸ manual mode on"), so showing the label
// unconditionally puts "permission prompts: ask every time" next to "start mode: Bypass" in the
// launch dialog. The mirror's chip reads the terminal and prints "Manual", so use that word.
//
// Other kinds' default labels (cursor/kiro "Agent", copilot/codex "Default", opencode "Build")
// say nothing about permissions and are left alone. The test is the value "Bypass" because that
// is the only kind whose label names a permission state; splitting it out into a cap would add
// one flag and the correspondence would come back to this same line.
export function nonPlanModeLabel(kind: string | null | undefined, skipPermissions: boolean): string {
  const a = agentOf(kind);
  if (a.caps.permissionChoice && !skipPermissions && a.defaultModeLabel === "Bypass") return "Manual";
  return a.defaultModeLabel;
}

// availableKinds returns the set of kinds launchable in the given context. shell
// is always present; the rest gate on their availability predicate.
export function availableKinds(ctx: AvailCtx): Record<SessionKind, boolean> {
  const out = {} as Record<SessionKind, boolean>;
  for (const k of SESSION_KINDS) out[k] = AGENTS[k].available(ctx);
  return out;
}

// Kinds offered in a repo row's launch menu, in display order. Every entry must
// carry the launchableFromRepo cap (asserted in availability.test.ts); the order
// is presentational.
export const repoLaunchKinds: SessionKind[] = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "shell"];

export type { SsmHost };
