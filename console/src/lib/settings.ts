import { useSyncExternalStore } from "react";
import { api, apiJSON } from "../core/api/client.ts";
import { setLocale } from "./i18n/index.ts";
import type { MsgKey } from "./i18n/index.ts";
import type { WorkingSet } from "./workingSets.ts";

// Display settings (theme / fonts / file-viewer options / icon set). Persisted in
// localStorage for instant load + offline, AND mirrored to the server per-user
// (GET/PUT /api/env/ui-prefs) so they follow the user across browsers/devices.
// Shared across React (useSettings) and non-React code (term.js via getSettings +
// subscribe). Terminal and viewer fonts are independent — they default to different
// families so they're visibly distinct out of the box.

const KEY = "af-display-settings";

export const CODE_FONTS = [
  "Source Code Pro",
  "JetBrains Mono",
  "Fira Code",
  "IBM Plex Mono",
  "システム等幅", // i18n-exempt: raw font value matched in fontStack (display via font.*)
];

// ── CJK font (which font draws Ambiguous-width characters) ──────────────────
//
// Font fallback applies per character, not per typeface. Kanji and kana are absent from Latin
// fonts, so they fall through to the CJK font at the end of the stack; but ①②③ (U+2460...)
// and ■ ○ ★ Ⅰ Ⅱ *are* present in DejaVu Sans Mono / Menlo / Consolas, so resolution stops
// there and never reaches the CJK font. Those characters are East Asian Width = Ambiguous, and
// a Latin font draws them half-width — thin and small beside a full-width kanji, which is why
// the circled digits in a diff look shrunken.
//
// The fix is an @font-face that maps only that range to the CJK font, placed at the head of the
// stack. Its src depends on the user's choice, so it is not written in CSS: applyCjkFont()
// rewrites a <style> element (tokens.css prepends the same family to --mono).
export const CJK_FAMILY = "AF CJK Ambiguous";
export const CJK_FAMILY_PROSE = "AF CJK Ambiguous Prose";

// Two ranges are routed to the CJK font: one for surfaces where column alignment must not
// break (monospace), and one for prose.
//
// The monospace surfaces (diff, viewer, code blocks) must not shift columns, so they get only
// the Japanese numbering marks, which are never used as parts of an ASCII diagram:
//   U+2160-217F Roman numerals (Ⅰ Ⅱ ⅰ)
//   U+2460-24FF enclosed alphanumerics (① ⑴ Ⓐ) — the main target
//   U+3200-32FF enclosed CJK letters and months (㊀ ㈱ ㍻)
export const CJK_UNICODE_RANGE = "U+2160-217F, U+2460-24FF, U+3200-32FF";

// Prose surfaces (mirror and chat body text, proportional) have no columns, so the range widens
// to the symbols Japanese bullet lists commonly use:
//   U+25A0-25FF geometric shapes (■ □ ○ ● ◇ ▲)
//   U+2600-26FF miscellaneous symbols (★ ☆ ☎)
// They stay out of the monospace range because a ● or ■ inside a CLI box (├─┤) rendered
// full-width pushes the box's right edge out of line: the CLI composes its output counting
// Ambiguous as one column.
export const CJK_UNICODE_RANGE_PROSE = `${CJK_UNICODE_RANGE}, U+25A0-25FF, U+2600-26FF`;

// Deliberately excluded from both: arrows (U+2190-21FF) and box drawing (U+2500-257F). They are
// the skeleton of ASCII diagrams and tree views, which full-width glyphs would break. Greek and
// accented Latin letters are Ambiguous too, but widening them would turn identifiers in code
// full-width, so they are out of scope as well.

// Selectable CJK fonts. CJK_FONT_AUTO leaves the choice to the OS (GENERIC_CJK as it is);
// CJK_FONT_OFF installs no @font-face, i.e. the Latin font's half-width glyphs as before.
// Anything else is a real font name, falling back to GENERIC_CJK when it is not installed.
// i18n-exempt-start: raw font values that get persisted (display via font.*, docs/log/28 §2.4)
export const CJK_FONT_AUTO = "自動";
export const CJK_FONT_OFF = "欧文優先";
export const CJK_FONTS = [
  CJK_FONT_AUTO,
  "Noto Sans Mono CJK JP",
  "Hiragino Kaku Gothic ProN",
  "Yu Gothic",
  "Meiryo",
  CJK_FONT_OFF,
];
// i18n-exempt-end

// Generic CJK chain always appended after the chosen font, so someone who picks "Meiryo" on a
// Mac still lands on Hiragino instead of seeing nothing change at all.
const GENERIC_CJK = [
  "Noto Sans Mono CJK JP",
  "Noto Sans CJK JP",
  "Hiragino Kaku Gothic ProN",
  "Yu Gothic",
  "Meiryo",
  "MS Gothic",
];

// Chat fonts: unlike the code viewer, the chat reads as prose, so proportional
// families are offered first ("システム" = system sans, "セリフ" = serif), with the
// monospace code fonts still available for anyone who prefers them.
// i18n-exempt: raw font values matched in fontStack (display via font.*, docs/log/28 §2.4)
export const CHAT_FONTS = ["システム", "セリフ", "Source Code Pro", "JetBrains Mono", "Fira Code", "IBM Plex Mono"];

// Reader view fonts: the reader shows Japanese prose, so it offers the two families that
// matter for reading — 明朝 (mincho, serif, the default) and ゴシック (gothic, sans) — each
// resolved with CJK fallbacks in readerFontStack.
// i18n-exempt: raw font values matched in fontStack (display via font.*, docs/log/28 §2.4)
export const READER_FONTS = ["明朝", "ゴシック"];

// File-icon sets (brand SVGs under assets/fileicons/<id>/). value = asset subdir.
export const ICON_SETS: { id: string; labelKey: MsgKey }[] = [
  { id: "vscode", labelKey: "iconset.vscode" },
  { id: "material", labelKey: "iconset.material" },
  { id: "devicon", labelKey: "iconset.devicon" },
  { id: "seti", labelKey: "iconset.seti" },
];

// Base UI theme. labelKey is an i18n key, resolved with t() by DisplayTab / TopBar.
export const THEMES: { id: string; labelKey: MsgKey }[] = [
  { id: "dark", labelKey: "theme.dark" },
  { id: "light", labelKey: "theme.light" },
];

// Main-area layout profiles. Both DisplayTab and the TopBar appearance popover use them, so
// the choices are defined here once; otherwise one side gains or drifts a label on its own.
export const PANE_LAYOUTS: { id: "split" | "tabs"; labelKey: MsgKey }[] = [
  { id: "split", labelKey: "display.pane_layout_split" },
  { id: "tabs", labelKey: "display.pane_layout_tabs" },
];

// UI display language (docs/log/28 / ADR 0016). The labels are each language's endonym and are
// never translated, so the list reads in native names whatever the UI language is. The ids must
// match the i18n catalogues / SUPPORTED_LOCALES.
export const LOCALES = [
  { id: "ja", label: "日本語" }, // i18n-exempt: endonym (shown natively whatever the UI language)
  { id: "en", label: "English" },
];

// Per-region theme choices (session mirror / assistant chat). "inherit" = follow the app
// theme above; "dark"/"light" give that region its own base theme, independent of the rest
// of the Console — applied by scoping data-theme onto the region container (.mirrorview /
// .chatview; see tokens.css scoped token blocks + effectiveTheme).
export const REGION_THEMES: { id: string; labelKey: MsgKey }[] = [
  { id: "inherit", labelKey: "region_theme.inherit" },
  { id: "dark", labelKey: "theme.dark" },
  { id: "light", labelKey: "theme.light" },
];

// Resolve a per-region theme preference against the app theme, to a concrete "light"/"dark".
export function effectiveTheme(pref: string, base: string): "light" | "dark" {
  if (pref === "dark" || pref === "light") return pref;
  return base === "light" ? "light" : "dark";
}

// Surface (top bar / left pane) background choices. Each color has a per-theme tint
// so it always contrasts with the theme's text color. "default" = theme default.
export const SURFACE_COLORS: { id: string; labelKey: MsgKey; dark: string | null; light: string | null; accent: string | null }[] = [
  { id: "default", labelKey: "surface_color.default", dark: null, light: null, accent: null },
  { id: "slate", labelKey: "surface_color.slate", dark: "#1b2733", light: "#e2e8f0", accent: "#6b8fc4" },
  { id: "blue", labelKey: "surface_color.blue", dark: "#16263f", light: "#dbe7fb", accent: "#3b82f6" },
  { id: "green", labelKey: "surface_color.green", dark: "#15291f", light: "#dcefe0", accent: "#2fb872" },
  { id: "purple", labelKey: "surface_color.purple", dark: "#241a33", light: "#ece0fb", accent: "#a875f5" },
  { id: "warm", labelKey: "surface_color.warm", dark: "#2a1f17", light: "#f6e8da", accent: "#e0964a" },
  // 2026-08-07: five more hues so the five surfaces can be told apart at a glance.
  // The binding constraint when adding one is NOT the surface tints (they sit at
  // 11–13:1 against --fg in both themes) but the light theme's segmented control:
  // tokens.css derives --sel-fg = accent 55% + --fg on --sel-bg = accent 16% + white,
  // so a bright//saturated accent (vivid yellow, lime) drops that pair below AA. Every
  // accent below was measured through that derivation and clears the existing worst
  // case (warm, 4.82:1): teal 4.91 / rose 6.09 / pink 5.38 / indigo 5.86 / mono 5.25.
  { id: "teal", labelKey: "surface_color.teal", dark: "#12292b", light: "#d8eeee", accent: "#2fb3bd" },
  { id: "rose", labelKey: "surface_color.rose", dark: "#2e1a1e", light: "#fbe0e4", accent: "#e05770" },
  { id: "pink", labelKey: "surface_color.pink", dark: "#2c1a28", light: "#f9e0f2", accent: "#d872c8" },
  { id: "indigo", labelKey: "surface_color.indigo", dark: "#1c1e3a", light: "#e0e2fb", accent: "#7b7ff0" },
  // mono = achromatic: a surface that reads as "not the default" without adding a hue.
  { id: "mono", labelKey: "surface_color.mono", dark: "#242424", light: "#e6e6e6", accent: "#9a9a9a" },
];

