// Brand icons — vendored monochrome SVGs for the things that have a logo of their own.
// Today that is the agent CLIs (an "agent kind"); a per-provider set was considered and
// dropped, for the reason in assets/brandicons/ATTRIBUTION.md (the surfaces that would
// carry it are native <select>s, which cannot hold markup). The set dimension is kept in
// the class names anyway so adding one later does not mean renaming these. See that same
// file for the sources and licenses.
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
// from the surrounding element and both themes work with no per-icon rule. This module only
// decides WHETHER a key has an icon; the mask itself is a CSS class, so the url() is quoted
// by Vite rather than assembled by hand (styles/brandicons.css explains why that bites).

// The keys are listed by hand rather than discovered with import.meta.glob. That glob is a
// VITE transform, and ui/Icon.tsx — which imports this — is also bundled by the esbuild
// rendering harnesses (scripts/pdf, scripts/doc, scripts/drawio). esbuild leaves the call
// alone, so `import.meta.glob` is undefined at runtime, the module throws while
// initialising and the ENTIRE bundle renders nothing: pdf:check went from 15 OK to a bare
// "querySelector('.pdfview') is null". Anything reached from ui/ has to stay plain ESM.
// brandicons.test.ts checks this list against the asset folder and the CSS, so it cannot
// quietly fall behind.
const AGENT_KEYS = ["antigravity", "claude", "codex", "copilot", "cursor", "kiro", "opencode"];

/** Vendored keys per set: { agents: ["claude", "codex", …] }. */
export const BRAND_SETS: Record<string, string[]> = { agents: AGENT_KEYS };

/** Marks an Icon name as a brand asset instead of a codicon glyph ("brand:claude"). */
export const BRAND_PREFIX = "brand:";

/** Class pair for an agent-kind brand icon, or null when the key is not vendored — ui/Icon.tsx
 *  then falls back to a codicon, so a typo or a not-yet-vendored agent degrades to a glyph
 *  rather than an empty box. */
export function agentBrandClass(key: string): string | null {
  return AGENT_KEYS.includes(key) ? `brandicon bi-agent-${key}` : null;
}
