// Label badge colours for the work item rail and detail panel (#993). Pure, so the choice of
// colour is testable without a DOM; how the colour is drawn lives in workitems.css.

const HEX6 = /^[0-9a-f]{6}$/i;

/** A tracker colour as "rrggbb" (GitHub's form, no "#"), or "" when it is not one. The value
 * ends up in an inline style, so anything else is refused here rather than passed through. */
export function normalizeHex(v: unknown): string {
  if (typeof v !== "string") return "";
  const s = v.startsWith("#") ? v.slice(1) : v;
  return HEX6.test(s) ? s.toLowerCase() : "";
}

function hslToHex(h: number, s: number, l: number): string {
  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return [f(0), f(8), f(4)].map((x) => Math.round(x * 255).toString(16).padStart(2, "0")).join("");
}

/** A stable colour for a label the tracker gave none (Jira, a row cached before colours were
 * carried). Only the hue varies, so two different names stay apart while none of them is
 * lighter or darker than the rest; the mid lightness keeps the edge visible on both themes. */
export function derivedLabelHex(name: string): string {
  // FNV-1a: the same name must get the same colour on every row and every reload.
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return hslToHex((h >>> 0) % 360, 0.55, 0.45);
}

/** The badge colour for `name` as "#rrggbb": the tracker's own when it is valid, else derived. */
export function labelColor(name: string, trackerHex?: string): string {
  return `#${normalizeHex(trackerHex) || derivedLabelHex(name)}`;
}