// The four themeable surfaces (settings key + labels). Shared by DisplayTab and the
// TopBar appearance popover so "which surfaces are colorable" is defined once — `short` for
// the compact popover rows, `long` for the settings-tab rows.
export const SURFACE_TARGETS: { key: "topbarColor" | "leftpaneColor" | "viewerColor" | "chatColor" | "sharedColor" | "assistantColor"; shortKey: MsgKey; longKey: MsgKey }[] = [
  { key: "topbarColor", shortKey: "surface.topbar.short", longKey: "surface.topbar.long" },
  { key: "leftpaneColor", shortKey: "surface.leftpane.short", longKey: "surface.leftpane.long" },
  { key: "viewerColor", shortKey: "surface.viewer.short", longKey: "surface.viewer.long" },
  // chatColor drives the session mirror's (.mirrorview) --chat-bg / --chat-accent; labelled
  // "session" (「セッション」) so it isn't confused with the assistant chat. Key kept as
  // chatColor for backward-compat with persisted prefs.
  { key: "chatColor", shortKey: "surface.session.short", longKey: "surface.session.long" },
  // sharedColor is the same mechanism for a shared session (.shared-view). Giving the surface
  // where you read someone else's conversation a different color from your own mirror makes it
  // obvious at a glance which one you are looking at (docs/log/59).
  { key: "sharedColor", shortKey: "surface.shared.short", longKey: "surface.shared.long" },
  // assistantColor is the same surface mechanism for the assistant chat (.chatview).
  { key: "assistantColor", shortKey: "surface.assistant.short", longKey: "surface.assistant.long" },
];

// Resolve a surface color id to its value for the active theme (null = no override).
export function surfaceValue(id: string, theme: string): string | null {
  const c = SURFACE_COLORS.find((x) => x.id === id);
  if (!c) return null;
  return theme === "light" ? c.light : c.dark;
}

// The vivid accent that matches a surface color's family (theme-independent), used to
// tint the chat highlights so they follow the chosen palette instead of a fixed blue.
export function surfaceAccent(id: string): string | null {
  const c = SURFACE_COLORS.find((x) => x.id === id);
  return (c && c.accent) || null;
}

// Linear blend between two #rrggbb colors (t in 0..1 toward `to`).
function mixHex(from: string, to: string, t: number): string {
  const p = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const a = p(from);
  const b = p(to);
  const c = a.map((v, i) => Math.round(v + (b[i] - v) * t));
  return "#" + c.map((v) => v.toString(16).padStart(2, "0")).join("");
}

// Derive a region's surface background from a chosen surface color, for the given
// (region-effective) theme: lighten toward white in light, darken toward black in dark.
// null (default color) => no override, so the region falls back to the theme --bg in CSS.
// Used for the file viewer (applyTheme) and, per region theme, the mirror / assistant chat.
export function surfaceBg(id: string, theme: string): string | null {
  const c = surfaceValue(id, theme);
  if (!c) return null;
  return theme === "light" ? mixHex(c, "#ffffff", 0.45) : mixHex(c, "#000000", 0.34);
}

// Default row highlight per theme — mirrors --active-bg / --hover-bg in styles.css
// (:root dark, [data-theme="light"]). Used when no left-pane surface is chosen, so
// the highlight matches the prior fixed behavior.
const THEME_ROW_DEFAULTS = {
  dark: { active: "#20303a", hover: "#1b2730" },
  light: { active: "#d7e6fb", hover: "#eaf1fb" },
};

// shadeForSurface derives a row highlight from a left-pane surface color so the
// active/hover stays in the surface's color family: darken toward black in light
// mode, lighten toward white in dark mode.
function shadeForSurface(hex: string, theme: string, kind: string): string {
  const t =
    kind === "active" ? (theme === "light" ? 0.12 : 0.16) : theme === "light" ? 0.06 : 0.08;
  return mixHex(hex, theme === "light" ? "#000000" : "#ffffff", t);
}

// Settings for one read-aloud character (a ttsVoicePool value). Every field is optional; an
// absent field means the default behaviour.
export interface TtsCharConf {
  use?: boolean;
  style?: string;
  speed?: number;
}

export interface AgentLaunchDefault {
  model: string;
  effort: string;
  startMode: "normal" | "plan";
  /**
   * Launch with the permission prompts skipped (docs/log/76). true = the long-standing fleet
   * default (--dangerously-skip-permissions for claude, the equivalent flag for other kinds);
   * false = ask for approval on every tool run. The default is true, so behaviour is unchanged.
   *
   * The Agent reads this value in-process too (ui_prefs.go skipPermissionsPref), so the same
   * default applies to launches that do not go through the Console (MCP create_session,
   * scheduled runs, restart, fork). Ignored for kinds without caps.permissionChoice
   * (codex / opencode).
   */
  skipPermissions: boolean;
}

export type AgentLaunchDefaults = Record<string, AgentLaunchDefault>;

