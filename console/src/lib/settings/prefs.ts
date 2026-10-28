// One descriptor per Console preference (ADR 0124 Phase 2): its key, default, scope and, where
// the stored value needs one, the normaliser. The Settings type, DEFAULTS, the load-time
// normalisation and the sync key sets (device-local / agent-defaulted / accumulated) are all
// derived from PREFS below, so adding a preference is one entry here.
//
// The order of PREFS is the order of DEFAULTS and so of every object written to localStorage
// and PUT to ui-prefs; settingsEquivalence.dom.test.tsx pins it.
import type { WorkingSet } from "../workingSets.ts";
import type { LaunchTemplateStore } from "../launchTemplates.ts";
import type { PromptHistoryStore } from "../promptHistory.ts";
import { ASSISTANT_AGENT_KINDS, ASSISTANT_RECOMMENDED_MODEL, CJK_FONT_AUTO, DEFAULT_MODEL } from "./options.ts";
import { IMAGE_PROVIDERS } from "./imageProviders.ts";
import {
  DEFAULT_AGENT_LAUNCH,
  migrateOpencodeCatalog,
  normalizeAgentMemory,
  normalizeClaudeCustomModels,
  normalizeExternalMemory,
  normalizeMemoryReview,
  type AgentLaunchDefaults,
  type ExternalMemoryMode,
  type MemoryReviewMode,
  type StreamRepliesMode,
  type TtsCharConf,
} from "./normalize.ts";

export type PrefScope = "synced" | "device-local" | "agent-defaulted" | "accumulated";

export interface Pref<T> {
  readonly default: T;
  readonly scope: PrefScope;
  /** The Agent's reading of a stored value. Applied on load to the stored copy's value (absent
   *  included) and, after every hydrate merge, to the merged value, so it has to be idempotent. */
  readonly normalize?: (v: unknown) => T;
  /** A legacy-value migration applied to each incoming copy that holds the key (the stored one on
   *  load, the server's on hydrate) before anything compares it. Unlike normalize it never runs
   *  over the merged value: a local value is not re-read, so an unsaved local choice stays as it
   *  is instead of being compared in its migrated form. */
  readonly migrate?: (v: unknown) => T;
}

type PrefOpts<T> = Pick<Pref<T>, "normalize" | "migrate">;

// An ordinary preference: stored on this device and in the user's server copy, the server's
// value winning on hydrate.
function synced<T>(d: T, opts: PrefOpts<T> = {}): Pref<T> {
  return { default: d, scope: "synced", ...opts };
}

// Device-local — kept only in localStorage, never sent to nor restored from the server.
// "Does this device make sound" and "how does this screen look" depend on where it is used
// (office or home, brightness, headphones), so making them follow the user onto every device
// gets in the way. Preferences such as voice, speed, dictionary and fonts stay cross-device.
// For these keys it also removes the path where server-wins plus a dropped debounced save
// brings an OFF switch back to life.
function deviceLocal<T>(d: T, opts: PrefOpts<T> = {}): Pref<T> {
  return { default: d, scope: "device-local", ...opts };
}

// Agent-defaulted — keys the Agent itself reads from ui-prefs with a default of its own when the
// key is missing, the default here being that same default. A missing key is therefore not
// "never saved" but "the Agent's default is in force", and hydrate keeps the two sides from
// disagreeing about it: without that, a browser that had one on for one account would show it on
// for another whose server copy lacks the key, and the next save of any setting would switch it
// on there.
function agentDefaulted<T>(d: T, opts: PrefOpts<T> = {}): Pref<T> {
  return { default: d, scope: "agent-defaulted", ...opts };
}

// Accumulated data — unlike toggles and colors these settings build up over time and cannot be
// recovered once lost (learned reply suggestions, pins, SSM usage tallies, keybindings, working
// sets, the reading dictionary). Server-wins is fine for ordinary settings, but for these an
// empty server value must never overwrite a non-empty local one: an empty server value is far
// more often "not written yet / lost by accident" than "the user deleted it", and the accidental
// case propagates to every device and cannot be undone (see prefsLoaded in sync.ts).
// The cost: a "delete all" on device A does not reach device B, which still holds the entries
// and restores them. Between something you can delete again and something that never comes back,
// the latter is what gets protected.
function accumulated<T>(d: T, opts: PrefOpts<T> = {}): Pref<T> {
  return { default: d, scope: "accumulated", ...opts };
}

const isTrue = (v: unknown): boolean => v === true;

