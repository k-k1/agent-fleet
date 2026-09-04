import type { MsgKey } from "./i18n/index.ts";

// Terminal background tints. Each session's terminal gets a subtle dark background so
// the agent kind (and, for SSM, the target host) is recognizable at a glance without
// hurting readability. The tint is a mostly-black base with a small amount of a vivid
// hue mixed in.

// Vivid base color per kind — mirrors the --kind-* values in tokens.css (the dark-theme
// ones). The terminal background is always dark (TERM_BASE) regardless of theme, so the dark
// values are what gets mixed in. Change the palette in tokens.css and this must be updated
// with it (kind-color-css-checklist). SSM uses a per-host
// color instead (its kind base is only the fallback when a session carries no explicit
// host color).
const KIND_BASE: Record<string, string> = {
  claude: "#e0a45e",
  codex: "#4ec97a",
  cursor: "#d96ba1",
  agy: "#4285f4",
  kiro: "#a371f7",
  copilot: "#7d8590",
  opencode: "#aab4be",
  shell: "#46c9d0",
  ssm: "#6d8bf5",
};

// SSM host color palette offered in settings. The terminal shows a dark tint of the
// chosen hue, so prod / staging / … hosts are visually distinct. "auto" derives a
// stable hue from the host name.
export const SSM_HOST_COLORS: { id: string; labelKey: MsgKey; base: string }[] = [
  { id: "auto", labelKey: "color.auto", base: "" },
  { id: "red", labelKey: "color.red", base: "#e0574e" },
  { id: "orange", labelKey: "color.orange", base: "#e8913a" },
  { id: "yellow", labelKey: "color.yellow", base: "#d8b83a" },
  { id: "green", labelKey: "color.green", base: "#4ec97a" },
  { id: "teal", labelKey: "color.teal", base: "#46c9d0" },
  { id: "blue", labelKey: "color.blue", base: "#6d8bf5" },
  { id: "purple", labelKey: "color.purple", base: "#b07cf2" },
  { id: "pink", labelKey: "color.pink", base: "#e069b0" },
  { id: "gray", labelKey: "color.gray", base: "#8a94a6" },
];

// The default (untinted) terminal background, and how much hue to mix in.
export const TERM_BASE = "#1e1e1e";
const TINT = 0.13;

// Linear blend between two #rrggbb colors (t in 0..1 toward `to`).
function mixHex(from: string, to: string, t: number): string {
  const p = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const a = p(from);
  const b = p(to);
  const c = a.map((v, i) => Math.round(v + (b[i] - v) * t));
  return "#" + c.map((v) => v.toString(16).padStart(2, "0")).join("");
}

// termBackground: the pane's terminal background — a dark tint. `override` (a hex, e.g.
// an SSM session's stored host color) wins; otherwise the kind's base color. When
// neither resolves, the plain default background.
export function termBackground(kind: string | undefined, override?: string | null): string {
  const base = (override && override.trim()) || KIND_BASE[kind || ""] || "";
  return base ? mixHex(TERM_BASE, base, TINT) : TERM_BASE;
}

// autoHostColor: a deterministic palette hue for an SSM host that has no explicit color
// chosen, keyed by a stable string (host id / alias) so it stays the same across runs.
export function autoHostColor(key: string): string {
  const pal = SSM_HOST_COLORS.filter((c) => c.base);
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return pal[h % pal.length].base;
}

// hostColorBase resolves a host's chosen color id (from settings) to its base hex,
// falling back to the deterministic auto hue.
export function hostColorBase(colorId: string | undefined, hostKey: string): string {
  const c = SSM_HOST_COLORS.find((x) => x.id === colorId);
  return c && c.base ? c.base : autoHostColor(hostKey);
}