export interface Settings {
  termFont: string;
  termSize: number;
  /** CJK font: draws only the East Asian Width = Ambiguous characters (①②③ and the like)
   * with this font instead of the Latin code font (applyCjkFont / CJK_UNICODE_RANGE). One of
   * CJK_FONTS; CJK_FONT_AUTO leaves it to the OS, CJK_FONT_OFF keeps the Latin font. */
  cjkFont: string;
  viewerFont: string;
  viewerSize: number;
  chatFont: string;
  chatSize: number;
  lineNumbers: boolean;
  wrap: boolean;
  tabSize: number;
  minimap: boolean;
  // Wrap code blocks in the Markdown view (Doc/File preview, markdown inside chat) by default.
  // This is the initial state of the toggle at each block's bottom right
  // (features/viewer/MarkdownView.tsx); the per-block override it makes is not persisted.
  // Default true — wrapping usually reads better than horizontal scrolling.
  markdownCodeWrap: boolean;
  iconSet: string;
  theme: string;
  /** Main-area layout profile. Stored only on this device; each profile's
   * concrete layout remains per user, tenant and browser tab. */
  paneLayout: "split" | "tabs";
  // UI display language (docs/log/28 / ADR 0016), "ja" | "en". Unlike theme this is not
  // device-local: it syncs through the server so the language follows the person onto every
  // device. The default comes from the browser language, falling back to Japanese (detectLocale).
  locale: string;
  // Per-region base theme, independent of `theme`: "inherit" (default, follow the app),
  // "dark", or "light". Applied by scoping data-theme onto the region container.
  // mirrorTheme → .mirrorview (session mirror); assistantTheme → .chatview (assistant chat).
  mirrorTheme: string;
  // sharedTheme → .shared-view (the recipient side of a shared session, docs/log/59).
  sharedTheme: string;
  assistantTheme: string;
  topbarColor: string;
  leftpaneColor: string;
  viewerColor: string;
  // Surface color for the session mirror (chatColor), the recipient-side shared session
  // (sharedColor) and the assistant chat (assistantColor).
  chatColor: string;
  sharedColor: string;
  assistantColor: string;
  mirrorSend: string;
  // Default claude model for new sessions (launch dialog + repo launch). Usually a tier
  // alias (opus/sonnet/haiku), but may be a user-registered full id to pin a release.
  defaultModel: string;
  // Per-agent launch defaults. defaultModel remains as a migration mirror for older
  // Console/server prefs; new code reads this map for all three agent kinds.
  agentLaunchDefaults: AgentLaunchDefaults;
  // Models to hide (AgentsTab > each card > behaviour settings): kind → excluded model ids.
  // The motive is preventing billing accidents (on a Claude Team plan, Fable is charged as API
  // credit); model id namespaces differ per kind, so the exclusion list is kind-scoped too. The
  // Agent reads this key from ui-prefs, narrows both the picker and MCP list_models, and then
  // refuses outright a launch that names an excluded model (workspace/agent/model_deny.go).
  hiddenModels: Record<string, string[]>;
  // Claude Code OAuth has no account-aware catalog endpoint. Full model ids registered by
  // the user become durable choices in the Console picker and MCP list_models.
  claudeCustomModels: string[];
  // ON/OFF for the SESSION title suggestion (Settings > AI assist). Covers both the automatic
  // banner and the rename dialog's "ask AI for a suggestion" (「AIに提案してもらう」) button;
  // the chat side is assistantTitleSuggest and branch names are branchSuggestEnabled — each AI
  // assist feature gates on its own key now (it used to also silently gate branch names).
  // Default true so existing users get it without an explicit opt-in.
  autoTitleSuggest: boolean;
  // Global ON/OFF for session-to-session messaging (AgentsTab > Sessions, docs/log/58 /
  // ADR 0041). Not per-agent: it applies to every kind that gets af's own MCP server
  // (claude / codex / opencode / cursor / kiro / agy / copilot), and shell / ssm can
  // neither send nor receive by construction.
  // Default FALSE, unlike autoTitleSuggest: letting sessions type into each other
  // widens the injection surface (a session that read a poisoned repo can reach every
  // other session), so it has to be chosen rather than inherited on upgrade. The Agent
  // reads this key from ui-prefs to decide whether the session-side MCP server
  // advertises the two peer tools at all.
  peerMessaging: boolean;
  // How the opencode launch-model list is shaped (AgentsTab > opencode). One
  // OPENCODE_API_KEY opens both opencode.ai billing routes, so the same model shows up
  // twice: opencode/… (Zen, pay-per-request) and opencode-go/… (the Go subscription).
  // Which opencode.ai billing route this workspace means to use:
  //   "off"  — hard-disables opencode: overrides any stored key or OAuth login, even
  //            ones added later without switching the route back. For tenants whose
  //            security policy forbids opencode reaching a third party without an
  //            explicit, durable opt-in — the default ("zen", below) already behaves
  //            like this with nothing configured, but "off" is the deliberate,
  //            tamper-resistant version of that.
  //   "free" — the zero-auth free models only. Also makes opencode launchable with no
  //            credential at all, and the Agent stops injecting OPENCODE_API_KEY.
  //   "go"   — the subscription route (opencode-go/…). Needs an API key (measured: an
  //            account login alone does not grow the Go ids).
  //   "zen"  — pay-per-request (opencode/…), plus the Go ids when the account has both.
  // The Agent reads this from ui-prefs, so it shapes the MCP list_models an assistant
  // picks from as well as this picker. Legacy values migrate in normalizeSettings.
  opencodeCatalog: "off" | "free" | "go" | "zen";
  // Show the mirror's thinking block expanded from the start (kind-scoped; Settings > Agents >
  // each card > behaviour settings). Default off for every kind, i.e. collapsed as before and
  // opened by a click. How much thinking a backend emits varies enormously by kind and model, so
  // whether always-expanded reads well depends on the backend — hence a Record keyed by kind
  // like hiddenModels (an unset kind is false).
  expandThinking: Record<string, boolean>;
  // ON/OFF for the CHAT title suggestion (Settings > AI assist; the rename dialog's "ask AI for
  // a suggestion" (「AIに提案してもらう」) button — the assistant has no automatic banner). Split out of
  // autoTitleSuggest so sessions and chats gate independently; load()/hydrateUIPrefs
  // migrate an explicit legacy OFF, and the Agent falls back to autoTitleSuggest when
  // this key is absent.
  assistantTitleSuggest: boolean;
  // ON/OFF for the branch-name AI suggestion (Settings > AI assist; the worktree / branch
  // rename dialogs). Used to ride on autoTitleSuggest, which no label or note ever
  // said — one AI assist feature, one key. Migrates from autoTitleSuggest so an explicit
  // legacy OFF keeps branch names off too.
  branchSuggestEnabled: boolean;
  // ON/OFF for the File pane's AI edit suggestion (Settings > AI assist). Previously had no
  // setting at all and was always on; default true keeps that behaviour.
  editSuggestEnabled: boolean;
  // Forced output language for assistant chat: "auto" = follow the input language
  // (default), "ja" / "en" = always reply in that language (even for foreign-language
  // content). The Agent reads this key from ui-prefs and injects a language rule into the
  // chat system prompt (translate assistant is exempt). See docs/log/19.
  // CHAT ONLY. Read-aloud used to borrow this key to pick the TTS engine and voice;
  // that axis is now ttsLang — one key, one meaning.
  outputLanguage: string;
  // Assistant-CHAT backend priority (AssistantTab reordering): auto-selection takes the
  // first CONNECTED kind in this order (the Agent's preferredChatAgent, read live from
  // ui-prefs). Applies to builtin assistants' NEW conversations; user-defined assistants
  // keep their own per-assistant agent choice. Replaces the legacy single-pin
  // assistantAgent key — hydrateUIPrefs migrates a stored pin by promoting it to the
  // front, and the Agent normalizes partial/stale lists against its own default order.
  // Chat only. The one-shot helpers rank backends with aiAssistOrder.
  assistantAgentOrder: string[];
  // AI-assist generation backend priority (Settings > AI assist): the same normalization, ranked
  // separately from the chat because the two want opposite things — the chat wants the
  // strongest CLI, the one-shots run constantly and want the cheapest one that works.
  // Migrated from assistantAgentOrder (they were one list), so nothing changes on upgrade.
  aiAssistOrder: string[];
  // Per-backend models for builtin assistant conversations. "recommended" resolves
  // against the live catalog (and is shown with its current concrete result); empty
  // delegates to the CLI default. Explicit models are kept for every backend so
  // priority fallback never silently changes the requested model.
  assistantModels: Record<string, string>;
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
  aiShortModels: Record<string, string>;
  aiProseModels: Record<string, string>;
  // Auto turn on session reports (docs/log/30): when a session an af_write assistant
  // launched/steered reports back, the assistant runs one turn automatically to
  // process it. Default ON; the backend caps unattended turns at 10 per conversation
  // (reset by a user message) regardless of this switch.
  assistantAutoTurn: boolean;
  // Ceiling on unattended auto turns per conversation (reset whenever the user sends
  // a message). Backend clamps to [1, 50] — there is no unlimited mode; the clamp is
  // the structural runaway stop (docs/log/30).
  assistantAutoTurnLimit: number;
  // Model used for auto turns only (claude conversations only; empty = keep the conversation's
  // model). Processing a report is routine work, so routing it to a light model such as haiku
  // cuts token cost substantially. User turns and compaction summary turns stay on the
  // conversation's own model (chatAutoTurnModel on the Agent side).
  assistantAutoTurnModel: string;
  // Coalescing window for auto turns (seconds; 0 = immediate). A completion report does not
  // trigger an auto turn right away: every report arriving inside the window is processed in one
  // turn (report cards and notifications stay immediate). The Agent's chatAutoTurnDelay clamps
  // it to 600 seconds.
  assistantAutoTurnDelay: number;
  // Quiet completion reports: a normal completion report runs no auto turn (card and
  // notification only; the report rides along on the next turn). Failures, questions and plan
  // approvals behave as before. Default OFF.
  assistantQuietCompletion: boolean;
  // Auto-pilot (docs/log/30): when an instructed session stops at an AskUserQuestion, the
  // operator answers with the session's own recommendation; when it stops at plan
  // approval, the operator has another session review the plan, feeds back findings,
  // and approves once clean — sharing each decision in chat. Default OFF — acting in
  // the user's stead is consequential, so this is a deliberate opt-in; unclear or
  // destructive choices/plans still ask the user.
  assistantAutoPilot: boolean;
  // Auto-resume after an abort (docs/log/47): when a session's turn is CUT OFF before it answered by
  // something that clears on its own (dropped connection, temporary rate limit), the
  // operator nudges it to continue instead of only relaying to the user. Default ON —
  // unlike auto-pilot this makes no decision on the user's behalf, it just re-runs work
  // they already asked for; failures whose cause won't clear (usage limit, prompt too
  // long) are classified out and never auto-resumed.
  assistantAutoResume: boolean;
  // Auto-resume after a usage-limit reset (docs/log/47 §4-4, Agent settings > Claude): a claude
  // session cut off by its usage limit gets a one-shot schedule at the reset instant
  // that tells it to carry on (the agent books it with the CP scheduler, so a workspace
  // stopped in the meantime is woken for it). Default ON. Note this toggle governs the
  // RESUME only — dismissing the limit menu itself ("stop and wait", the no-charge
  // option) happens either way, because while it is up the session accepts nothing at all.
  rateLimitAutoResume: boolean;
  // Auto-resume a cut-off turn (docs/log/47 §4-6): when a claude turn dies on something that
  // clears on its own (dropped connection, temporary rate limit, stream idle timeout),
  // the agent itself re-sends 「続けて」 (continue) after a short backoff, up to maxAutoResumeAttempts.
  // Default ON. Unlike assistantAutoResume this needs no assistant conversation — it
  // applies to every claude TUI session — and the assistant only hears about the cut-off
  // once the retries are exhausted (which is what makes it cheaper in tokens, not just
  // in latency).
  claudeAbortAutoResume: boolean;
  // Preventive auto-compaction (docs/log/33 stage 4): when a chat's context is still at/above
  // the backend threshold (90%) as a new turn starts, summarize-and-hand-off first.
  // Default ON — the 80% notice gives a manual window before this fires.
  assistantAutoCompact: boolean;
  // Absolute token threshold for auto-compaction, OR'd with the relative 90% (docs/log/33
  // §5.1). A resume-driven chat re-reads the whole context every turn, so occupancy is directly
  // the price of a turn. The Agent's chatAutoCompactTokenThreshold clamps it to a 20k floor.
  assistantAutoCompactTokens: number;
  // Fetch limit for get_session_output (the tool an operator reads session output with), in KiB
  // and from the tail only. Tool results accumulate in the conversation context, so this limit
  // sets the price of every later turn. The Agent's mcpSessionOutputTail clamps it to
  // [4, 1024] KiB.
  assistantOutputTailKiB: number;
  // Per-SSM-host terminal color: host id → color id (see lib/termcolor SSM_HOST_COLORS).
  // Applied to a session's terminal background when it's created (sent as its color).
  ssmHostColors: Record<string, string>;
  // Per-SSM-host usage tally: host id → { count = launches, at = last launch epoch ms }. Orders
  // the quick-connect cards in the connection modal (most frequent first, ties broken by most
  // recent). Updated when startSsm succeeds; like ssmHostColors it lives only in client settings.
  ssmHostUsage: Record<string, { count: number; at: number }>;
  // ON/OFF for reply suggestions (quick replies, lib/quickReplies). Default ON.
  quickRepliesEnabled: boolean;
  // ON/OFF for reply suggestions v2, the sparkle button that generates from context with an
  // LLM. Default ON — tokens are spent only when it is pressed. Matches the default of the
  // Agent's replySuggestEnabled (ui-prefs).
  replySuggestEnabled: boolean;
  // Learned data for reply suggestions: normalized key → { text = display spelling,
  // count = times sent, at = last sent epoch ms }. Updated when send() succeeds; same shape as
  // ssmHostUsage and mirrored to the server, so it syncs across devices.
  quickReplies: Record<string, { text: string; count: number; at: number }>;
  // Suggestions the user removed from the reply-suggestion menu (normalized keys). Deleting the
  // learned entry alone is not enough — seeding or re-learning brings it back — so the hidden
  // marking is kept separately. Sending the same text again clears it.
  quickRepliesHidden: string[];
  // Pinned (always shown) suggestions. Holds display spellings in pin order rather than keys, so
  // a pin survives the learned data being pruned and the order stays the one the user chose
  // (lib/quickReplies).
  quickRepliesPinned: string[];
  // Branch-name template for launching from a work item (docs/log/80 P2). The placeholders are
  // {key} (PROJ-123 / issue-45) and {slug} (an ASCII slug from the title, empty for Japanese).
  // Empty string = the default, feature/{key}-{slug}. Clearing this does NOT fall back to the
  // server's temp/<slug>; clearing the branch field in the launch dialog is what does that.
  workItemBranchTemplate: string;
  // Read-aloud (TTS, docs/log/24 + ADR0013): speaks agent answers through VOICEVOX (Zundamon)
  // by calling the CP-native /api/tts/synthesize sentence by sentence (features/chat/tts.ts).
  ttsEnabled: boolean;
  // Provider choice. auto = Zundamon when the text is Japanese and the engine is ready, an
  // automatic fallback to Polly JP when the engine is absent (starting or disabled), and Polly
  // for anything not Japanese. The CP makes the final call, being the single source of truth for
  // engine readiness. An explicit "polly" always uses Polly.
  ttsProvider: string; // "auto" | "voicevox" | "polly"
  // Read-aloud language ("auto" | "ja" | "en"). auto follows the UI display language. It decides
  // the engine when ttsProvider is auto (en → Polly) and Polly's default VoiceId (en → Joanna).
  // Deliberately separate from the assistant's reply language (outputLanguage), which it used to
  // borrow: one key with two meanings meant that setting the chat's reply language to English
  // silently turned the mirror's read-aloud into Polly. The migration carries the old value
  // over, so nothing changes on upgrade.
  ttsLang: string;
  ttsVoiceVoicevox: string; // VOICEVOX speaker number ("3" = Zundamon, normal style)
  ttsVoicePolly: string; // Polly VoiceId ("Takumi" etc.); also used for the auto fallback
  ttsSpeed: number; // 0.5 to 2.0 (speedScale)
  // Output volume multiplier (0..1) for Zundamon (VOICEVOX). Zundamon is louder at source than
  // the other characters, so it is turned down slightly to match the other voices and the
  // notification sounds. Applied to the playback gain only when the Zundamon voice is used
  // (VV_CHAR_NAMES identifies its speakers); it is not a synthesis parameter, so it does not
  // invalidate the cache.
  ttsZundamonVolume: number;
  // How to play while the Console is a background tab or minimized (document.hidden), or focus
  // has moved to another window. mute = silent; quiet = lower the master volume (35% by default,
  // adjustable with ttsBackgroundVolume); normal = normal volume.
  ttsBackgroundPlayback: "mute" | "quiet" | "normal";
  // Master volume multiplier (0..1) for background playback (ttsBackgroundPlayback = "quiet"),
  // set with a slider. Returning to the Console ramps smoothly back to normal volume
  // (ttsMasterGain in ttsControl.ts).
  ttsBackgroundVolume: number;
  // Pan a pane's read-aloud in stereo to match the pane's current horizontal column. Even at the
  // far edges it is capped at ±70% rather than panned fully, which is easier to listen to.
  ttsStereoByPane: boolean;
  // Announce by voice when a background session answers or asks (docs/log/24 Tier1). A separate
  // axis from the chat's automatic read-aloud (ttsEnabled): it reads a short announcement
  // prefixed with the session name, through a serial queue. Only while the tab is visible,
  // because session monitoring stops on document.hidden.
  ttsSessionNotify: boolean;
  // Notify when a subscription usage window (5-hour / weekly) resets from a state where the
  // limit had actually been hit (app/usageResetNotify.ts). Uses the resetsAt on the WsBar usage
  // chip and raises a browser notification plus, when read-aloud is on, a short spoken "you can
  // resume now" message. A normal reset that never hit the limit stays silent, to avoid spam.
  // Detected reliably while the Console tab is open; a reset that happened while it was closed
  // notifies exactly once on the next open.
  usageResetNotify: boolean;
  // Convert English words to katakana before handing them to VOICEVOX (docs/log/24, the CP's
  // enkana preprocessing), so English is read plausibly in a Japanese accent without leaving
  // Zundamon's voice. It is a transliteration based on the CMU pronouncing dictionary, so a word
  // comes out phonetically (カフィー) rather than as the established loanword (コーヒー).
  ttsEnglishKana: boolean;
  // User reading dictionary (docs/log/24). One "spelling=reading" per line, applied as a literal
  // substitution to the text just before it is spoken (English, Japanese or symbols alike, and
  // regardless of whether enkana is on). Longer spellings match first. Empty = disabled. See
  // parse/applyUserDict in features/chat/ttsText.ts.
  ttsUserDict: string;
  // Synthesis cache ceiling, in total seconds of audio. Keeps audio for identical text under
  // identical synthesis conditions in memory so a repeat reading is instant
  // (features/chat/tts.ts). About 0.1MB per second as PCM. 0 = no cache.
  ttsCacheSec: number;
  // Mirror (chat): read a new answer aloud karaoke-style as soon as it arrives for the active
  // pane's session (features/mirror/turnTts.ts). Complements ttsSessionNotify, the short
  // announcement for sessions you are not watching — this one reads the body you are looking at.
  ttsAutoReadMirror: boolean;
  // Read aloud in every open pane, not just the active one (a sub-option of ttsAutoReadMirror).
  // New answers from all panes queue serially into a single playback. The same session open in
  // several panes is still read by one pane only (the ownership registration in
  // features/mirror/turnTts.ts). Combined with per-session voices (ttsVoicePerSession), the
  // voice tells you which pane an answer came from.
  ttsAutoReadAllPanes: boolean;
  // Read the settled work trace (the narration before the final answer) quietly. off = do not
  // read it; whisper and hushed select the matching VOICEVOX styles. A speaker without those
  // styles, and Polly, simply lower the volume of the same voice.
  ttsWorkRead: string; // "off" | "whisper" | "hushed"
  // Output volume multiplier (0..1) when the work trace is read quietly (ttsWorkRead != off),
  // set with a slider. Independent of the whisper/hushed style: this is the output gain that
  // makes it audibly quieter than the final answer.
  ttsWorkVolume: number;
  // Give each session its own voice (sessionVoiceOpts in features/chat/tts.ts). A hash of the
  // session name deterministically picks from the speaker pool (the standard VOICEVOX characters
  // / Polly's three voices), applied to the mirror's read-aloud and to session voice
  // notifications, so the voice identifies the session. The chat tab and the reader view keep
  // the selected speaker.
  ttsVoicePerSession: boolean;
  // Per-character settings (voiceCharacters / activeVoicePool in features/chat/tts.ts), keyed by
  // the engine's character name. use = include it in the session voice pool and the reader
  // view's choices (unset means the default pool: true only for the characters listed in
  // SESSION_VOICES in tts.ts); style = the base style (a speaker number, normal when unset);
  // speed = a per-character speed (the global ttsSpeed when unset). The list itself comes from
  // the VOICEVOX engine's live catalog (GET /api/tts/speakers).
  ttsVoicePool: Record<string, TtsCharConf>;
  // Mirror (chat): when the active pane's session stops for confirmation (AskUserQuestion, plan
  // approval, a permission request), read the question and its choices aloud. For a choice it
  // prefers the description (the tooltip body) over the display label (pendingSpeech in
  // features/chat/ttsText.ts).
  ttsReadPending: boolean;
  // In the mirror's automatic read-aloud, a long answer (roughly over 500 characters) is
  // summarized to two sentences by the assistant (a headless CLI) and that is read instead
  // (features/mirror/MirrorView.tsx). The full body is always available from the turn's
  // read-aloud button. A failed or timed-out summary falls back to reading the whole text.
  ttsSummaryRead: boolean;
  // Switch the emotion style by what the sentence says (emotionOpts in features/chat/tts.ts):
  // errors and failures are read in the ツンツン (prickly) styles, successes and completions in
  // the あまあま (sweet) ones. Only effective for speakers that have style variants (Zundamon,
  // Shikoku Metan, Kyushu Sora).
  ttsEmotion: boolean;
  // Read inline code (`...`) abbreviated (abbrevCode in features/chat/ttsText.ts). A hash becomes
  // its first two characters plus a filler word (なんとか and the like); camelCase and paths
  // become the first word plus a filler, with the last word appended when there are three or
  // more. Short tokens, anything containing a space or Japanese, and spellings covered by the
  // reading dictionary are left as they are. A bare hash without backticks (lowercase hex such
  // as f437e17, or a UUID) is treated the same way (isBareHash).
  ttsAbbrevCode: boolean;
  // When a particle (を・は・で・に・と) is immediately followed by a kanji, insert a comma so
  // it is read with a small pause (pauseParticles in features/chat/ttsText.ts) — a breath,
  // shorter than the beat a full stop gives.
  ttsParticlePause: boolean;
  // Show the reader view (docs/log/24) in vertical writing; default false = horizontal. Follows
  // the toggle in ReaderView.
  readerVertical: boolean;
  // Reader view voice. "" = keep the speaker from settings; "vv:<speaker>" = a VOICEVOX
  // character; "polly:<VoiceId>" = Polly. Follows the selection in the ReaderView header
  // (voiceChoiceOpts in features/chat/tts.ts resolves it into a TtsOptions override).
  readerVoice: string;
  // Reader view body font: a READER_FONTS value (明朝 = serif, the default; ゴシック = sans).
  // ReaderView passes it to the body as --reader-font, resolved by readerFontStack.
  readerFont: string;
  // Reader view body font size in px. ReaderView passes it to the body as --reader-size.
  readerSize: number;
  // User rebindings of keyboard shortcuts (docs/log/29 + ADR-0017). Key = a command id, or the
  // synthetic id of an app-reserved key (app.leader / app.palette / app.cheatsheet). Value = the
  // overriding code (the normalized chord string from chords.ts; "" disables it explicitly). An
  // unlisted key keeps its default. Only direct accelerators and reserved keys can be rebound —
  // leader sequences (p r and the like) are structure and stay fixed. Resolved by
  // effectiveCommands / boundChord in features/keys/bindings.ts. Synced across devices (not
  // DEVICE_LOCAL).
  keybindings: Record<string, string>;
  // Terminal input priority (docs/log/29). When ON, global app shortcuts are suppressed while
  // the terminal (xterm) has focus and keys pass straight through to it (Ctrl combinations go to
  // the terminal). Only the leader key (Ctrl/⌘+K by default, rebindable) survives, and every
  // command is still reachable from it through which-key or the palette. Default OFF, i.e. the
  // app captures.
  terminalPriority: boolean;
  // Working set definitions (docs/log/52 + ADR 0036): named sets of { working copies,
  // conversations, sessions without a repo } that narrow the left pane down to one piece of
  // work. The definitions sync across devices (server ui-prefs); which set is currently being
  // viewed is workingSetActive, which is device-local. Corrupt values and references to entities
  // that no longer exist are neutralized by normalize / the predicates in lib/workingSets.ts.
  workingSets: WorkingSet[];
  // Id of the working set currently shown ("" = all). Device-local like theme (DEVICE_LOCAL), so
  // watching work X on a PC and work Y on a tablet holds per device.
  workingSetActive: string;
  // Pass keys straight through to shell/SSM terminals (docs/log/29). When ON, and unlike
  // terminalPriority, every app shortcut is suppressed while a shell or ssm terminal has focus —
  // including the leader (Ctrl/⌘+K) and the palette (Ctrl/⌘+P) — so Ctrl+K (kill-line), Ctrl+P
  // (previous history) and the rest reach xterm/PTY unchanged: a pure terminal. Only shell/ssm
  // are affected; agent terminals behave as before. Default OFF. Click elsewhere to leave it.
  shellTermPassthrough: boolean;
}

