// What settings do to the page: the theme and surface colors on <html>, the CJK @font-face, the
// font stacks, and the UI locale. Re-exported from lib/settings.ts, which calls the three apply
// functions before first paint, on every change and after a server hydrate.
import { setLocale } from "../i18n/index.ts";
import {
  CJK_FAMILY,
  CJK_FAMILY_PROSE,
  CJK_FONT_AUTO,
  CJK_FONT_OFF,
  CJK_UNICODE_RANGE,
  CJK_UNICODE_RANGE_PROSE,
  SURFACE_COLORS,
} from "./options.ts";
import type { Settings } from "./prefs.ts";

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

// Resolve a per-region theme preference against the app theme, to a concrete "light"/"dark".
export function effectiveTheme(pref: string, base: string): "light" | "dark" {
  if (pref === "dark" || pref === "light") return pref;
  return base === "light" ? "light" : "dark";
}

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

// applyLocale — pushes the current locale into the i18n runtime and updates <html lang> at
// runtime. Called from the same three places as applyTheme (before first paint, on change,
// after a server hydrate).
export function applyLocale(s: Settings): void {
  setLocale(s.locale);
  if (typeof document !== "undefined") document.documentElement.lang = s.locale;
}
