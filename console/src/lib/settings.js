import { useSyncExternalStore } from "react";
import { api, apiJSON } from "../api.js";

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
  "システム等幅",
];

// File-icon sets (brand SVGs under assets/fileicons/<id>/). value = asset subdir.
export const ICON_SETS = [
  { id: "vscode", label: "VS Code Icons（カラー）" },
  { id: "material", label: "Material（カラー）" },
  { id: "devicon", label: "Devicon（カラー）" },
  { id: "seti", label: "Seti（単色・タイプ別着色）" },
];

// Base UI theme.
export const THEMES = [
  { id: "dark", label: "ダーク" },
  { id: "light", label: "ライト" },
];

// Surface (top bar / left pane) background choices. Each color has a per-theme tint
// so it always contrasts with the theme's text color. "default" = theme default.
export const SURFACE_COLORS = [
  { id: "default", label: "デフォルト", dark: null, light: null },
  { id: "slate", label: "スレート", dark: "#1b2733", light: "#e2e8f0" },
  { id: "blue", label: "ブルー", dark: "#16263f", light: "#dbe7fb" },
  { id: "green", label: "グリーン", dark: "#15291f", light: "#dcefe0" },
  { id: "purple", label: "パープル", dark: "#241a33", light: "#ece0fb" },
  { id: "warm", label: "ウォーム", dark: "#2a1f17", light: "#f6e8da" },
];

// Resolve a surface color id to its value for the active theme (null = no override).
export function surfaceValue(id, theme) {
  const c = SURFACE_COLORS.find((x) => x.id === id);
  if (!c) return null;
  return theme === "light" ? c.light : c.dark;
}

// Linear blend between two #rrggbb colors (t in 0..1 toward `to`).
function mixHex(from, to, t) {
  const p = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const a = p(from);
  const b = p(to);
  const c = a.map((v, i) => Math.round(v + (b[i] - v) * t));
  return "#" + c.map((v) => v.toString(16).padStart(2, "0")).join("");
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
function shadeForSurface(hex, theme, kind) {
  const t =
    kind === "active" ? (theme === "light" ? 0.12 : 0.16) : theme === "light" ? 0.06 : 0.08;
  return mixHex(hex, theme === "light" ? "#000000" : "#ffffff", t);
}

const DEFAULTS = {
  termFont: "Source Code Pro",
  termSize: 13,
  viewerFont: "JetBrains Mono",
  viewerSize: 13,
  lineNumbers: true,
  wrap: false,
  tabSize: 4,
  minimap: true,
  iconSet: "vscode",
  theme: "dark",
  topbarColor: "default",
  leftpaneColor: "default",
  viewerColor: "default",
  // Markdown mirror composer: "mod-enter" = Ctrl/⌘+Enter submits, Enter inserts a
  // newline (phone-friendly default); "enter" = Enter submits, Shift+Enter newline.
  mirrorSend: "mod-enter",
};

// Mirror composer submit-key options, shared by the settings UI.
export const MIRROR_SEND_MODES = [
  { id: "mod-enter", label: "Ctrl+Enter で送信" },
  { id: "enter", label: "Enter で送信" },
];

// Build a CSS font-family stack for a chosen family, with CJK + generic fallbacks.
export function fontStack(name) {
  if (!name || name === "システム等幅") {
    return 'ui-monospace, SFMono-Regular, Menlo, Consolas, "DejaVu Sans Mono", "Noto Sans Mono CJK JP", monospace';
  }
  return `"${name}", "Noto Sans Mono CJK JP", ui-monospace, Menlo, Consolas, monospace`;
}

function load() {
  try {
    return { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) || "{}") };
  } catch {
    return { ...DEFAULTS };
  }
}

let state = load();
const subs = new Set();

// applyTheme writes the base theme + region color overrides onto <html>, so the
// whole CSS-variable palette switches. Called at load (before paint) and on change.
export function applyTheme(s) {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const theme = s.theme === "light" ? "light" : "dark";
  root.dataset.theme = theme;
  const setVar = (name, val) =>
    val ? root.style.setProperty(name, val) : root.style.removeProperty(name);
  setVar("--topbar-bg", surfaceValue(s.topbarColor, theme));
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
  // File viewer background, derived from the chosen surface: lighter than the
  // surfaces in light theme (toward white), darker in dark theme (toward black).
  // Unset => theme --bg.
  const vw = surfaceValue(s.viewerColor, theme);
  setVar("--viewer-bg", vw ? (theme === "light" ? mixHex(vw, "#ffffff", 0.45) : mixHex(vw, "#000000", 0.34)) : null);
}
applyTheme(state);

export function getSettings() {
  return state;
}

// Debounced mirror of the full settings object to the per-user server store. Best
// effort: if the workspace is stopped / agent unreachable, localStorage still holds it.
let saveTimer = null;
function scheduleServerSave() {
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    apiJSON("api/env/ui-prefs", "PUT", state).catch(() => {});
  }, 600);
}

// hydrateUIPrefs pulls the server-stored prefs (if any) and merges the known keys
// over the local state, so a fresh browser inherits the user's settings. Called once
// at boot after the tenant is resolved (state.jsx). Server wins over localStorage.
export async function hydrateUIPrefs() {
  let srv;
  try {
    srv = await api("api/env/ui-prefs");
  } catch {
    return;
  }
  if (!srv || typeof srv !== "object" || srv.error) return;
  let changed = false;
  const merged = { ...state };
  for (const k of Object.keys(DEFAULTS)) {
    if (k in srv && srv[k] !== merged[k]) {
      merged[k] = srv[k];
      changed = true;
    }
  }
  if (!changed) return;
  state = merged;
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  subs.forEach((fn) => fn());
}

export function setSetting(key, value) {
  state = { ...state, [key]: value };
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  scheduleServerSave();
  subs.forEach((fn) => fn());
}

export function subscribe(fn) {
  subs.add(fn);
  return () => subs.delete(fn);
}

export function useSettings() {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