// The pinned fallback model. Used as the seeded global default and as resolveModel's
// terminal fallback, so a new session always launches on a concrete tier — never claude's
// own release-varying default. Change here to move everyone's baseline tier.
export const DEFAULT_MODEL = "sonnet";

// Headless-chat backend kinds in the built-in priority order (mirrors the Agent's
// defaultHeadlessOrder; agy last — its free-plan quota is scarce). Display labels
// come from the agent registry (assistantName), not i18n.
export const ASSISTANT_AGENT_KINDS = ["claude", "codex", "opencode", "cursor", "agy"] as const;
export const ASSISTANT_RECOMMENDED_MODEL = "recommended";

// normalizeAssistantOrder folds any stored value into a total order over
// ASSISTANT_AGENT_KINDS: unknown entries and dupes drop, missing kinds append in
// the built-in order — same rules as the Agent's assistantAgentOrderPref, so what
// the UI shows is exactly what the backend will do.
export function normalizeAssistantOrder(v: unknown): string[] {
  const out: string[] = [];
  const push = (k: unknown) => {
    if (typeof k === "string" && (ASSISTANT_AGENT_KINDS as readonly string[]).includes(k) && !out.includes(k)) out.push(k);
  };
  if (Array.isArray(v)) v.forEach(push);
  ASSISTANT_AGENT_KINDS.forEach(push);
  return out;
}

