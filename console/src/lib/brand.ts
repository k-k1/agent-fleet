// Per-deployment branding, so prod / staging / a laptop running the same image are told
// apart at a glance. The Control Plane injects it into index.html ahead of the bundle
// (control-plane/brand.go) — it is NOT a build-time constant, because one console bundle
// ships to every deployment.
//
// Absent on an unbranded deployment, in the tests, and in any shell served untouched,
// which is why every read below falls back to what the shipped files say.
type InjectedBrand = { label?: string; color?: string; name?: string };

const injected = (globalThis as { __AF_BRAND?: InjectedBrand }).__AF_BRAND;

/** Short deployment label ("dev", "staging"). Empty on an unbranded deployment. */
export const brandLabel = injected?.label?.trim() || "";

/** The product name with the label already folded in — "[dev] Agent Fleet". */
export const brandName = injected?.name?.trim() || "Agent Fleet";

/** The deployment's accent colour; matches the favicon and the PWA theme colour. */
export const brandColor = injected?.color?.trim() || "#149ba7";

/** What the browser tab says when no pane has claimed the title. */
export const appTitle = `${brandName} — Console`;

/**
 * Ink for text sitting ON brandColor. The palette spans light (orange) to dark (violet),
 * and white is NOT readable across it — white on the shipped teal is 3.35:1, under the
 * 4.5:1 small-text bar — so the side with more contrast wins, per colour.
 */
export const brandInk = readableInk(brandColor);

function readableInk(hex: string): string {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return "#fff";
  const n = parseInt(m[1], 16);
  const chan = (c: number) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  const l = 0.2126 * chan((n >> 16) & 255) + 0.7152 * chan((n >> 8) & 255) + 0.0722 * chan(n & 255);
  const onWhite = 1.05 / (l + 0.05);
  const onBlack = (l + 0.05) / 0.05;
  // Pure black, not the usual off-black ink: on blue and red the softer tone measures
  // 4.03:1 and 4.41:1, i.e. under the bar the whole helper exists to clear.
  return onWhite >= onBlack ? "#fff" : "#000";
}
