// Per-deployment branding, so prod / staging / a laptop running the same image are told
// apart at a glance. The Control Plane injects it into index.html ahead of the bundle
// (control-plane/brand.go) — it is NOT a build-time constant, because one console bundle
// ships to every deployment.
//
// Absent on an unbranded deployment, in the tests, and in any shell served untouched,
// which is why every read below falls back to what the shipped files say.
import { create } from "zustand";

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

export function readableInk(hex: string): string {
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

// --- the live value -------------------------------------------------------------------
//
// The constants above are the value this document was SERVED with. An administrator who
// changes the colour in the Admin modal is looking at the top bar while they do it, so the
// chip has to move with them — being told to reload after picking a colour reads as "it
// did not work". Everything the browser owns (the tab title, the favicon, the theme
// colour) is re-pointed by applyBrand; the manifest and any other tab follow on reload.

interface BrandStore {
  label: string;
  color: string;
  name: string;
  ink: string;
}

export const useBrandStore = create<BrandStore>(() => ({
  label: brandLabel,
  color: brandColor,
  name: brandName,
  ink: brandInk,
}));

/** Non-reactive read, for code outside React. */
export const currentBrand = () => useBrandStore.getState();

export function applyBrand(b: { label: string; color: string; name: string }) {
  const label = b.label.trim();
  const color = b.color.trim() || "#149ba7";
  const name = b.name.trim() || "Agent Fleet";
  useBrandStore.setState({ label, color, name, ink: readableInk(color) });
  if (typeof document === "undefined") return;
  // Only when no pane has claimed the tab (a pop-out sets its own title from the pane it
  // shows — clobbering that here would rename someone else's window).
  const title = `${name} — Console`;
  if (document.title.endsWith("— Console")) document.title = title;
  const theme = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (theme) theme.content = color;
  // The icon is served no-store, but the browser keeps the favicon it already painted, so
  // the href has to change for it to fetch again.
  for (const sel of ['link[rel="icon"]', 'link[rel="apple-touch-icon"]']) {
    const link = document.querySelector<HTMLLinkElement>(sel);
    if (!link) continue;
    const href = link.getAttribute("href") || "";
    link.setAttribute("href", href.split("?")[0] + "?b=" + encodeURIComponent(color));
  }
}