// migrateAiAssistPrefs — carry-over for splitting AI assist generation out of the assistant
// (docs/log/84). It only fills in missing keys and never touches a value that is already there.
//
// Both load() (localStorage) and hydrateUIPrefs() (server prefs) call this same function.
// Writing the rule out twice invites fixing only one copy — the old assistantTitleSuggest
// migration really had been transcribed into two places.
//
// The principle is that an upgrade must not change behaviour. aiProseModels is the deliberate
// exception: the old assistantUtilityModels said "titles and suggestions" in both its name and
// its label, yet it also replaced the defaults for File pane edit suggestions and plan updates
// (the sonnet class). Someone who put haiku there meant it for short labels, as the name said,
// not to downgrade prose generation. The prose side goes back to the recommendation that fits
// its purpose — the point of this split.
export function migrateAiAssistPrefs(o: Record<string, unknown>): void {
  // Title suggestion moves to one key per feature. The old autoTitleSuggest covered sessions,
  // chats and branch names at once, so an explicit OFF is carried over to all three.
  if (typeof o.autoTitleSuggest === "boolean") {
    if (!("assistantTitleSuggest" in o)) o.assistantTitleSuggest = o.autoTitleSuggest;
    if (!("branchSuggestEnabled" in o)) o.branchSuggestEnabled = o.autoTitleSuggest;
  }
  // The CLI priority splits into a chat list and an assist-generation list. It was one list
  // before, so the assist side inherits that order verbatim; the split changes nothing anyone
  // would notice.
  if (!("aiAssistOrder" in o) && Array.isArray(o.assistantAgentOrder)) {
    o.aiAssistOrder = normalizeAssistantOrder(o.assistantAgentOrder);
  }
  // Both the short and the prose keys inherit the old utility models. Despite its name the old
  // key really did drive both, so this is the initial value that carries the pre-release
  // behaviour over unchanged. The point of splitting them is being able to change them
  // independently later, not moving one to a different model the moment this ships.
  if (o.assistantUtilityModels && typeof o.assistantUtilityModels === "object") {
    const legacy = o.assistantUtilityModels as Record<string, string>;
    if (!("aiShortModels" in o)) o.aiShortModels = { ...legacy };
    if (!("aiProseModels" in o)) o.aiProseModels = { ...legacy };
  }
  // The read-aloud language stops borrowing outputLanguage and gets its own key. It inherits the
  // borrowed value, so the current behaviour — reply language English means read-aloud goes to
  // Polly — is preserved. From now on either one can be changed alone.
  if (!("ttsLang" in o) && typeof o.outputLanguage === "string") o.ttsLang = o.outputLanguage;
}

const DEFAULT_AGENT_LAUNCH: AgentLaunchDefaults = {
  claude: { model: DEFAULT_MODEL, effort: "", startMode: "normal", skipPermissions: true },
  codex: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  cursor: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  copilot: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  kiro: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  agy: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  opencode: { model: "", effort: "", startMode: "normal", skipPermissions: true },
};

const DEFAULTS: Settings = {
  termFont: "Source Code Pro",
  termSize: 13,
  cjkFont: CJK_FONT_AUTO,
  viewerFont: "JetBrains Mono",
  viewerSize: 13,
  chatFont: "システム", // i18n-exempt: raw font value matched in fontStack
  chatSize: 14,
  lineNumbers: true,
  wrap: false,
  tabSize: 4,
  minimap: true,
  markdownCodeWrap: true,
  iconSet: "vscode",
  theme: "dark",
  paneLayout: "split",
  locale: detectLocale(),
  mirrorTheme: "inherit",
  sharedTheme: "inherit",
  assistantTheme: "inherit",
  topbarColor: "default",
  leftpaneColor: "default",
  viewerColor: "default",
  chatColor: "default",
  sharedColor: "default",
  assistantColor: "default",
  // Markdown mirror composer: "mod-enter" = Ctrl/⌘+Enter submits, Enter inserts a
  // newline (phone-friendly default); "enter" = Enter submits, Shift+Enter newline.
  mirrorSend: "mod-enter",
  defaultModel: DEFAULT_MODEL, // concrete tier (avoids claude's release-varying own pick)
  agentLaunchDefaults: DEFAULT_AGENT_LAUNCH,
  // New accounts exclude claude's Fable by default: on a Claude Team plan Fable is charged as
  // API credit, so this prevents an accidental pick from the very first launch. Existing users'
  // saved settings are not overwritten — load()/hydrateUIPrefs() apply defaults only to keys
  // that were never saved.
  hiddenModels: { claude: ["fable"] },
  claudeCustomModels: [],
  autoTitleSuggest: true,
  peerMessaging: false, // opt-in (docs/log/58 / ADR 0041) — not a surface to widen by default
  opencodeCatalog: "off",
  expandThinking: {},
  assistantTitleSuggest: true,
  branchSuggestEnabled: true,
  editSuggestEnabled: true,
  outputLanguage: "auto",
  assistantAgentOrder: [...ASSISTANT_AGENT_KINDS],
  aiAssistOrder: [...ASSISTANT_AGENT_KINDS],
  assistantModels: {
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
  },
  aiShortModels: {
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
  },
  aiProseModels: {
    claude: ASSISTANT_RECOMMENDED_MODEL,
    codex: ASSISTANT_RECOMMENDED_MODEL,
    opencode: ASSISTANT_RECOMMENDED_MODEL,
    cursor: ASSISTANT_RECOMMENDED_MODEL,
    agy: ASSISTANT_RECOMMENDED_MODEL,
  },
  assistantAutoTurn: true,
  assistantAutoTurnLimit: 10,
  assistantAutoTurnModel: "",
  assistantAutoTurnDelay: 60,
  assistantQuietCompletion: false,
  assistantAutoPilot: false,
  assistantAutoResume: true,
  rateLimitAutoResume: true,
  claudeAbortAutoResume: true,
  assistantAutoCompact: true,
  assistantAutoCompactTokens: 150000,
  assistantOutputTailKiB: 32,
  ssmHostColors: {},
  // Read-aloud defaults, i.e. the recommended setup. They are the same values the settings tab's
  // reset button restores (TTS_RESET), so new users — and existing users who never chose — start
  // here. Only read-aloud itself and the voice notification start OFF; everything else is the
  // setup that is comfortable once it is turned on (per-session voices, fast speed, auto-read
  // across all panes, confirmation questions, katakana readings, a 15-minute cache). TTS_RESET is
  // derived from these DEFAULTS by picking out the TTS keys, so the two can never disagree.
  ttsEnabled: false,
  ttsProvider: "auto",
  ttsLang: "auto",
  ttsVoiceVoicevox: "3", // normal style
  ttsVoicePolly: "Takumi",
  ttsSpeed: 1.25, // fast
  ttsZundamonVolume: 0.85, // Zundamon turned down slightly to match the other voices
  ttsBackgroundPlayback: "quiet",
  ttsBackgroundVolume: 0.35, // 35% in the background (the value of the former fixed HIDDEN_TTS_GAIN)
  ttsStereoByPane: true,
  ttsSessionNotify: false,
  usageResetNotify: true,
  ttsEnglishKana: true,
  ttsUserDict: "",
  ttsCacheSec: 900, // 15 minutes
  ttsAutoReadMirror: true,
  ttsAutoReadAllPanes: true,
  ttsWorkRead: "off",
  ttsWorkVolume: 0.5, // quiet work trace (between the former whisper 0.58 and hushed 0.3)
  ttsVoicePerSession: true,
  // {} = the 14 standard characters (SESSION_VOICES in tts.ts) are what sessions get assigned.
  // A new user and a reset both start here, so the character set is always the same 14. Extra
  // characters the engine offers must be ticked individually in the character list to be used.
  ttsVoicePool: {},
  ttsEmotion: false,
  ttsReadPending: true,
  ttsSummaryRead: false,
  ttsAbbrevCode: true,
  ttsParticlePause: true,
  readerVertical: false,
  readerVoice: "",
  readerFont: "明朝", // i18n-exempt: raw font value matched in fontStack
  readerSize: 17,
  keybindings: {},
  terminalPriority: false,
  shellTermPassthrough: false,
  ssmHostUsage: {},
  quickRepliesEnabled: true,
  replySuggestEnabled: true,
  quickReplies: {},
  quickRepliesHidden: [],
  quickRepliesPinned: [],
  workItemBranchTemplate: "",
  workingSets: [],
  workingSetActive: "",
};

