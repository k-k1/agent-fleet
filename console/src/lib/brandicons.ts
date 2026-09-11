// Brand icons — vendored monochrome SVGs for the things that have a logo of their own:
// the agent CLIs (an "agent kind") and, later, the model providers. See
// assets/brandicons/ATTRIBUTION.md for the sources and licenses.
//
// Why vendored rather than fetched from the upstream CDN at runtime: a cross-origin
// <use href> is refused by the SVG spec and an <img src> to a remote SVG renders it in
// its own document, where `currentColor` resolves to black — so a runtime icon is either
// impossible or stuck in light-mode colors. Getting the theme back would mean fetching
// the markup and injecting it into our DOM, i.e. third-party markup in the Console on
// every load. Vendored assets cost ~4KB and none of that.
//
// Every icon is a single-color path on `currentColor`, so they are painted as a CSS mask
// (styles/brandicons.css) exactly like the monochrome file-icon sets — the color then comes
// from the surrounding element and both themes work with no per-icon rule.
//
// This module only decides WHETHER a key has an icon; the mask itself is a CSS class, so
// the url() is quoted by Vite rather than assembled by hand (styles/brandicons.css explains
// why that distinction bites).

// The glob is the inventory: adding an SVG to the folder is enough to make it known here,
// and brandicons.test.ts checks the CSS has a matching rule. Only the URL strings land in
// the bundle, and nothing loads them — the CSS rules are what the browser fetches.
const mods = import.meta.glob<string>("../assets/brandicons/*/*.svg", {
  eager: true,
  query: "?url",
  import: "default",
});

/** Vendored keys per set: { agents: ["claude", "codex", …] }. */
export const BRAND_SETS: Record<string, string[]> = {};
for (const p in mods) {
  const parts = p.split("/");
  const key = (parts.pop() ?? "").replace(".svg", "");
  const set = parts.pop() ?? "";
  (BRAND_SETS[set] ||= []).push(key);
}

/** Marks an Icon name as a brand asset instead of a codicon glyph ("brand:claude"). */
export const BRAND_PREFIX = "brand:";

/** Class pair for an agent-kind brand icon, or null when the key is not vendored — ui/Icon.tsx
 *  then falls back to a codicon, so a typo or a not-yet-vendored agent degrades to a glyph
 *  rather than an empty box. */
export function agentBrandClass(key: string): string | null {
  return BRAND_SETS.agents?.includes(key) ? `brandicon bi-agent-${key}` : null;
}