const PREFS = {
  termFont: synced("Source Code Pro"),
  termSize: synced(13),
  /** Terminal: Ctrl+C copies when text is selected (else SIGINT) and Ctrl+V pastes, as in
   * Cloud Shell. Off keeps both keys for the PTY (^C / ^V). */
  termCtrlCV: synced(true),
  /** CJK font: draws only the East Asian Width = Ambiguous characters (①②③ and the like)
   * with this font instead of the Latin code font (applyCjkFont / CJK_UNICODE_RANGE). One of
   * CJK_FONTS; CJK_FONT_AUTO leaves it to the OS, CJK_FONT_OFF keeps the Latin font. */
  cjkFont: synced(CJK_FONT_AUTO),
  viewerFont: synced("JetBrains Mono"),
  viewerSize: synced(13),
  chatFont: synced("システム"), // i18n-exempt: raw font value matched in fontStack
  chatSize: synced(14),
  lineNumbers: synced(true),
  wrap: synced(false),
  tabSize: synced(4),
  minimap: synced(true),
  // Wrap code blocks in the Markdown view (Doc/File preview, markdown inside chat) by default.
  // This is the initial state of the toggle at each block's bottom right
  // (features/viewer/MarkdownView.tsx); the per-block override it makes is not persisted.
  // Default true — wrapping usually reads better than horizontal scrolling.
  markdownCodeWrap: synced(true),
  iconSet: synced("vscode"),
  theme: deviceLocal("dark"),
  /** Main-area layout profile. Stored only on this device; each profile's
   * concrete layout remains per user, tenant and browser tab. */
  paneLayout: deviceLocal<"split" | "tabs">("tabs"),
  // UI display language (docs/log/28 / ADR 0016), "ja" | "en". Unlike theme this is not
  // device-local: it syncs through the server so the language follows the person onto every
  // device. The default comes from the browser language, falling back to Japanese (detectLocale).
  locale: synced(detectLocale()),
  // Per-region base theme, independent of `theme`: "inherit" (default, follow the app),
  // "dark", or "light". Applied by scoping data-theme onto the region container.
  // mirrorTheme → .mirrorview (session mirror); assistantTheme → .chatview (assistant chat).
  mirrorTheme: deviceLocal("inherit"),
  // sharedTheme → .shared-view (the recipient side of a shared session, docs/log/59).
  sharedTheme: deviceLocal("inherit"),
  assistantTheme: deviceLocal("inherit"),
  topbarColor: deviceLocal("default"),
  leftpaneColor: deviceLocal("default"),
  viewerColor: deviceLocal("default"),
  // Surface color for the session mirror (chatColor), the recipient-side shared session
  // (sharedColor) and the assistant chat (assistantColor).
  chatColor: deviceLocal("default"),
  sharedColor: deviceLocal("default"),
  assistantColor: deviceLocal("default"),
  // Keep a separate appearance (APPEARANCE_KEYS) per tenant on this device. Device-local, off by
  // default; only offered to members with two or more memberships.
  appearancePerTenant: deviceLocal(false),
  // Markdown mirror composer: "mod-enter" = Ctrl/⌘+Enter submits, Enter inserts a
  // newline (phone-friendly default); "enter" = Enter submits, Shift+Enter newline.
  mirrorSend: synced("mod-enter"),
  // Default claude model for new sessions (launch dialog + repo launch). Usually a tier
  // alias (opus/sonnet/haiku), but may be a user-registered full id to pin a release.
  defaultModel: synced(DEFAULT_MODEL), // concrete tier (avoids claude's release-varying own pick)
  // Per-agent launch defaults. defaultModel remains as a migration mirror for older
  // Console/server prefs; new code reads this map for all three agent kinds.
  //
  // No descriptor normaliser: the map is read through a migration from the legacy defaultModel,
  // whose seed differs between load (store.ts) and hydrate (sync.ts), and re-reading a merged map
  // would re-seed it.
  agentLaunchDefaults: synced<AgentLaunchDefaults>(DEFAULT_AGENT_LAUNCH),
  // Models to hide (AgentsTab > each card > behaviour settings): kind → excluded model ids.
  // The motive is preventing billing accidents (on a Claude Team plan, Fable is charged as API
  // credit); model id namespaces differ per kind, so the exclusion list is kind-scoped too. The
  // Agent reads this key from ui-prefs, narrows both the picker and MCP list_models, and then
  // refuses outright a launch that names an excluded model (workspace/agent/model_deny.go).
  //
  // New accounts exclude claude's Fable by default: on a Claude Team plan Fable is charged as
  // API credit, so this prevents an accidental pick from the very first launch. Existing users'
  // saved settings are not overwritten — load()/hydrateUIPrefs() apply defaults only to keys
  // that were never saved.
  hiddenModels: accumulated<Record<string, string[]>>({ claude: ["fable"] }),
  // Claude Code OAuth has no account-aware catalog endpoint. Full model ids registered by
  // the user become durable choices in the Console picker and MCP list_models.
  claudeCustomModels: accumulated<string[]>([], { normalize: normalizeClaudeCustomModels }),
  // ON/OFF for the SESSION title suggestion (Settings > AI assist). Covers both the automatic
  // banner and the rename dialog's "ask AI for a suggestion" (「AIに提案してもらう」) button;
  // the chat side is assistantTitleSuggest and branch names are branchSuggestEnabled — each AI
  // assist feature gates on its own key now (it used to also silently gate branch names).
  // Default true so existing users get it without an explicit opt-in.
  autoTitleSuggest: synced(true),
  // Global ON/OFF for session-to-session messaging (AgentsTab > Sessions, docs/log/58 /
  // ADR 0041). Not per-agent: it applies to every kind that gets af's own MCP server
  // (claude / codex / opencode / cursor / kiro / agy / copilot), and shell / ssm can
  // neither send nor receive by construction.
  // Default FALSE, unlike autoTitleSuggest: letting sessions type into each other
  // widens the injection surface (a session that read a poisoned repo can reach every
  // other session), so it has to be chosen rather than inherited on upgrade. The Agent
  // reads this key from ui-prefs to decide whether the session-side MCP server
  // advertises the two peer tools at all.
  peerMessaging: synced(false), // opt-in (docs/log/58 / ADR 0041) — not a surface to widen by default
  // Global ON/OFF for the image generation tool (AgentsTab > Sessions, ADR 0069). Same shape
  // as peerMessaging: not per-agent, read by the Agent from ui-prefs to decide whether the
  // session-side MCP server is launched with --image-gen at all.
  // Default FALSE. Every generated image spends the user's ChatGPT plan quota — 3-5x faster
  // than a text turn — from sessions that are not Codex sessions and would otherwise never
  // touch it, so it is chosen rather than inherited on upgrade.
  imageGeneration: synced(false), // opt-in (ADR 0069) — it spends the ChatGPT plan quota
  // NOTE: sessionFleetObserve was here (docs/log/86 stage 1) and is gone. Fleet observation —
  // reading another session's state and usage, reading and writing your user's memo queue — is
  // part of every session now, with no switch, so a stored value would only be misleading.
  // Session steering (ADR 0073): create_session plus the reads that choose where to launch,
  // and driving the sessions this one started (output / stop / stop-after-turn / resume).
  // Deletion stays closed even for a session's own children.
  // Default FALSE — this is the one that spends the shared host with nobody watching. (It used
  // to be offered only once observation was on; that prerequisite went with the switch.)
  sessionFleetSpawn: synced(false), // opt-in (ADR 0073) — lets a session spend host resources unattended
  // Past-session search for sessions (ADR 0110): whether the session-side MCP server offers
  // search_sessions. Default TRUE, unlike the switches above, and matching the Agent's own
  // missing-key answer: it only reads this user's transcripts, which a session's shell can
  // already open. The Console's own search (the palette's conversations mode) ignores it.
  sessionSearch: agentDefaulted(true), // opt-out (ADR 0110) — read-only, and the Agent treats a missing key as on
  // AF-owned agent memory for sessions (ADR 0108): whether the session-side MCP server offers the
  // memory_* tools, and whether the Agent answers them. Default FALSE, like the Agent's own
  // missing-key answer: what one session saves is read by every kind in later sessions, so it is
  // turned on knowingly. The Console's change list works whatever this is set to.
  agentMemory: agentDefaulted(false, { normalize: normalizeAgentMemory }), // opt-in (ADR 0108) — what one session saves reaches every kind later
  // AF memory for an agent outside Agent Fleet, through the Control Plane's /mcp (#1988): "off",
  // "read" or "write". Separate from agentMemory: that governs sessions in the workspace, this a
  // client on another machine, whose model then sees the memory text. Default "off", like the
  // Agent's own missing-key answer (uiprefs.ExternalMemory).
  externalMemory: agentDefaulted<ExternalMemoryMode>("off", { normalize: normalizeExternalMemory }), // opt-in (#1988) — memory text leaves the workspace for an outside client's model
  // The background memory review (ADR 0117): "off" or "propose". Default "off", like the Agent's own
  // missing-key answer (uiprefs.MemoryReview): conversation excerpts go to a model. It also needs
  // agentMemory on. There is no control for it yet; this is the key the Agent reads.
  memoryReview: agentDefaulted<MemoryReviewMode>("off", { normalize: normalizeMemoryReview }), // opt-in (ADR 0117) — conversation excerpts go to a model, and it spends tokens
  // Automatic background review (ADR 0117 decision 1): the turn-end loop may claim sessions on its
  // own. Default false, like the Agent's own missing-key answer (uiprefs.MemoryReviewAuto); the manual
  // "Suggest memories now" needs only memoryReview.
  memoryReviewAuto: agentDefaulted(false, { normalize: isTrue }), // opt-in (ADR 0117 decision 1) — the loop spends tokens unattended
  // Muse Code's contributor mode (#2535, Settings > Agents > Muse Code): on, Meta may use the
  // conversations to improve its product. Default false, like the Agent's own missing-key answer
  // (uiprefs.MuseContributorMode). A muse session records the value it was created with and keeps
  // it across stop and start, so switching affects sessions created afterwards.
  museContributorMode: agentDefaulted(false, { normalize: isTrue }), // opt-in (#2535) — the vendor may use the conversations
  // How many children ONE session may have at a time (ADR 0073 decision 6, AgentsTab > Session).
  // Per parent, not per workspace: two parents at the ceiling is twice that many agents.
  //
  // The Agent clamps it to 1..SPAWN_CHILD_LIMIT_MAX and falls back to the default outside that,
  // so a value hand-edited into ui-prefs cannot raise the ceiling. Offered as a fixed set of
  // choices rather than a free number precisely so the range is a property of the control.
  sessionSpawnChildLimit: synced(3), // the value the limit had while it was a constant (ADR 0073 decision 6)
  // How long a stopped session stays in the active list before it moves to the archive (ADR
  // 0097; AgentsTab > Session). Days from STOPPED_ARCHIVE_DAYS, STOPPED_ARCHIVE_NEVER for "off",
  // or 0 for "the deployment default" — AF_SESSION_STOPPED_TTL, else 7 days. 0 is stored rather
  // than 7 so the deployment's env var still applies to a user who never picked a period. The
  // Agent reads it on every session list, so a change applies on the next list with no restart.
  sessionStoppedArchiveDays: synced(0), // the deployment default (ADR 0097)
  // The spend budget a new session gets when its launch names none (#1054, AgentsTab > Session),
  // in USD; 0 = none. The Agent reads it at create time, so MCP and scheduled launches get it
  // too. One of SPEND_CAP_DEFAULTS — the spend is an estimate, and round figures say so.
  sessionSpendCapUsd: synced(0), // no budget (#1054)
  // Which image provider generate_image tries first (AgentsTab > Sessions, ADR 0069). The
  // Agent normalizes whatever is stored into a TOTAL order — unknown ids and duplicates drop,
  // unmentioned providers append in the built-in order — so a list saved before a provider
  // existed still ranks it. Same rule as normalizeImageProviderOrder below, so the UI shows
  // exactly what the backend will do.
  //
  // It only became a setting when there were two providers to rank: ordering a list of one is
  // not a choice. The order matters because the providers spend DIFFERENT accounts — codex the
  // ChatGPT plan, agy the Antigravity one — and "auto" walks this list, falling through to the
  // next when one is signed out or out of quota.
  imageProviderOrder: synced<string[]>([...IMAGE_PROVIDERS]),
  // How the opencode launch-model list is shaped (AgentsTab > opencode). One
  // OPENCODE_API_KEY opens both opencode.ai billing routes, so the same model shows up
  // twice: opencode/… (Zen, pay-per-request) and opencode-go/… (the Go subscription).
  // Which opencode.ai billing route this workspace means to use:
  //   "off"  — hard-disables opencode: overrides any stored key or OAuth login, even
  //            ones added later without switching the route back. For tenants whose
  //            security policy forbids opencode reaching a third party without an
  //            explicit, durable opt-in. It is also the DEFAULT, so a fresh workspace
  //            launches nothing until someone chooses one of the routes below.
  //   "own"  — opencode without opencode.ai: only the providers connected directly
  //            (anthropic/…) and the fleet's own engines, and OPENCODE_API_KEY is not
  //            injected. The card shows it as a route because that is how it reads to a
  //            user ("do not bill opencode.ai"), even though it is the absence of one.
  //   "free" — the zero-auth free models only. Also makes opencode launchable with no
  //            credential at all, and the Agent stops injecting OPENCODE_API_KEY.
  //   "go"   — the subscription route (opencode-go/…). Needs an API key (measured: an
  //            account login alone does not grow the Go ids).
  //   "zen"  — pay-per-request (opencode/…), plus the Go ids when the account has both.
  // The Agent reads this from ui-prefs, so it shapes the MCP list_models an assistant
  // picks from as well as this picker. Legacy values migrate in normalizeSettings.
  //
  // The card presents this as TWO controls — an on/off switch and a route list — because
  // "off" is a different kind of decision from the rest: it governs the whole kind, while
  // the others only pick which side of opencode.ai is listed. One 4-way control made the
  // three routes look like they had the same reach as off, and hid that none of them can
  // take another vendor's key away. The stored value stays one string.
  opencodeCatalog: synced<"off" | "own" | "free" | "go" | "zen">("off", { migrate: migrateOpencodeCatalog }),
  // Whether the lcpp (llama.cpp) kind may be launched at all (Settings > Agents > llama.cpp,
  // docs/log/105 §106.2). Unlike opencodeCatalog's "off" this kind has no billing route to fall
  // back to — off simply hides it from every launch menu and the registry's available()
  // predicate. Default true (opt-out): existing deployments already launch lcpp today, so
  // upgrading to this setting must not silently stop them. The Agent reads the same key
  // (ui-prefs lcppEnabled) as the actual, server-side gate — this switch is not decorative.
  lcppEnabled: synced(true), // opt-out (docs/log/105 §106.2) — an existing deployment launches lcpp today
  // Show the mirror's thinking block expanded from the start (kind-scoped; Settings > Agents >
  // each card > behaviour settings). Default off for every kind, i.e. collapsed as before and
  // opened by a click. How much thinking a backend emits varies enormously by kind and model, so
  // whether always-expanded reads well depends on the backend — hence a Record keyed by kind
  // like hiddenModels (an unset kind is false).
  expandThinking: accumulated<Record<string, boolean>>({}),
  // How the mirror shows the reply the agent is still writing (kind-scoped; Settings > Agents >
  // each card > behaviour settings; #1250, #1274): "off", "lines" (each poll's text as it comes)
  // or "typewriter" (typed out between polls). Unlike expandThinking an unset kind is ON —
  // typewriter — and off is the opt-out for those who would rather see a reply once it is
  // complete. Booleans are what the setting stored while it was an on/off switch: false is off,
  // true is typewriter; read through streamReplies(), never directly. Off is still written as
  // false (StreamRepliesRow), so an older Console sharing the synced prefs — whose accessor takes
  // any non-false value as on — stays off too. Only kinds whose Agent route sends the in-progress
  // reply read it (claude).
  streamReplies: synced<Record<string, boolean | StreamRepliesMode>>({}),
  // ON/OFF for the CHAT title suggestion (Settings > AI assist; the rename dialog's "ask AI for
  // a suggestion" (「AIに提案してもらう」) button — the assistant has no automatic banner). Split out of
  // autoTitleSuggest so sessions and chats gate independently; load()/hydrateUIPrefs
  // migrate an explicit legacy OFF, and the Agent falls back to autoTitleSuggest when
  // this key is absent.
  assistantTitleSuggest: synced(true),
  // ON/OFF for the branch-name AI suggestion (Settings > AI assist; the worktree / branch
  // rename dialogs). Used to ride on autoTitleSuggest, which no label or note ever
  // said — one AI assist feature, one key. Migrates from autoTitleSuggest so an explicit
  // legacy OFF keeps branch names off too.
  branchSuggestEnabled: synced(true),
  // ON/OFF for the File pane's AI edit suggestion (Settings > AI assist). Previously had no
  // setting at all and was always on; default true keeps that behaviour.
  editSuggestEnabled: synced(true),
  // ON/OFF for the mirror's per-answer translation button (Settings > AI assist, docs/log/97).
  // Default true: the button spends nothing until it is pressed, and a deployment that does not
  // want that spend at all turns it off here (the Agent gates on the same key).
  mirrorTranslateEnabled: synced(true),
  // Press the translate button for the reader when a turn finishes, instead of waiting for them
  // to press it (Settings > AI assist, docs/log/97 §97.12). Default FALSE, unlike the button
  // itself: this is the one translation setting that spends without anyone asking, so it is
  // opted into. Read only by the mirror (the Agent sees an ordinary request carrying
  // trigger=auto); mirrorTranslateEnabled off disables it whatever this says.
  mirrorAutoTranslate: synced(false), // opt-in: the only translation setting that spends unasked
  // ON/OFF for the chat plan's explicit "refresh" button (Settings > AI assist, docs/log/103
  // §103.3-2). Previously had no setting at all and was always on, like editSuggestEnabled;
  // default true keeps that behaviour. The Agent gates the same key server-side
  // (HandleChatPlanRefresh) — the toggle here is not the only thing standing between a press
  // and a model run.
  planUpdateEnabled: synced(true),
  // Forced output language for assistant chat: "auto" = follow the input language
  // (default), "ja" / "en" = always reply in that language (even for foreign-language
  // content). The Agent reads this key from ui-prefs and injects a language rule into the
  // chat system prompt (translate assistant is exempt). See docs/log/19.
  // CHAT ONLY. Read-aloud used to borrow this key to pick the TTS engine and voice;
  // that axis is now ttsLang — one key, one meaning.
  outputLanguage: synced("auto"),
  // Assistant-CHAT backend priority (AssistantTab reordering): auto-selection takes the
  // first CONNECTED kind in this order (the Agent's preferredChatAgent, read live from
  // ui-prefs). Applies to builtin assistants' NEW conversations; user-defined assistants
  // keep their own per-assistant agent choice. Replaces the legacy single-pin
  // assistantAgent key — hydrateUIPrefs migrates a stored pin by promoting it to the
  // front, and the Agent normalizes partial/stale lists against its own default order.
  // Chat only. The one-shot helpers rank backends with aiAssistOrder.
  assistantAgentOrder: synced<string[]>([...ASSISTANT_AGENT_KINDS]),
  // AI-assist generation backend priority (Settings > AI assist): the same normalization, ranked
  // separately from the chat because the two want opposite things — the chat wants the
  // strongest CLI, the one-shots run constantly and want the cheapest one that works.
  // Migrated from assistantAgentOrder (they were one list), so nothing changes on upgrade.
  aiAssistOrder: synced<string[]>([...ASSISTANT_AGENT_KINDS]),
  // Per-feature agent pin (Settings > AI assist, docs/log/103 decision 1): "" (missing) means
  // no pin — the feature follows aiAssistOrder like every other one-shot. Keyed by the same
  // feature ids the usage ledger already uses (usage.val.feature.*), so the row someone reads
  // in the usage view is the row they adjust here.
  aiFeatureAgents: synced<Record<string, string>>({}),
  // Per-feature, per-backend model override, one level under aiFeatureAgents (docs/log/103
  // decision 4): aiFeatureModels[feature][kind]. Writing a concrete model needs a kind, so the
  // Console only offers this once the row is pinned to one CLI — "auto" can pick "default" or
  // "recommended" only. Switching the pin to a different CLI does not erase the model stored
  // for the previous one (kind-scoped, same shape as aiShortModels/aiProseModels): flipping the
  // pin back recovers it.
  aiFeatureModels: synced<Record<string, Record<string, string>>>({}),
  // Per-backend models for builtin assistant conversations. "recommended" resolves
  // against the live catalog (and is shown with its current concrete result); empty
  // delegates to the CLI default. Explicit models are kept for every backend so
  // priority fallback never silently changes the requested model.
  assistantModels: synced<Record<string, string>>({
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
    muse: ASSISTANT_RECOMMENDED_MODEL,
  }),
  // AI-assist generation models, split by what the call actually needs (Settings > AI assist):
  //   aiShortModels — a short label: session/chat titles, branch names, reply chips.
  //     Recommended resolves to a cheap tier (haiku class).
  //   aiProseModels — text a human will read and keep: File pane edit suggestions,
  //     chat plan updates. Recommended resolves to the mid tier (sonnet class).
  // They were one key (assistantUtilityModels) whose label only mentioned titles and
  // suggestions, yet a value set there ALSO replaced the prose defaults — picking haiku
  // for titles quietly downgraded edit suggestions. BOTH inherit that old value, so the
  // split changes nothing on release; what it buys is being able to move them apart
  // afterwards.
  aiShortModels: synced<Record<string, string>>({
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
    muse: ASSISTANT_RECOMMENDED_MODEL,
  }),
  aiProseModels: synced<Record<string, string>>({
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
    muse: ASSISTANT_RECOMMENDED_MODEL,
  }),
  // Auto turn on session reports (docs/log/30): when a session an af_write assistant
  // launched/steered reports back, the assistant runs one turn automatically to
  // process it. Default ON; the backend caps unattended turns at 10 per conversation
  // (reset by a user message) regardless of this switch.
  assistantAutoTurn: synced(true),
  // Ceiling on unattended auto turns per conversation (reset whenever the user sends
  // a message). Backend clamps to [1, 50] — there is no unlimited mode; the clamp is
  // the structural runaway stop (docs/log/30).
  assistantAutoTurnLimit: synced(10),
  // Model used for auto turns only (claude conversations only; empty = keep the conversation's
  // model). Processing a report is routine work, so routing it to a light model such as haiku
  // cuts token cost substantially. User turns and compaction summary turns stay on the
  // conversation's own model (chatAutoTurnModel on the Agent side).
  assistantAutoTurnModel: synced(""),
  // Coalescing window for auto turns (seconds; 0 = immediate). A completion report does not
  // trigger an auto turn right away: every report arriving inside the window is processed in one
  // turn (report cards and notifications stay immediate). The Agent's chatAutoTurnDelay clamps
  // it to 600 seconds.
  assistantAutoTurnDelay: synced(60),
  // Quiet completion reports: a normal completion report runs no auto turn (card and
  // notification only; the report rides along on the next turn). Failures, questions and plan
  // approvals behave as before. Default OFF.
  assistantQuietCompletion: synced(false),
  // Auto-pilot (docs/log/30): when an instructed session stops at an AskUserQuestion, the
  // operator answers with the session's own recommendation; when it stops at plan
  // approval, the operator has another session review the plan, feeds back findings,
  // and approves once clean — sharing each decision in chat. Default OFF — acting in
  // the user's stead is consequential, so this is a deliberate opt-in; unclear or
  // destructive choices/plans still ask the user.
  assistantAutoPilot: synced(false),
  // Auto-resume after an abort (docs/log/47): when a session's turn is CUT OFF before it answered by
  // something that clears on its own (dropped connection, temporary rate limit), the
  // operator nudges it to continue instead of only relaying to the user. Default ON —
  // unlike auto-pilot this makes no decision on the user's behalf, it just re-runs work
  // they already asked for; failures whose cause won't clear (usage limit, prompt too
  // long) are classified out and never auto-resumed.
  assistantAutoResume: synced(true),
  // Auto-resume after a usage-limit reset (docs/log/47 §4-4, Agent settings > Claude): a claude
  // session cut off by its usage limit gets a one-shot schedule at the reset instant
  // that tells it to carry on (the agent books it with the CP scheduler, so a workspace
  // stopped in the meantime is woken for it). Default ON. Note this toggle governs the
  // RESUME only — dismissing the limit menu itself ("stop and wait", the no-charge
  // option) happens either way, because while it is up the session accepts nothing at all.
  rateLimitAutoResume: synced(true),
  // Auto-resume a cut-off turn (docs/log/47 §4-6): when a claude turn dies on something that
  // clears on its own (dropped connection, temporary rate limit, stream idle timeout),
  // the agent itself re-sends 「続けて」 (continue) after a short backoff, up to maxAutoResumeAttempts.
  // Default ON. Unlike assistantAutoResume this needs no assistant conversation — it
  // applies to every claude TUI session — and the assistant only hears about the cut-off
  // once the retries are exhausted (which is what makes it cheaper in tokens, not just
  // in latency).
  claudeAbortAutoResume: synced(true),
  // Preventive auto-compaction (docs/log/33 stage 4): when a chat's context is still at/above
  // the backend threshold (90%) as a new turn starts, summarize-and-hand-off first.
  // Default ON — the 80% notice gives a manual window before this fires.
  assistantAutoCompact: synced(true),
  // Absolute token threshold for auto-compaction, OR'd with the relative 90% (docs/log/33
  // §5.1). A resume-driven chat re-reads the whole context every turn, so occupancy is directly
  // the price of a turn. The Agent's chatAutoCompactTokenThreshold clamps it to a 20k floor.
  assistantAutoCompactTokens: synced(150000),
  // Fetch limit for get_session_output (the tool an operator reads session output with), in KiB
  // and from the tail only. Tool results accumulate in the conversation context, so this limit
  // sets the price of every later turn. The Agent's mcpSessionOutputTail clamps it to
  // [4, 1024] KiB.
  assistantOutputTailKiB: synced(32),
  // Per-SSM-host terminal color: host id → color id (see lib/termcolor SSM_HOST_COLORS).
  // Applied to a session's terminal background when it's created (sent as its color).
  ssmHostColors: accumulated<Record<string, string>>({}),
  // Read-aloud defaults, i.e. the recommended setup. They are the same values the settings tab's
  // reset button restores (TTS_RESET), so new users — and existing users who never chose — start
  // here. Only read-aloud itself and the voice notification start OFF; everything else is the
  // setup that is comfortable once it is turned on (per-session voices, fast speed, auto-read
  // across all panes, confirmation questions, katakana readings, a 15-minute cache). TTS_RESET is
  // derived from these DEFAULTS by picking out the TTS keys, so the two can never disagree.
  //
  // Read-aloud (TTS, docs/log/24 + ADR0013): speaks agent answers through VOICEVOX (Zundamon)
  // by calling the CP-native /api/tts/synthesize sentence by sentence (features/chat/tts.ts).
  ttsEnabled: deviceLocal(false),
  // Provider choice. auto = Zundamon when the text is Japanese and the engine is ready, an
  // automatic fallback to Polly JP when the engine is absent (starting or disabled), and Polly
  // for anything not Japanese. The CP makes the final call, being the single source of truth for
  // engine readiness. An explicit "polly" always uses Polly.
  // "auto" | "voicevox" | "polly"
  ttsProvider: synced("auto"),
  // Read-aloud language ("auto" | "ja" | "en"). auto follows the UI display language. It decides
  // the engine when ttsProvider is auto (en → Polly) and Polly's default VoiceId (en → Joanna).
  // Deliberately separate from the assistant's reply language (outputLanguage), which it used to
  // borrow: one key with two meanings meant that setting the chat's reply language to English
  // silently turned the mirror's read-aloud into Polly. The migration carries the old value
  // over, so nothing changes on upgrade.
  ttsLang: synced("auto"),
  // VOICEVOX speaker number ("3" = Zundamon, normal style)
  ttsVoiceVoicevox: synced("3"), // normal style
  // Polly VoiceId ("Takumi" etc.); also used for the auto fallback
  ttsVoicePolly: synced("Takumi"),
  // 0.5 to 2.0 (speedScale)
  ttsSpeed: synced(1.25), // fast
  // Output volume multiplier (0..1) for Zundamon (VOICEVOX). Zundamon is louder at source than
  // the other characters, so it is turned down slightly to match the other voices and the
  // notification sounds. Applied to the playback gain only when the Zundamon voice is used
  // (VV_CHAR_NAMES identifies its speakers); it is not a synthesis parameter, so it does not
  // invalidate the cache.
  ttsZundamonVolume: synced(0.85), // Zundamon turned down slightly to match the other voices
  // How to play while the Console is a background tab or minimized (document.hidden), or focus
  // has moved to another window. mute = silent; quiet = lower the master volume (35% by default,
  // adjustable with ttsBackgroundVolume); normal = normal volume.
  ttsBackgroundPlayback: synced<"mute" | "quiet" | "normal">("quiet"),
  // Master volume multiplier (0..1) for background playback (ttsBackgroundPlayback = "quiet"),
  // set with a slider. Returning to the Console ramps smoothly back to normal volume
  // (ttsMasterGain in ttsControl.ts).
  ttsBackgroundVolume: synced(0.35), // 35% in the background (the value of the former fixed HIDDEN_TTS_GAIN)
  // Pan a pane's read-aloud in stereo to match the pane's current horizontal column. Even at the
  // far edges it is capped at ±70% rather than panned fully, which is easier to listen to.
  ttsStereoByPane: synced(true),
  // Announce by voice when a background session answers or asks (docs/log/24 Tier1). A separate
  // axis from the chat's automatic read-aloud (ttsEnabled): it reads a short announcement
  // prefixed with the session name, through a serial queue. Only while the tab is visible,
  // because session monitoring stops on document.hidden.
  ttsSessionNotify: deviceLocal(false),
  // Notify when a subscription usage window (5-hour / weekly) resets from a state where the
  // limit had actually been hit (app/usageResetNotify.ts). Uses the resetsAt on the WsBar usage
  // chip and raises a browser notification plus, when read-aloud is on, a short spoken "you can
  // resume now" message. A normal reset that never hit the limit stays silent, to avoid spam.
  // Detected reliably while the Console tab is open; a reset that happened while it was closed
  // notifies exactly once on the next open.
  usageResetNotify: deviceLocal(true),
  // Notify (OS notification and voice) when a CHILD session — one another session spawned
  // (origin "session") — finishes a turn and waits for input. Its parent is the one waiting on
  // that turn, so a fleet of children otherwise pings once per child turn. Off also marks the
  // notification read on arrival (no dot, no unread count; the row stays). Questions and
  // permission requests from a child still notify: nobody but a person can answer those.
  childIdleNotify: synced(true),
  // The notification settings table (features/notifications/prefs.ts): per-row overrides of the
  // unread dot, sparse, row -> false. Absent means the row's legacy key or ON, so an empty map is
  // exactly the behaviour before the table existed. Synced, because unread is the CP's per-member
  // seen mark: a device that muted a row marks it read for every device.
  notifyUnread: synced<Record<string, boolean>>({}),
  // The same table's "row.os" / "row.voice" overrides. Device-local like ttsSessionNotify:
  // whether this device interrupts depends on where it is.
  notifyDevice: deviceLocal<Record<string, boolean>>({}),
  // Convert English words to katakana before handing them to VOICEVOX (docs/log/24, the CP's
  // enkana preprocessing), so English is read plausibly in a Japanese accent without leaving
  // Zundamon's voice. It is a transliteration based on the CMU pronouncing dictionary, so a word
  // comes out phonetically (カフィー) rather than as the established loanword (コーヒー).
  ttsEnglishKana: synced(true),
  // User reading dictionary (docs/log/24). One "spelling=reading" per line, applied as a literal
  // substitution to the text just before it is spoken (English, Japanese or symbols alike, and
  // regardless of whether enkana is on). Longer spellings match first. Empty = disabled. See
  // parse/applyUserDict in features/chat/ttsText.ts.
  ttsUserDict: accumulated(""),
  // Synthesis cache ceiling, in total seconds of audio. Keeps audio for identical text under
  // identical synthesis conditions in memory so a repeat reading is instant
  // (features/chat/tts.ts). About 0.1MB per second as PCM. 0 = no cache.
  ttsCacheSec: synced(900), // 15 minutes
  // Mirror (chat): read a new answer aloud karaoke-style as soon as it arrives for the active
  // pane's session (features/mirror/turnTts.ts). Complements ttsSessionNotify, the short
  // announcement for sessions you are not watching — this one reads the body you are looking at.
  ttsAutoReadMirror: synced(true),
  // Read aloud in every open pane, not just the active one (a sub-option of ttsAutoReadMirror).
  // New answers from all panes queue serially into a single playback. The same session open in
  // several panes is still read by one pane only (the ownership registration in
  // features/mirror/turnTts.ts). Combined with per-session voices (ttsVoicePerSession), the
  // voice tells you which pane an answer came from.
  ttsAutoReadAllPanes: synced(true),
  // Read the settled work trace (the narration before the final answer) quietly. off = do not
  // read it; whisper and hushed select the matching VOICEVOX styles. A speaker without those
  // styles, and Polly, simply lower the volume of the same voice.
  // "off" | "whisper" | "hushed"
  ttsWorkRead: synced("off"),
  // Output volume multiplier (0..1) when the work trace is read quietly (ttsWorkRead != off),
  // set with a slider. Independent of the whisper/hushed style: this is the output gain that
  // makes it audibly quieter than the final answer.
  ttsWorkVolume: synced(0.5), // quiet work trace (between the former whisper 0.58 and hushed 0.3)
  // Give each session its own voice (sessionVoiceOpts in features/chat/tts.ts). A hash of the
  // session name deterministically picks from the speaker pool (the standard VOICEVOX characters
  // / Polly's three voices), applied to the mirror's read-aloud and to session voice
  // notifications, so the voice identifies the session. The chat tab and the reader view keep
  // the selected speaker.
  ttsVoicePerSession: synced(true),
  // Per-character settings (voiceCharacters / activeVoicePool in features/chat/tts.ts), keyed by
  // the engine's character name. use = include it in the session voice pool and the reader
  // view's choices (unset means the default pool: true only for the characters listed in
  // SESSION_VOICES in tts.ts); style = the base style (a speaker number, normal when unset);
  // speed = a per-character speed (the global ttsSpeed when unset). The list itself comes from
  // the VOICEVOX engine's live catalog (GET /api/tts/speakers).
  //
  // {} = the 14 standard characters (SESSION_VOICES in tts.ts) are what sessions get assigned.
  // A new user and a reset both start here, so the character set is always the same 14. Extra
  // characters the engine offers must be ticked individually in the character list to be used.
  ttsVoicePool: accumulated<Record<string, TtsCharConf>>({}),
  // Switch the emotion style by what the sentence says (emotionOpts in features/chat/tts.ts):
  // errors and failures are read in the ツンツン (prickly) styles, successes and completions in
  // the あまあま (sweet) ones. Only effective for speakers that have style variants (Zundamon,
  // Shikoku Metan, Kyushu Sora).
  ttsEmotion: synced(false),
  // Mirror (chat): when the active pane's session stops for confirmation (AskUserQuestion, plan
  // approval, a permission request), read the question and its choices aloud. For a choice it
  // prefers the description (the tooltip body) over the display label (pendingSpeech in
  // features/chat/ttsText.ts).
  ttsReadPending: synced(true),
  // In the mirror's automatic read-aloud, a long answer (roughly over 500 characters) is
  // summarized to two sentences by the assistant (a headless CLI) and that is read instead
  // (features/mirror/MirrorView.tsx). The full body is always available from the turn's
  // read-aloud button. A failed or timed-out summary falls back to reading the whole text.
  ttsSummaryRead: synced(false),
  // Read inline code (`...`) abbreviated (abbrevCode in features/chat/ttsText.ts). A hash becomes
  // its first two characters plus a filler word (なんとか and the like); camelCase and paths
  // become the first word plus a filler, with the last word appended when there are three or
  // more. Short tokens, anything containing a space or Japanese, and spellings covered by the
  // reading dictionary are left as they are. A bare hash without backticks (lowercase hex such
  // as f437e17, or a UUID) is treated the same way (isBareHash).
  ttsAbbrevCode: synced(true),
  // When a particle (を・は・で・に・と) is immediately followed by a kanji, insert a comma so
  // it is read with a small pause (pauseParticles in features/chat/ttsText.ts) — a breath,
  // shorter than the beat a full stop gives.
  ttsParticlePause: synced(true),
  // Show the reader view (docs/log/24) in vertical writing; default false = horizontal. Follows
  // the toggle in ReaderView.
  readerVertical: synced(false),
  // Reader view voice. "" = keep the speaker from settings; "vv:<speaker>" = a VOICEVOX
  // character; "polly:<VoiceId>" = Polly. Follows the selection in the ReaderView header
  // (voiceChoiceOpts in features/chat/tts.ts resolves it into a TtsOptions override).
  readerVoice: synced(""),
  // Reader view body font: a READER_FONTS value (明朝 = serif, the default; ゴシック = sans).
  // ReaderView passes it to the body as --reader-font, resolved by readerFontStack.
  readerFont: synced("明朝"), // i18n-exempt: raw font value matched in fontStack
  // Reader view body font size in px. ReaderView passes it to the body as --reader-size.
  readerSize: synced(17),
  // User rebindings of keyboard shortcuts (docs/log/29 + ADR-0017). Key = a command id, or the
  // synthetic id of an app-reserved key (app.leader / app.palette / app.cheatsheet). Value = the
  // overriding code (the normalized chord string from chords.ts; "" disables it explicitly). An
  // unlisted key keeps its default. Only direct accelerators and reserved keys can be rebound —
  // leader sequences (p r and the like) are structure and stay fixed. Resolved by
  // effectiveCommands / boundChord in features/keys/bindings.ts. Synced across devices (not
  // DEVICE_LOCAL).
  keybindings: accumulated<Record<string, string>>({}),
  // Terminal input priority (docs/log/29). When ON, global app shortcuts are suppressed while
  // the terminal (xterm) has focus and keys pass straight through to it (Ctrl combinations go to
  // the terminal). Only the leader key (Ctrl/⌘+K by default, rebindable) survives, and every
  // command is still reachable from it through which-key or the palette. Default OFF, i.e. the
  // app captures.
  terminalPriority: synced(false),
  // Pass keys straight through to shell/SSM terminals (docs/log/29). When ON, and unlike
  // terminalPriority, every app shortcut is suppressed while a shell or ssm terminal has focus —
  // including the leader (Ctrl/⌘+K) and the palette (Ctrl/⌘+P) — so Ctrl+K (kill-line), Ctrl+P
  // (previous history) and the rest reach xterm/PTY unchanged: a pure terminal. Only shell/ssm
  // are affected; agent terminals behave as before. Default OFF. Click elsewhere to leave it.
  shellTermPassthrough: synced(false),
  // Per-SSM-host usage tally: host id → { count = launches, at = last launch epoch ms }. Orders
  // the quick-connect cards in the connection modal (most frequent first, ties broken by most
  // recent). Updated when startSsm succeeds; like ssmHostColors it lives only in client settings.
  ssmHostUsage: accumulated<Record<string, { count: number; at: number }>>({}),
  // ON/OFF for reply suggestions (quick replies, lib/quickReplies). Default ON.
  quickRepliesEnabled: synced(true),
  // ON/OFF for the SESSION MIRROR's reply suggestions v2 (✨), the sparkle button that
  // generates from context with an LLM. Default ON — tokens are spent only when it is pressed.
  // Matches the default of the Agent's replySuggestEnabled (ui-prefs).
  // CHAT's own ✨ is assistantReplySuggestEnabled, a separate key (docs/log/103 §103.3-3): this
  // key used to gate both on the Console side too, through a ui-prefs read the Agent spelled
  // differently (`replySuggest`, never written), so the chat toggle never actually took effect.
  replySuggestEnabled: synced(true),
  // ON/OFF for the CHAT's own ✨ reply suggestions v2 (docs/log/103 §103.3-3/§103.9). Default
  // ON; an explicit legacy OFF on replySuggestEnabled migrates here once (migrateAiAssistPrefs)
  // so nobody who had turned suggestions off finds the chat's ✨ switched back on by the split.
  assistantReplySuggestEnabled: synced(true),
  // Learned data for reply suggestions: normalized key → { text = display spelling,
  // count = times sent, at = last sent epoch ms }. Updated when send() succeeds; same shape as
  // ssmHostUsage and mirrored to the server, so it syncs across devices.
  quickReplies: accumulated<Record<string, { text: string; count: number; at: number }>>({}),
  // Suggestions the user removed from the reply-suggestion menu (normalized keys). Deleting the
  // learned entry alone is not enough — seeding or re-learning brings it back — so the hidden
  // marking is kept separately. Sending the same text again clears it.
  quickRepliesHidden: accumulated<string[]>([]),
  // Pinned (always shown) suggestions. Holds display spellings in pin order rather than keys, so
  // a pin survives the learned data being pruned and the order stays the one the user chose
  // (lib/quickReplies).
  quickRepliesPinned: accumulated<string[]>([]),
  // WS bar agent-usage chips: which agents keep a permanent slot on the bar (session kinds).
  // The bar otherwise shows the two most recently used and folds the rest behind one "+N" chip
  // (app/usageChipPlan.ts). Both lists are the user overriding that ranking — pinning is how you
  // ask for a third chip, so there is no separate count setting.
  //
  // Empty = nothing overridden: the WS bar ranks the usage chips by recent use on its own.
  usageChipsPinned: synced<string[]>([]),
  // Agents whose chip always sits in the folded popover, however recently they were used.
  usageChipsFolded: synced<string[]>([]),
  // Branch-name template for launching from a work item (docs/log/80 P2), read by the Agent's
  // resolver as the user's `*` rule (ADR 0103 decision 2). Placeholders: {ref} {num} {key}
  // {project} {type} {prefix} {slug} (decision 4). Empty string = the resolver's built-in
  // {prefix}{ref}-{slug}, or feature/{key} from an Agent without the resolver (branchForItem).
  // Clearing this does NOT fall back to the server's temp/<slug>; clearing the branch field in
  // the launch dialog is what does that.
  workItemBranchTemplate: synced(""),
  // Working set definitions (docs/log/52 + ADR 0036): named sets of { working copies,
  // conversations, sessions without a repo } that narrow the left pane down to one piece of
  // work. The definitions sync across devices (server ui-prefs); which set is currently being
  // viewed is workingSetActive, which is device-local. Corrupt values and references to entities
  // that no longer exist are neutralized by normalize / the predicates in lib/workingSets.ts.
  workingSets: accumulated<WorkingSet[]>([]),
  // Id of the working set currently shown ("" = all). Device-local like theme (DEVICE_LOCAL), so
  // watching work X on a PC and work Y on a tablet holds per device.
  workingSetActive: deviceLocal(""),
  // The launch modal's personal first-prompt templates (#1469): every repository, or one base
  // repository when `repo` is set. Synced so they follow the user across devices; size-capped by
  // lib/launchTemplates.ts, because one PUT over the Agent's 64 KiB limit fails every key's sync.
  // Wrapped in an object that is never empty once written, so a deliberate "deleted the last one"
  // reaches other devices instead of being restored by the ACCUMULATED empty-server rule.
  launchTemplates: accumulated<LaunchTemplateStore>({}),
  // The launch modal's recent first prompts, newest first, keyed by base repository. Capped and
  // wrapped by lib/promptHistory.ts for the same reasons.
  launchHistory: accumulated<PromptHistoryStore>({}),
} satisfies Record<string, Pref<unknown>>;