// VOICEVOX Zundamon styles (speaker number → label), used by the speaker picker in the settings UI.
// i18n-exempt-start: VOICEVOX style names are proper nouns and stay untranslated (docs/log/28 §6.4)
export const VOICEVOX_ZUNDAMON: [string, string][] = [
  ["3", "ノーマル"],
  ["1", "あまあま"],
  ["7", "ツンツン"],
  ["5", "セクシー"],
  ["22", "ささやき"],
  ["38", "ヒソヒソ"],
];
// i18n-exempt-end

// TTS providers (docs/log/24 Phase 2). The CP decides what auto resolves to. Labels are i18n keys.
export const TTS_PROVIDERS: [string, MsgKey][] = [
  ["auto", "tts.provider_auto"],
  ["voicevox", "tts.provider_voicevox"],
  ["polly", "tts.provider_polly"],
];

// Read-aloud language (Settings > Read aloud). "auto" follows the UI display language — a
// different thing from the assistant's reply-language auto, which follows the input, so it has
// its own label key.
export const TTS_LANGS: [string, MsgKey][] = [
  ["auto", "tts.lang_auto"],
  ["ja", "tts.lang_ja"],
  ["en", "tts.lang_en"],
];

// Polly's Japanese neural speakers (VoiceId → i18n key).
export const TTS_POLLY_VOICES: [string, MsgKey][] = [
  ["Takumi", "tts.polly_takumi"],
  ["Kazuha", "tts.polly_kazuha"],
  ["Tomoko", "tts.polly_tomoko"],
];

// Synthesis cache ceilings (total seconds of audio → i18n key). PCM uses about 0.1MB per second.
export const TTS_CACHE_SIZES: [number, MsgKey][] = [
  [0, "tts.cache_none"],
  [300, "tts.cache_5m"],
  [900, "tts.cache_15m"],
  [1800, "tts.cache_30m"],
];

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

// Read-aloud speeds (speedScale). Labels are i18n keys.
export const TTS_SPEEDS: [number, MsgKey][] = [
  [0.75, "tts.speed_slow"],
  [1.0, "tts.speed_normal"],
  [1.25, "tts.speed_fast"],
  [1.5, "tts.speed_faster"],
];

export const TTS_WORK_READ_MODES: [string, MsgKey][] = [
  ["off", "tts.work_off"],
  ["whisper", "tts.work_whisper"],
  ["hushed", "tts.work_hushed"],
];

export const TTS_BACKGROUND_PLAYBACK_MODES: [string, MsgKey][] = [
  ["mute", "tts.bg_mute"],
  ["quiet", "tts.bg_quiet"],
  ["normal", "tts.bg_normal"],
];

// Assistant-chat output-language choices, shared by the settings UI. "auto" leaves the
// language to the user's input; "ja"/"en" force the reply language.
export const OUTPUT_LANGUAGES: [string, MsgKey][] = [
  ["auto", "out_lang.auto"],
  ["ja", "out_lang.ja"],
  ["en", "out_lang.en"],
];


// Mirror composer submit-key options, shared by the settings UI.
export const MIRROR_SEND_MODES: { id: string; labelKey: MsgKey }[] = [
  { id: "mod-enter", labelKey: "mirror_send.mod_enter" },
  { id: "enter", labelKey: "mirror_send.enter" },
];

