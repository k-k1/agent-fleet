// Label badge colours for the work item rail and detail panel (#993).
//
// Pure, so the contrast rule is testable without a DOM: the badge is only as good as the
// foreground it picks, and a wrong pick is unreadable text in one theme or the other.

/** The fill and text colour of one label badge, both "#rrggbb". */
export interface LabelBadgeColors {
  bg: string;
  fg: string;
}

const HEX6 = /^[0-9a-f]{6}$/i;

/** A tracker colour as "rrggbb" (GitHub's form, no "#"), or "" when it is not one. The value
 * ends up in an inline style, so anything else is refused here rather than passed through. */
export function normalizeHex(v: unknown): string {
  if (typeof v !== "string") return "";
  const s = v.startsWith("#") ? v.slice(1) : v;
  return HEX6.test(s) ? s.toLowerCase() : "";
}

function channel(c: number): number {
  const s = c / 255;
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

/** WCAG 2 relative luminance of "rrggbb". */
export function luminance(hex: string): number {
  const n = parseInt(hex, 16);
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255);
}

/** WCAG 2 contrast ratio between two "rrggbb" colours (1..21). */
export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Black or white, whichever reads better on `bg`. One of the two always clears 4.5:1 against
 * any fill, so this never has to fall back to something unreadable. */
export function readableForeground(bg: string): string {
  return contrastRatio(bg, "000000") >= contrastRatio(bg, "ffffff") ? "000000" : "ffffff";
}

function hslToHex(h: number, s: number, l: number): string {
  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return [f(0), f(8), f(4)].map((x) => Math.round(x * 255).toString(16).padStart(2, "0")).join("");
}

/** A stable colour for a label the tracker gave none (Jira, a row cached before colours were
 * carried). Only the hue varies, so two different names stay apart while none of them is
 * lighter or darker than the rest; the mid lightness keeps the fill visible on both themes. */
export function derivedLabelHex(name: string): string {
  // FNV-1a: the same name must get the same colour on every row and every reload.
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return hslToHex((h >>> 0) % 360, 0.55, 0.45);
}

/** The badge colours for `name`, from the tracker's colour when there is a valid one. */
export function labelBadgeColors(name: string, trackerHex?: string): LabelBadgeColors {
  const bg = normalizeHex(trackerHex) || derivedLabelHex(name);
  return { bg: `#${bg}`, fg: `#${readableForeground(bg)}` };
}