type PrefValues = { -readonly [K in keyof typeof PREFS]: (typeof PREFS)[K]["default"] };

export interface Settings extends PrefValues {}

type Key = keyof Settings;

const PREF_KEYS = Object.keys(PREFS) as Key[];
const pref = (k: Key): Pref<unknown> => PREFS[k];

export const DEFAULTS: Settings = Object.fromEntries(PREF_KEYS.map((k) => [k, pref(k).default])) as unknown as Settings;

const keysIn = (scope: PrefScope): ReadonlySet<Key> => new Set(PREF_KEYS.filter((k) => pref(k).scope === scope));
const DEVICE_LOCAL = keysIn("device-local");
const AGENT_DEFAULTED = keysIn("agent-defaulted");
const ACCUMULATED = keysIn("accumulated");

/** Exported as a policy seam so persistence tests can pin which preferences
 * must never cross the device boundary. */
export const isDeviceLocalSetting = (key: keyof Settings): boolean => DEVICE_LOCAL.has(key);

export const isAgentDefaultedSetting = (key: keyof Settings): boolean => AGENT_DEFAULTED.has(key);

/** Exported as a policy seam so tests can pin which preferences must never be
 * silently emptied by a server copy that has lost them. */
export const isAccumulatedSetting = (key: keyof Settings): boolean => ACCUMULATED.has(key);