// Claude model choices, shared by the launch dialog and the default-model setting. Only
// concrete tiers — the "default" (「既定」) / "" option, which deferred to claude's own
// release-varying default, was dropped on purpose so model selection stays deterministic. Each
// alias still tracks the newest model within its tier. Mirrored in Go as claude.Models()
// (workspace/agent/internal/agents/claude/models.go, served by /agents/claude/models
// for the MCP list_models) — keep the two lists in sync.
export const CLAUDE_MODELS: [string, string][] = [
  ["fable", "Fable"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

// applyCjkFont — rewrites the CJK_FAMILY @font-face from the current settings. Called from the
// same three places as applyTheme (first paint, on change, after a server hydrate). With
// CJK_FONT_OFF the rule is removed entirely; an undefined family name is simply skipped in the
// stack, which is what restores the previous appearance.
export function applyCjkFont(s: Settings): void {
  if (typeof document === "undefined") return;
  const id = "af-cjk-font";
  const prev = document.getElementById(id);
  const name = s.cjkFont || CJK_FONT_AUTO;
  if (name === CJK_FONT_OFF) {
    prev?.remove();
    return;
  }
  const families = name === CJK_FONT_AUTO ? GENERIC_CJK : [name, ...GENERIC_CJK];
  const src = families.map((f) => `local("${f}")`).join(", ");
  const face = (family: string, range: string) =>
    `@font-face{font-family:"${family}";src:${src};unicode-range:${range};}`;
  const css = face(CJK_FAMILY, CJK_UNICODE_RANGE) + face(CJK_FAMILY_PROSE, CJK_UNICODE_RANGE_PROSE);
  const el = (prev as HTMLStyleElement | null) ?? document.createElement("style");
  el.id = id;
  if (el.textContent !== css) el.textContent = css;
  if (!el.isConnected) document.head.appendChild(el);
}

// The Latin monospace stack — the bare form, with no CJK-first @font-face prepended.
function codeFontStack(name: string): string {
  if (!name || name === "システム等幅") { // i18n-exempt: raw font value matched in fontStack
    return 'ui-monospace, SFMono-Regular, Menlo, Consolas, "DejaVu Sans Mono", "Noto Sans Mono CJK JP", monospace';
  }
  return `"${name}", "Noto Sans Mono CJK JP", ui-monospace, Menlo, Consolas, monospace`;
}

// Build a CSS font-family stack for a chosen family, with CJK + generic fallbacks.
// The leading CJK_FAMILY is the unicode-range-limited @font-face that applyCjkFont() installs;
// it routes only East Asian Width = Ambiguous characters (①②③ and the like) to the CJK font.
export function fontStack(name: string): string {
  return `"${CJK_FAMILY}", ${codeFontStack(name)}`;
}

// Terminal stack — unlike fontStack it does not prepend CJK_FAMILY. xterm counts Ambiguous as
// one column wide (the CLI's own wrapping assumes the same), so drawing a CJK font's full-width
// glyph into one cell spills over into the neighbouring cell and breaks the line; the terminal
// alone keeps the Latin font's half-width glyphs.
export function termFontStack(name: string): string {
  return codeFontStack(name);
}

// Chat font stack — proportional by default. "システム"/"セリフ" map to sans/serif
// system stacks (with CJK fallbacks); any other name is a code font (monospace).
export function chatFontStack(name: string): string {
  if (!name || name === "システム") { // i18n-exempt: raw font value matched in fontStack
    // A gothic family, so prepending the gothic CJK_FAMILY keeps the letterforms consistent.
    // There are no columns here, so use the wider range (PROSE, up to ■ ○ ★). The serif branch
    // does not prepend it, to avoid gothic circled digits inside mincho body text.
    return `"${CJK_FAMILY_PROSE}", system-ui, -apple-system, "Hiragino Kaku Gothic ProN", "Noto Sans CJK JP", sans-serif`;
  }
  if (name === "セリフ") { // i18n-exempt: raw font value matched in fontStack
    return 'Georgia, "Times New Roman", "Hiragino Mincho ProN", "Noto Serif CJK JP", serif';
  }
  return fontStack(name);
}

// Reader font stack — Japanese prose. "ゴシック" = sans (gothic); anything else
// (default "明朝") = serif (mincho). Both list CJK families first with generic
// fallbacks so they render correctly where the OS lacks Hiragino/Yu.
export function readerFontStack(name: string): string {
  if (name === "ゴシック") { // i18n-exempt: raw font value matched in fontStack
    return '"Hiragino Kaku Gothic ProN", "Yu Gothic", "Noto Sans JP", "Noto Sans CJK JP", system-ui, sans-serif';
  }
  return '"Hiragino Mincho ProN", "Yu Mincho", "Noto Serif JP", "Noto Serif CJK JP", "Noto Serif", serif';
}

function load(): Settings {
  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || "{}");
    // Migrate the old ON/OFF setting to the three-way choice; an explicit new setting wins.
    if (!("ttsBackgroundPlayback" in saved) && typeof saved.ttsQuietWhenHidden === "boolean") {
      saved.ttsBackgroundPlayback = saved.ttsQuietWhenHidden ? "quiet" : "normal";
    }
    delete saved.ttsQuietWhenHidden;
    // Split of AI assist generation (one key per title-suggestion feature, two separate
    // priority/model lists, an independent read-aloud language). Runs the same function as
    // hydrateUIPrefs().
    migrateAiAssistPrefs(saved);
    const legacyClaudeModel = typeof saved.defaultModel === "string" ? saved.defaultModel : DEFAULT_MODEL;
    const rows = saved.agentLaunchDefaults && typeof saved.agentLaunchDefaults === "object"
      ? saved.agentLaunchDefaults
      : {};
    return {
      ...DEFAULTS,
      ...saved,
      claudeCustomModels: normalizeClaudeCustomModels(saved.claudeCustomModels),
      agentLaunchDefaults: normalizeAgentLaunchDefaults(rows, legacyClaudeModel),
      // Map the legacy model-list values (go-first / hide-zen / all) onto the three billing routes.
      opencodeCatalog: migrateOpencodeCatalog(saved.opencodeCatalog),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

export function normalizeClaudeCustomModels(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of value) {
    if (typeof raw !== "string") continue;
    const id = raw.trim();
    const key = id.toLowerCase();
    if (!/^claude-[a-z0-9][a-z0-9._\-[\]]*$/i.test(id) || seen.has(key) ||
        CLAUDE_MODELS.some(([alias]) => alias.toLowerCase() === key)) continue;
    seen.add(key);
    out.push(id);
  }
  return out;
}

export function normalizeAgentLaunchDefaults(rows: unknown, legacyClaudeModel = DEFAULT_MODEL): AgentLaunchDefaults {
  const src = rows && typeof rows === "object" ? rows as Record<string, Partial<AgentLaunchDefault>> : {};
  const out: AgentLaunchDefaults = {};
  // Drive the set of normalized kinds from DEFAULT_AGENT_LAUNCH — the single source of
  // truth — rather than a parallel literal list that silently drifts (copilot was
  // dropped that way, discarding its saved launch defaults on reload). Adding a kind's
  // default above now normalizes it automatically.
  for (const kind of Object.keys(DEFAULT_AGENT_LAUNCH)) {
    const base = DEFAULT_AGENT_LAUNCH[kind];
    const row = src[kind] || {};
    out[kind] = {
      model: typeof row.model === "string" ? row.model : kind === "claude" ? legacyClaudeModel : base.model,
      effort: typeof row.effort === "string" ? row.effort : base.effort,
      startMode: row.startMode === "plan" ? "plan" : "normal",
      // The default is to skip. Only an explicit false asks for approval: defaulting a missing
      // or corrupt value to "ask" would leave every session on a device that read older prefs
      // waiting for approval, and not changing behaviour is the premise of this feature.
      skipPermissions: row.skipPermissions !== false,
    };
  }
  return out;
}

export function agentLaunchDefault(s: Settings, kind: string): AgentLaunchDefault {
  return s.agentLaunchDefaults[kind] || { model: "", effort: "", startMode: "normal", skipPermissions: true };
}

// expandThinking is a kind's "expand thinking from the start" setting. Unset, an unknown kind,
// and a corrupt saved value (server ui-prefs are written by other Console versions too) all
// mean false, i.e. shown collapsed.
export function expandThinking(s: Settings, kind?: string | null): boolean {
  if (!kind) return false;
  return s.expandThinking?.[kind] === true;
}

let state = load();
const subs = new Set<() => void>();

// applyTheme writes the base theme + region color overrides onto <html>, so the
// whole CSS-variable palette switches. Called at load (before paint) and on change.
export function applyTheme(s: Settings): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const theme = s.theme === "light" ? "light" : "dark";
  root.dataset.theme = theme;
  const setVar = (name: string, val: string | null) =>
    val ? root.style.setProperty(name, val) : root.style.removeProperty(name);
  setVar("--topbar-bg", surfaceValue(s.topbarColor, theme));
  // Admin modal accent follows the chosen top-bar surface (buttons/tabs/bars retint via
  // .admin-surface). Null (no color chosen) → falls back to the CSS :root default blue.
  setVar("--topbar-accent", surfaceAccent(s.topbarColor));
  const lp = surfaceValue(s.leftpaneColor, theme);
  setVar("--leftpane-bg", lp);
  // Make the left-pane row highlight follow the chosen surface color (sessions /
  // repos / files active + hover). The .leftpane rule rebinds --active-bg /
  // --hover-bg to these. When no surface is chosen, fall back to the theme default
  // (read live so it tracks dark/light) so behavior is unchanged.
  if (lp) {
    setVar("--lp-active-bg", shadeForSurface(lp, theme, "active"));
    setVar("--lp-hover-bg", shadeForSurface(lp, theme, "hover"));
  } else {
    const d = THEME_ROW_DEFAULTS[theme];
    setVar("--lp-active-bg", d.active);
    setVar("--lp-hover-bg", d.hover);
  }
  // Left-pane accent = the chosen surface's matching accent, so the rail's focus
  // bar (active session / repo) and the worktree spine follow the palette instead
  // of the fixed app blue. Null (no surface chosen) → CSS falls back to --accent.
  setVar("--lp-accent", surfaceAccent(s.leftpaneColor));
  // File viewer background, derived from the chosen surface: lighter than the
  // surfaces in light theme (toward white), darker in dark theme (toward black).
  // Unset => theme --bg.
  setVar("--viewer-bg", surfaceBg(s.viewerColor, theme));
  // Viewer surface accent — the terminal pane shares --viewer-bg, so the terminal/chat toggle
  // rebinds --sel-accent to this. Null (no viewer color) → CSS falls back to topbar.
  setVar("--viewer-accent", surfaceAccent(s.viewerColor));
  // NOTE: the session mirror's --chat-bg/--chat-accent and the assistant chat's surface are
  // NOT set here anymore — each region owns them per its own theme (mirrorTheme/assistantTheme)
  // as inline vars on .mirrorview / .chatview, since those regions can differ from the app
  // theme. See MirrorView.tsx / ChatView.tsx (surfaceBg + surfaceAccent + effectiveTheme).
}

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

// applyLocale — pushes the current locale into the i18n runtime and updates <html lang> at
// runtime. Called from the same three places as applyTheme (before first paint, on change,
// after a server hydrate).
export function applyLocale(s: Settings): void {
  setLocale(s.locale);
  if (typeof document !== "undefined") document.documentElement.lang = s.locale;
}
applyTheme(state);
applyLocale(state);
applyCjkFont(state);

export function getSettings(): Settings {
  return state;
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

// Debounced mirror of the full settings object to the per-user server store. Best
// effort: if the workspace is stopped / agent unreachable, localStorage still holds it.
let saveTimer: ReturnType<typeof setTimeout> | null = null;
let saveInFlight: Promise<void> | null = null;

// Has the server's ui-prefs been read even once? Saving is a whole-object PUT where the last
// writer wins, so nothing may be sent before that read succeeds: an unhydrated state is only
// this browser's localStorage or DEFAULTS, not necessarily the user's settings, and PUTting it
// wipes the real server data (learned suggestions, pins, usage tallies, keybindings) with
// defaults. Damage: a GET returning 502 right after a workspace restart, together with an empty
// localStorage, let the learning from a single mirror reply PUT the full default set over the
// server, and it propagated from there to every other device.
let prefsLoaded = false;
// Local changes made before that read succeeded. Sent once, all together, as soon as it does.
let savePending = false;
let hydrateRetry: ReturnType<typeof setTimeout> | null = null;
let hydrateAttempt = 0;
// Left unfetched, changes would never sync, so retry a few times independently, starting from a
// short interval. Once those are used up, App's focus / visibilitychange (refreshUIPrefs) picks
// it up.
const HYDRATE_RETRY_MS = [2_000, 5_000, 15_000, 30_000, 60_000];

function scheduleHydrateRetry(): void {
  if (prefsLoaded || hydrateRetry || hydrateAttempt >= HYDRATE_RETRY_MS.length) return;
  const wait = HYDRATE_RETRY_MS[hydrateAttempt++];
  hydrateRetry = setTimeout(() => {
    hydrateRetry = null;
    void hydrateUIPrefs();
  }, wait);
}

// Called the moment the server's current values have been read. Pending local changes are sent
// out here, and not before.
function markPrefsLoaded(): void {
  prefsLoaded = true;
  hydrateAttempt = 0;
  if (hydrateRetry) {
    clearTimeout(hydrateRetry);
    hydrateRetry = null;
  }
  if (savePending) {
    savePending = false;
    scheduleServerSave();
  }
}

function scheduleServerSave(): void {
  if (!prefsLoaded) {
    savePending = true;
    scheduleHydrateRetry();
    return;
  }
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    saveTimer = null;
    // Device-local keys are not sent to the server; they never leave this device.
    saveInFlight = apiJSON("api/env/ui-prefs", "PUT", serverPrefs(state))
      // Swallowing a failure means believing the settings are synced when they are not. A
      // permanent failure such as exceeding the 64 KiB limit (413) is only visible here, so it
      // must always be logged.
      .then((res) => {
        if (res && typeof res === "object" && res.error) warnPrefsSaveFailed(res.error);
      })
      .catch((e) => warnPrefsSaveFailed(e))
      .finally(() => { saveInFlight = null; });
  }, 600);
}

function warnPrefsSaveFailed(err: unknown): void {
  try {
    console.warn("[settings] failed to save ui-prefs to the server (kept locally)", err);
  } catch {}
}

/** Exported for tests: has the server copy been read at least once this session? */
export const uiPrefsLoaded = (): boolean => prefsLoaded;

// Pull changes made on another device. Never race a local debounced/in-flight save:
// an older server snapshot must not overwrite the value this tab is currently writing.
export async function refreshUIPrefs(): Promise<void> {
  if (saveTimer || saveInFlight) return;
  hydrateAttempt = 0; // back in the foreground = conditions changed; restore the retry budget
  await hydrateUIPrefs();
}

// Device-local settings — kept only in localStorage, never sent to nor restored from the server.
// "Does this device make sound" and "how does this screen look" depend on where it is used
// (office or home, brightness, headphones), so making them follow the user onto every device
// gets in the way. Preferences such as voice, speed, dictionary and fonts stay cross-device as
// before (not DEVICE_LOCAL). For these keys it also removes the path where server-wins plus a
// dropped debounced save brings an OFF switch back to life.
const DEVICE_LOCAL = new Set<keyof Settings>([
  "paneLayout", // main-area layout (one user may want a different one per device)
  "ttsEnabled", // read-aloud ON/OFF
  "ttsSessionNotify", // voice notification ON/OFF
  "usageResetNotify", // limit-reset notification ON/OFF (does this device make the sound)
  "theme", // dark/light
  "mirrorTheme", // session mirror theme (per-device presentation)
  "sharedTheme", // shared session theme (per-device presentation)
  "assistantTheme", // assistant chat theme (per-device presentation)
  "topbarColor", // appearance palette (surface colors)
  "leftpaneColor",
  "viewerColor",
  "chatColor",
  "sharedColor",
  "assistantColor",
  "workingSetActive", // working set currently shown (docs/log/52 — a different one per device)
]);

