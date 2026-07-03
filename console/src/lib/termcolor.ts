// Terminal background tints. Each session's terminal gets a subtle dark background so
// the agent kind (and, for SSM, the target host) is recognizable at a glance without
// hurting readability. The tint is a mostly-black base with a small amount of a vivid
// hue mixed in.

// Vivid base color per kind — mirrors the badge colors in styles.css (.kind-tag.kind-*).
// Keep in sync if the badge palette changes. SSM uses a per-host color instead (its
// kind base is only the fallback when a session carries no explicit host color).
const KIND_BASE: Record<string, string> = {
  claude: "#e0a45e",
  codex: "#4ec97a",
  opencode: "#b07cf2",
  shell: "#46c9d0",
  ssm: "#6d8bf5",
};

// SSM host color palette offered in settings. The terminal shows a dark tint of the
// chosen hue, so prod / staging / … hosts are visually distinct. "auto" derives a
// stable hue from the host name.
export const SSM_HOST_COLORS: { id: string; label: string; base: string }[] = [
  { id: "auto", label: "自動", base: "" },
  { id: "red", label: "レッド", base: "#e0574e" },
  { id: "orange", label: "オレンジ", base: "#e8913a" },
  { id: "yellow", label: "イエロー", base: "#d8b83a" },
  { id: "green", label: "グリーン", base: "#4ec97a" },
  { id: "teal", label: "ティール", base: "#46c9d0" },
  { id: "blue", label: "ブルー", base: "#6d8bf5" },
  { id: "purple", label: "パープル", base: "#b07cf2" },
  { id: "pink", label: "ピンク", base: "#e069b0" },
  { id: "gray", label: "グレー", base: "#8a94a6" },
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