// Object/array values compared by reference would differ on every hydrate (the server response
// is always a fresh object) — compare by value so a merge reports only a real change.
export const sameValue = (a: unknown, b: unknown): boolean =>
  a === b ||
  (typeof a === "object" && a !== null && typeof b === "object" && b !== null &&
    JSON.stringify(a) === JSON.stringify(b));

/** Each descriptor's migrate over an incoming copy, in place, for the keys it holds. */
export function migrateIncoming(o: Record<string, unknown>): void {
  for (const k of PREF_KEYS) {
    const m = pref(k).migrate;
    if (m && k in o) o[k] = m(o[k]);
  }
}

/** A stored copy (already legacy-migrated) over the defaults, read through the descriptors.
 *  Keys the copy holds that no descriptor names are kept: they go out with the next PUT. */
export function settingsFromStored(saved: Record<string, unknown>): Settings {
  migrateIncoming(saved);
  const out: Record<string, unknown> = { ...DEFAULTS, ...saved };
  for (const k of PREF_KEYS) {
    const n = pref(k).normalize;
    if (n) out[k] = n(saved[k]);
  }
  return out as unknown as Settings;
}

/** After a hydrate merge: each descriptor's normalize over the merged value, in place. Returns
 *  whether any value changed. */
export function normalizeMerged(merged: Settings): boolean {
  const o = merged as unknown as Record<string, unknown>;
  let changed = false;
  for (const k of PREF_KEYS) {
    const n = pref(k).normalize;
    if (!n) continue;
    const v = n(o[k]);
    if (!sameValue(v, o[k])) {
      o[k] = v;
      changed = true;
    }
  }
  return changed;
}

