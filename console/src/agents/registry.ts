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
  headlessChat: boolean; // can back an assistant-chat conversation via a headless CLI (docs/19)
  transcript: boolean; // stopped session opens a read-only chat history
  model: boolean; // offers a model selector at launch
  effort: boolean; // offers a reasoning-effort selector when the chosen driver supports it
  tuiEffort: boolean; // the TUI launch command can pin effort (managed uses Driver capabilities)
  tuiStartMode: boolean; // the TUI launch command can start deterministically in plan/normal
  contextBar: boolean; // shows the context-window token gauge
  imagePaste: boolean; // chat composer accepts pasted images (claude Read-tool flow)
  // composer offers the skill/command picker (GET /sessions/{name}/skills — docs/50).
  // Native listings: claude/codex/opencode scan filesystem conventions; cursor serves
  // the CLI-advertised ACP list. Every chat kind additionally gets "foreign" entries
  // (other conventions' SKILL.md, fired via prompt injection — docs/50 §8), which is
  // why kiro/copilot/agy are on too (foreign-only; no verified native mechanism).
  slashSkills: boolean;
  // …and NATIVE entries also show for MANAGED (paneless) sessions. Off for opencode:
  // its /commands are a TUI feature and firing them over the server API is unverified
  // (docs/50 §7) — foreign (injection) entries are exempt from this gate, being plain
  // prompts. cursor is verified (ACP session/prompt "/cmd" fired — 実測 2026-07-28);
  // codex "$skill" is a plain text mention, channel-agnostic by construction.
  slashSkillsManaged: boolean;
  planMode: boolean; // chat offers a plan-mode toggle (drives the TUI's mode-cycle key)
  // the mirror offers 「ここから分岐」 on a past user turn — a new session carrying the
  // history up to just before it (docs/55). Needs the kind to send transcript anchors
  // (Turn.anchorId) AND a managed session, since only the runtime APIs take a fork point;
  // the mirror checks both, so this cap alone never shows the affordance.
  forkAt: boolean;
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
  label: string; // display word — the compact one, used by every chip/header (kindLabel)
  // Full product name for the roomy launch pickers (作業を始める / はじめる) only.
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
  // plan (the real label follows from the next poll). claude "Bypass", codex "Default",
  // opencode "Build". "" for agents without a mode chip.
  defaultModeLabel: string;
  // the head-of-input character that opens the skill picker while typing ("/" for
  // slash-command kinds, "$" for codex skill mentions). "" = the picker opens from
  // its button only (kinds whose entries are all injection prompts — no slash form).
  skillTrigger: string;
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
    effort: false,
    tuiEffort: false,
    tuiStartMode: false,
    contextBar: false,
    imagePaste: false,
    slashSkills: false,
    slashSkillsManaged: false,
    planMode: false,
    forkAt: false,
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
      chat: true,
      headlessChat: true, // Phase A: claude -p backs assistant chat (docs/19)
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
    // Chat mirror lit up (段1): turns come from codex's rollout JSONL, normalized by the
    // Agent's transcript() and windowed by the generic /messages handler. The context
    // gauge works — codex logs token counts too. Plan mode + inline request_user_input
    // questions are supported. headlessChat via `codex exec --json` (assistant chat /
    // title suggestion backend); fork via `codex fork <id>` (server ForkSource).
    // forkAt: 発言時点からの分岐（docs/55）は app-server の `thread/fork` の lastTurnId
    // （inclusive）。managed のときだけ導線が出る（CLI ルートに分岐点を渡す口が無い）。
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
      slashSkills: true, // $CODEX_HOME/skills + .codex/skills — "$name" mention (docs/50 §7)
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
    planCycleKey: "", // TUI の Shift+Tab は 3 モード循環（agent/plan/ask を跨ぐ）— キー駆動トグルは出さない
    planEnterCmd: "",
    defaultModeLabel: "Agent",
    skillTrigger: "/",
    // Cursor CLI (docs/40, ADR 0023). Managed が既定: per-session child の
    // `cursor-agent acp`（ACP JSON-RPC over stdio）を driver が駆動する（Track A2）。
    // TUI も同じ Claude Code 互換 JSONL 転写を書くのでミラー/状態は両ドライバで成立。
    // model: launch-time only（`cursor-agent models` のアカウント連動ライブカタログ →
    // `--model`。effort はモデル id 自体に畳まれている＝別 effort cap は立てない。
    // ACP に per-session モデル指定口が無く稼働中変更も不可＝DynamicModel:false）。
    // fork なし（cursor の /fork は TUI 限定）。contextBar なし（v1 は per-turn トークンを
    // ミラーに載せない — docs/40 Track D）。imagePaste は ACP に image:true があるが v1
    // 未配線のためオフ（copilot と同じ「未検証の caps を立てない」1854d 教訓）。
    // 認証は専用ログインフロー（~/.config/cursor/auth.json・API キー登録は Track D）。
    managedDriver: true,
    // per-session child なので CLI(TUI) を選んでも常駐プロセス数は変わらない
    // （どちらも cursor 1 プロセス/セッション）— 追加コスト表示なし（copilot 同型）。
    tuiMemoryCost: "",
    caps: caps({
      chat: true,
      headlessChat: true, // `cursor-agent -p --mode ask` backs assistant chat, read-only (docs/40 Track D)
      transcript: true,
      model: true,
      tuiStartMode: true, // --plan（plan で起動）
      slashSkills: true, // ACP 広告リスト（builtin skill+global+project）が正 — docs/50 §7
      slashSkillsManaged: true, // ACP session/prompt "/cmd" の発火を実測（2026-07-28）
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
    // Antigravity CLI (docs/32, ADR 0008). v1.1.4 has no structured output, so
    // Terminal (CLI) is the only driver — no managed mode until agy grows an
    // event stream. The chat mirror works: the agent reads the per-conversation
    // brain/…/transcript_full.jsonl (written live) and normalizes it into
    // transcript() turns; input is pasted into the TUI like claude's mirror.
    // model: launch-time only, live catalog (api/agents/agy/models = `agy
    // models` display names) → `agy --model`; effort variants are baked into
    // the model names, so no separate effort cap. No fork, no plan hooks, no
    // context gauge (the transcript records no token counts).
    // imagePaste: upload + path-in-prompt (claude/codex の流儀)。agy は素のパス
    // 記述だけで画像を読む — 実機検証済み（日本語込みで OCR できた）。Starter Quota =
    // experimental pool: the launch hint carries the 実験枠 tag, the WS bar
    // shows the quota gauge.
    // headlessChat: `agy -p` backs assistant-chat conversations (resume via
    // --conversation; plain-text output — no working steps, no context gauge).
    managedDriver: false,
    tuiMemoryCost: "",
    caps: caps({
      chat: true,
      headlessChat: true,
      transcript: true,
      model: true,
      imagePaste: true,
      slashSkills: true, // foreign（注入）エントリのみ — docs/50 §8
      slashSkillsManaged: true, // 注入はただのプロンプト（agy に managed は無いが明示）
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden on hosts that cannot run agy (supported === false — docs/32
    // Track B RDRAND ガード); an unfetched conns bag falls back to visible
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
    planCycleKey: "", // TUI の Shift+Tab は 3 モード循環（autopilot を跨ぐ）— キー駆動トグルは出さない
    planEnterCmd: "",
    defaultModeLabel: "Default",
    skillTrigger: "",
    // GitHub Copilot CLI (docs/36). Managed が既定: per-session child の
    // `copilot --acp`（ACP JSON-RPC over stdio）を driver が駆動する。TUI も同じ
    // events.jsonl を書くのでミラー/状態は両ドライバで同一実装。
    // model/effort: launch-time only（TUI /model の PTY スクレイプによる
    // プラン反映ライブカタログ → `--model` / `--effort`。Free は Auto のみ＝
    // 既定だけが出る。ACP に動的変更が無い — 稼働中変更は managed 設定
    // モーダルの mode のみ）。
    // fork なし（CLI に fork 口が無い）。contextBar なし（events.jsonl は outTok
    // のみで文脈量が出ない）。imagePaste は未実測のため v1 オフ（1854d の逆 —
    // 未検証の caps を立てない）。認証は GitHub 連携相乗り＋Copilot サブスク前提。
    managedDriver: true,
    // per-session child なので CLI(TUI) を選んでも常駐プロセス数は変わらない
    // （どちらも copilot 1 プロセス/セッション）— 追加コスト表示なし。
    tuiMemoryCost: "",
    caps: caps({
      chat: true,
      transcript: true,
      model: true,
      effort: true,
      tuiEffort: true, // --effort
      tuiStartMode: true, // --mode plan
      slashSkills: true, // foreign（注入）エントリのみ — docs/50 §8
      slashSkillsManaged: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden when the image lacks the CLI (supported === false) or GitHub is not
    // connected; an unfetched conns bag stays visible (same policy as agy).
    available: (c) => c.conns?.copilot?.supported !== false && !!c.conns?.copilot?.connected,
  },
  kiro: {
    id: "kiro",
    icon: "compass", // codicon — Kiro の spec/guide 志向（kiro_guide モード）に寄せた中立形。既存8種と非衝突
    label: "Kiro",
    assistantName: "Kiro",
    short: "ki",
    cssClass: "kiro",
    launchHintKey: "agent.launch_hint.kiro",
    launchSuffix: "-ki",
    planCycleKey: "", // TUI は 3 モード循環（kiro_default/planner/guide を跨ぐ）— キー駆動トグルは出さない（cursor 同型）
    planEnterCmd: "",
    defaultModeLabel: "Agent",
    skillTrigger: "",
    // Kiro CLI（kiro-cli・旧 Amazon Q Developer CLI。docs/43, ADR 0026 予定）。
    // Terminal(TUI) ＋ Managed 両対応（Track A2）: managed は per-session child の
    // ACP（`kiro-cli acp`・cursor/copilot 同型）で、session/load のクロスプロセス resume＋
    // 文脈保持を実測（起動 UI に Terminal/Managed のドライバ選択を出す）。
    // chat/transcript: read 正本は v2 JSONL（~/.kiro/sessions/cli/<sid>.jsonl）——新 TUI ＋
    // ACP が
    // append-only で書き、toolUse 入力＋toolResult 出力まで載る（cursor で不可だった tool
    // 出力まで描ける）。状態検出は TUI 明示テキスト契約（state.go: "Kiro is working" /
    // "requires approval" / "ask a question or describe a task"）— 2.14.1 に Stop hook が
    // 無いため（実測）。
    // model: launch-time only（`kiro-cli chat --list-models -f json` のアカウント連動ライブ
    // カタログ → `--model`。Free でも named 指定可＝既定 auto。ACP set_model はあるが
    // 稼働中変更は Track A2 判定＝DynamicModel は立てない）。
    // effort: kiro には別フラグ `--effort` があり program.go は渡すが、モデルカタログに
    // effort メタデータが無く per-model の対応も未検証のため v1 は picker を出さない
    // （copilot/cursor の「未検証の caps を立てない」1854d 教訓）。
    // contextBar あり（Track D）: v2 JSONL 転写にトークン数は無いが、managed ACP の
    // _kiro.dev/metadata が運ぶライブ contextUsagePercentage を実 window に対する概算トークンへ
    // 変換して ContextReporter(ContextFill) 経由でミラーの ContextBar に配線（agy と同じ
    // セッションレベル fallback 経路）。稼働中 managed のみ表示・TUI/停止中は非表示。planMode なし
    // （3 モード循環でクリーンな二値でない — cursor 同型）。imagePaste は ACP に image:true が
    // あるが v1 未配線でオフ。headlessChat なし（§4-3 決定: ASSISTANT_AGENT_KINDS に kiro を
    // 加えない — タイトル提案は generic read 層で動く）。
    // 認証は device-flow ログイン（Builder ID/free・API キーは Track D）。
    managedDriver: true,
    // per-session child なので CLI(TUI) を選んでも常駐プロセス数は変わらない（どちらも kiro
    // 1 プロセス/セッション）— 追加コスト表示なし（cursor/copilot 同型）。
    tuiMemoryCost: "",
    caps: caps({
      chat: true,
      transcript: true,
      model: true,
      contextBar: true, // Track D: managed ACP の _kiro.dev/metadata ライブ context% → ContextFill 経由
      tuiStartMode: true, // program.go は mode=plan で --trust-all-tools を外す（承認待ちは state.go が "question" で拾う）
      slashSkills: true, // foreign（注入）エントリのみ — docs/50 §8
      slashSkillsManaged: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // Hidden when the CLI is not installed yet (supported === false・オンデマンド導入前) or
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
      slashSkills: true, // .opencode/command(s) + ~/.config/opencode/command — docs/50 §7
      // slashSkillsManaged stays false: /command は TUI 機能で、server API 経由の
      // 発火は未検証（未検証の caps を立てない — 1854d の教訓）。
      planMode: true,
      // 発言時点からの分岐（docs/55）: serve の `POST /session/{id}/fork` が messageID を
      // 取り、指定メッセージの手前で履歴のコピーを打ち切る（実測 1.18.14）。
      forkAt: true,
      runsInDir: true,
      launchableFromRepo: true,
    }),
    // opencode is ready with any of the three billing routes: a stored provider key,
    // an account connection, or 無料枠 — the zero-auth free tier really does answer
    // without credentials (measured), so a workspace that chose it must be able to
    // launch. supported === false (binary missing / old image) still hides the kind,
    // the same guard cursor and agy use.
    available: (c) =>
      c.conns?.opencode?.supported !== false &&
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
// carry the launchableFromRepo cap (asserted in availability.test.ts); the order
// is presentational.
export const repoLaunchKinds: SessionKind[] = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "shell"];

export type { SsmHost };
