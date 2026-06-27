import { useSyncExternalStore } from "react";

// Display settings (font / file-viewer options), persisted in localStorage and
// shared across React (useSettings) and non-React code (term.js via getSettings +
// subscribe). Terminal and viewer fonts are independent — they default to
// different families so they're visibly distinct out of the box.

const KEY = "af-display-settings";

export const CODE_FONTS = [
  "Source Code Pro",
  "JetBrains Mono",
  "Fira Code",
  "IBM Plex Mono",
  "システム等幅",
];

const DEFAULTS = {
  termFont: "Source Code Pro",
  termSize: 13,
  viewerFont: "JetBrains Mono",
  viewerSize: 13,
  lineNumbers: true,
  wrap: false,
  tabSize: 4,
};

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

export function getSettings() {
  return state;
}

export function setSetting(key, value) {
  state = { ...state, [key]: value };
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  subs.forEach((fn) => fn());
}

export function subscribe(fn) {
  subs.add(fn);
  return () => subs.delete(fn);
}

export function useSettings() {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