/** The default value of one settings key. A narrow window so that "restore the default"
 *  operations (the keyboard font-size reset and the like) read DEFAULTS as the single source of
 *  truth — a copied default drifts silently away from what a new user gets (TTS_RESET is derived
 *  from DEFAULTS for the same reason). */
export function defaultSetting<K extends keyof Settings>(key: K): Settings[K] {
  return DEFAULTS[key];
}

/** A copy of the whole default set. The window settings export / import (docs/log/79) uses to
 *  check which keys it knows and what shape their values have — if the import side kept its own
 *  key list, a newly added setting would silently stop being carried. */
export function settingsDefaults(): Settings {
  return { ...DEFAULTS };
}

// The initial state of the read-aloud settings, i.e. what the settings tab's reset button writes
// back. It is derived from the TTS keys of DEFAULTS, so it always matches what a new user gets;
// keeping DEFAULTS the single source of truth is what prevents drift. ttsVoicePool is {} (the 14
// standard characters in tts.ts are ticked), so a reset and a new user start from the same
// characters. The reading dictionary (ttsUserDict) is content the user typed, so a reset does not
// erase it and it is not included here.
const TTS_RESET_KEYS = [
  "ttsEnabled",
  "ttsSessionNotify",
  "ttsProvider",
  "ttsLang",
  "ttsVoiceVoicevox",
  "ttsVoicePolly",
  "ttsVoicePerSession",
  "ttsVoicePool",
  "ttsEmotion",
  "ttsSpeed",
  "ttsZundamonVolume",
  "ttsBackgroundPlayback",
  "ttsBackgroundVolume",
  "ttsStereoByPane",
  "ttsAutoReadMirror",
  "ttsAutoReadAllPanes",
  "ttsWorkRead",
  "ttsWorkVolume",
  "ttsSummaryRead",
  "ttsReadPending",
  "ttsAbbrevCode",
  "ttsParticlePause",
  "ttsEnglishKana",
  "ttsCacheSec",
] as const;
export const TTS_RESET: Partial<Settings> = Object.fromEntries(
  TTS_RESET_KEYS.map((k) => [k, DEFAULTS[k]]),
) as Partial<Settings>;

// detectLocale — the initial UI language when no locale is saved. Resolved from the browser
// language, falling back to Japanese for anything unsupported or unknown so existing users see
// no change. Runs once, when DEFAULTS is evaluated.
function detectLocale(): string {
  try {
    if ((navigator.language || "").toLowerCase().startsWith("en")) return "en";
  } catch {
    /* no navigator */
  }
  return "ja";
}
