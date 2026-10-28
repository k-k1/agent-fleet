// The choices each preference offers (fonts, themes, surface colors, read-aloud voices and
// modes, model tiers, child limits, archive periods), shared by the settings tabs and the TopBar
// popover. Re-exported from lib/settings.ts, which is where callers import them from.
//
// This module imports nothing from features/ (ADR 0124: lib/ is the lower layer).
import type { MsgKey } from "../i18n/index.ts";
import { AGENTS, inListOrder } from "../../agents/registry.ts";
import { SESSION_KINDS, type SessionKind } from "../../types/session.ts";

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

// The pinned fallback model. Used as the seeded global default and as resolveModel's
// terminal fallback, so a new session always launches on a concrete tier — never claude's
// own release-varying default. Change here to move everyone's baseline tier.
export const DEFAULT_MODEL = "sonnet";

// Headless-chat backend kinds (caps.headlessChat) in the built-in priority order, the
// registry's "assistant" rank (mirrors the Agent's DefaultHeadlessOrder; agy late — its
// free-plan quota is scarce). Display labels come from the registry (assistantName), not i18n.
export const ASSISTANT_AGENT_KINDS: readonly SessionKind[] = inListOrder(
  SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat),
  "assistant",
);
export const ASSISTANT_RECOMMENDED_MODEL = "recommended";

// The child limits a user may pick (ADR 0073 decision 6). Keep the last entry equal to the
// Agent's session.SpawnChildLimitMax: a choice past it is silently answered with the DEFAULT,
// not with the ceiling, so an option this list offered and the Agent refused would set the
// budget lower than the user asked for rather than higher.
export const SPAWN_CHILD_LIMITS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const;

// The stopped-session archive periods a user may pick, in days (ADR 0097). Must equal the Agent's
// session.StoppedArchiveDayChoices: the Agent reads any other number as "not set" and falls back
// to the deployment default, so a button missing there would silently do nothing.
export const STOPPED_ARCHIVE_DAYS = [1, 3, 7, 14, 30] as const;
// "Do not auto-archive" — the Agent's session.StoppedArchiveNever.
export const STOPPED_ARCHIVE_NEVER = -1;

// stoppedArchiveChoice — which button a stored value lights. Mirrors the Agent's
// NormalizeStoppedArchiveDays: a value no button produces (a hand-edited ui-prefs, an imported
// bundle, another Console version) is applied as Default, so it is shown as Default too rather
// than as no choice at all.
export function stoppedArchiveChoice(v: number): number {
  return (STOPPED_ARCHIVE_DAYS as readonly number[]).includes(v) || v === STOPPED_ARCHIVE_NEVER ? v : 0;
}

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