/** Exported as a policy seam so persistence tests can pin which preferences
 * must never cross the device boundary. */
export const isDeviceLocalSetting = (key: keyof Settings): boolean => DEVICE_LOCAL.has(key);

// Accumulated data — unlike toggles and colors these settings build up over time and cannot be
// recovered once lost (learned reply suggestions, pins, SSM usage tallies, keybindings, working
// sets, the reading dictionary). Server-wins is fine for ordinary settings, but for these an
// empty server value must never overwrite a non-empty local one: an empty server value is far
// more often "not written yet / lost by accident" than "the user deleted it", and the accidental
// case propagates to every device and cannot be undone (see the prefsLoaded comment above).
// The cost: a "delete all" on device A does not reach device B, which still holds the entries
// and restores them. Between something you can delete again and something that never comes back,
// the latter is what gets protected.
const ACCUMULATED = new Set<keyof Settings>([
  "quickReplies",
  "quickRepliesPinned",
  "quickRepliesHidden",
  "ssmHostUsage",
  "ssmHostColors",
  "keybindings",
  "hiddenModels",
  "expandThinking",
  "claudeCustomModels",
  "workingSets",
  "ttsVoicePool",
  "ttsUserDict",
]);

/** Exported as a policy seam so tests can pin which preferences must never be
 * silently emptied by a server copy that has lost them. */
export const isAccumulatedSetting = (key: keyof Settings): boolean => ACCUMULATED.has(key);

// "Has no content" — null/undefined, empty string, empty array, empty object. Numbers and
// booleans are out of scope: 0 and false are chosen values, not lost ones.
export function isEmptyPref(v: unknown): boolean {
  if (v == null || v === "") return true;
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === "object") return Object.keys(v as object).length === 0;
  return false;
}

// serverPrefs is a shallow copy of only the settings that may be stored on the server, i.e.
// everything except the device-local keys.
function serverPrefs(s: Settings): Partial<Settings> {
  const out: Partial<Settings> = {};
  for (const k of Object.keys(s) as (keyof Settings)[]) {
    if (!isDeviceLocalSetting(k)) (out as any)[k] = s[k];
  }
  return out;
}

// hydrateUIPrefs pulls the server-stored prefs (if any) and merges the known keys
// over the local state, so a fresh browser inherits the user's settings. Called once
// at boot after the tenant is resolved (state.jsx). Server wins over localStorage —
// EXCEPT DEVICE_LOCAL keys, which stay whatever this browser's localStorage holds.
// migrateOpencodeCatalog maps the legacy menu-shaping values onto the billing-route
// choice. Kept exported for the load() path and the tests — the Agent applies the same
// rule server-side (opencode.CatalogPref), so the two never disagree.
export function migrateOpencodeCatalog(v: unknown): "off" | "free" | "go" | "zen" {
  if (v === "off" || v === "free" || v === "go" || v === "zen") return v;
  if (v === "hide-zen") return "go"; // hiding Zen means intending to use Go only
  if (v === "go-first" || v === "all") return "zen"; // both mean wanting to see both
  return "off"; // unset/unknown = disabled until explicitly chosen
}

export async function hydrateUIPrefs(): Promise<boolean> {
  let srv: any;
  try {
    srv = await api("api/env/ui-prefs");
  } catch {
    scheduleHydrateRetry();
    return false;
  }
  // api() does not throw on an HTTP error; it returns {error:{code:"http_502"}} — exactly what
  // the CP does while a workspace is starting. Mistaking "could not fetch" for "the server is
  // empty" lets the next save overwrite the server with defaults, so treat a failure as a
  // failure and leave prefsLoaded unset.
  if (!srv || typeof srv !== "object" || srv.error) {
    scheduleHydrateRetry();
    return false;
  }
  // Users whose server copy still holds only the old boolean are migrated once to the three-way choice.
  if (!("ttsBackgroundPlayback" in srv) && typeof srv.ttsQuietWhenHidden === "boolean") {
    srv.ttsBackgroundPlayback = srv.ttsQuietWhenHidden ? "quiet" : "normal";
  }
  // Users left with only the legacy single pin (assistantAgent) migrate to a priority list with
  // that pin promoted to the front (the same rule as the Agent's assistantAgentOrderPref).
  if (!("assistantAgentOrder" in srv) && typeof srv.assistantAgent === "string" && srv.assistantAgent !== "auto") {
    srv.assistantAgentOrder = normalizeAssistantOrder([srv.assistantAgent]);
  }
  // Split of AI assist generation (same function as load(), hence the same rules). Must run
  // AFTER the single-pin promotion: aiAssistOrder inherits from assistantAgentOrder, so calling
  // it first would leave a user who has only the legacy pin on the default order.
  migrateAiAssistPrefs(srv);
  // The opencode setting changed from "how to shape the list" to "which billing route to use".
  // Of the legacy values, "hide Zen" means intending to use Go only, so it maps to go; "Go
  // first" and "show all" mean wanting to see both, so they map to zen (the previous
  // appearance). Same rule as the Agent's CatalogPref. Only touch it when the server actually
  // has a value: writing a default into a missing key would let the merge below overwrite the
  // local choice, sending an unsaved selection back to zen every time.
  if ("opencodeCatalog" in srv) srv.opencodeCatalog = migrateOpencodeCatalog(srv.opencodeCatalog);
  let changed = false;
  const merged: Settings = { ...state };
  // Object/array values compared by reference would differ on every hydrate (the server response
  // is always a fresh object) — compare by value so `changed` is set only on a real change.
  const sameValue = (a: unknown, b: unknown): boolean =>
    a === b ||
    (typeof a === "object" && a !== null && typeof b === "object" && b !== null &&
      JSON.stringify(a) === JSON.stringify(b));
  // If the server lost accumulated data that this device still has, push it back instead of
  // taking the server's value (self-repair).
  let restore = false;
  for (const k of Object.keys(DEFAULTS)) {
    const key = k as keyof Settings;
    if (isDeviceLocalSetting(key)) continue; // device-local keys are never restored
    if (!(k in srv) || sameValue(srv[k], (merged as any)[k])) continue;
    if (isAccumulatedSetting(key) && isEmptyPref(srv[k]) && !isEmptyPref((merged as any)[k])) {
      restore = true;
      continue;
    }
    (merged as any)[k] = srv[k];
    changed = true;
  }
  const serverRows = srv.agentLaunchDefaults;
  const legacyClaudeModel = typeof srv.defaultModel === "string" ? srv.defaultModel : merged.defaultModel;
  // A server written by an older Console has only defaultModel. Do not let the
  // already-normalized local map mask that server-side value during migration.
  // Once the new map exists on the server it is authoritative for every agent.
  const rows = serverRows && typeof serverRows === "object"
    ? serverRows
    : {
        ...merged.agentLaunchDefaults,
        claude: { ...merged.agentLaunchDefaults.claude, model: legacyClaudeModel },
      };
  const normalized = normalizeAgentLaunchDefaults(rows, legacyClaudeModel);
  if (JSON.stringify(normalized) !== JSON.stringify(merged.agentLaunchDefaults)) {
    merged.agentLaunchDefaults = normalized;
    changed = true;
  }
  const customClaude = normalizeClaudeCustomModels(merged.claudeCustomModels);
  if (JSON.stringify(customClaude) !== JSON.stringify(merged.claudeCustomModels)) {
    merged.claudeCustomModels = customClaude;
    changed = true;
  }
  // Reaching here means the server's current values really were read. A pending save flows after
  // this: markPrefsLoaded → scheduleServerSave reads state 600ms later, so the `state = merged`
  // below takes effect first.
  markPrefsLoaded();
  // Accumulated data the server did not have is written back from here to restore it.
  if (restore) scheduleServerSave();
  if (!changed) return true;
  state = merged;
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  applyLocale(state);
  applyCjkFont(state);
  subs.forEach((fn) => fn());
  return true;
}

// Resync across an identity boundary (a tenant switch, or a user switch inside one tenant).
// ACCUMULATED is designed to protect a non-empty local copy against an empty server value, but
// that premise is about one user's several devices; it must NOT apply just after the owner
// changed while the previous owner's data is still in memory. If it did, the previous owner's
// accumulated data would be written back into the new owner's ui-prefs.json — exactly how
// working sets once leaked into another account. Iterating through isAccumulatedSetting instead
// of listing ACCUMULATED again means keys added to that set later are covered automatically,
// with no per-key fix. The returned Promise is fire-and-forget for the caller (App.tsx), but is
// awaitable so tests can wait for completion.
export function resyncAccumulatedForIdentitySwitch(): Promise<boolean> {
  // A pending save (the 600ms debounce) may be carrying the previous owner's values, so drop it
  // rather than send it under the new owner.
  if (saveTimer) {
    clearTimeout(saveTimer);
    saveTimer = null;
  }
  savePending = false;
  const cleared: Partial<Settings> = {};
  for (const k of Object.keys(DEFAULTS) as (keyof Settings)[]) {
    if (isAccumulatedSetting(k)) (cleared as any)[k] = DEFAULTS[k];
  }
  state = { ...state, ...cleared };
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  subs.forEach((fn) => fn());
  // Local is already empty, so the restore guard above does not fire and the new owner's server
  // values are taken as they are, empty or not.
  return hydrateUIPrefs();
}

// The generic signature ties key and value together in the type system, preventing mismatches
// such as passing a boolean for "theme".
export function setSetting<K extends keyof Settings>(key: K, value: Settings[K]): void {
  setSettings({ [key]: value } as Partial<Settings>);
}

// setSettings — update several keys at once, coalescing them into one render and one server
// save. Used where many keys change together, such as a reset: calling setSetting 17 times would
// run that many re-renders and debounced saves.
export function setSettings(patch: Partial<Settings>): void {
  state = { ...state, ...patch };
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  applyLocale(state);
  applyCjkFont(state);
  scheduleServerSave();
  subs.forEach((fn) => fn());
}

export function subscribe(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

export function useSettings(): Settings {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
