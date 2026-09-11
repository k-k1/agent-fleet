// Brand icons — vendored monochrome SVGs for the things the Console names by brand: the agent
// CLIs (`agents/`) and the connected services (`services/` — GitHub, Slack, Jira, …). A third
// set keyed by the MODEL's provider was considered and dropped, for the reason in
// assets/brandicons/ATTRIBUTION.md. See that file for the sources and licenses.
//
// Why vendored rather than fetched from an upstream CDN at runtime: a cross-origin
// <use href> is refused by the SVG spec and an <img src> to a remote SVG renders it in its own
// document, where `currentColor` resolves to black — so a runtime icon is either impossible or
// stuck in light-mode colors. Getting the theme back would mean fetching the markup and
// injecting it into our DOM, i.e. third-party markup in the Console on every load.
//
// Every icon is a single-color path, so they are painted as a CSS mask
// (styles/brandicons.css) exactly like the monochrome file-icon sets — a mask reads only
// coverage, so the color comes from the surrounding element and both themes work with no
// per-icon rule. This module only decides WHETHER a key has an icon and which class draws it;
// the url() stays in CSS so Vite quotes it rather than us assembling it by hand (the comment
// in styles/brandicons.css explains why that distinction bites).

// The keys are listed by hand rather than discovered with import.meta.glob. That glob is a
// VITE transform, and ui/Icon.tsx — which imports this — is also bundled by the esbuild
// rendering harnesses (scripts/pdf, scripts/doc, scripts/drawio). esbuild leaves the call
// alone, so `import.meta.glob` is undefined at runtime, the module throws while initialising
// and the ENTIRE bundle renders nothing: pdf:check went from 15 OK to a bare
// "querySelector('.pdfview') is null". Anything reached from ui/ has to stay plain ESM.
// brandicons.test.ts checks these lists against the asset folders and the CSS, so they cannot
// quietly fall behind.
interface BrandSet {
  dir: string; // folder under assets/brandicons/
  cls: string; // class prefix in styles/brandicons.css
  keys: string[];
}
const SETS: BrandSet[] = [
  {
    dir: "agents",
    cls: "bi-agent-",
    keys: ["antigravity", "claude", "codex", "copilot", "cursor", "kiro", "opencode"],
  },
  {
    // Named by the settings card's provider id (features/settings/parts/providerCard), so a
    // card needs no id→icon table: its own id is the key. `svn` is deliberately absent — the
    // Subversion mark is three diagonal stripes that say nothing at 16px, where the "sv"
    // monogram at least does (compared at 1:1 in headless Chromium).
    dir: "services",
    cls: "bi-service-",
    keys: ["aws", "bitbucket", "cloudwatch", "discord", "github", "grafana", "jira", "pagerduty", "slack"],
  },
];

/** Folder -> keys, and folder -> class prefix. For the parity test, not for rendering. */
export const BRAND_SETS: Record<string, string[]> = Object.fromEntries(SETS.map((s) => [s.dir, s.keys]));
export const BRAND_CLASS: Record<string, string> = Object.fromEntries(SETS.map((s) => [s.dir, s.cls]));

/** Marks an Icon name as a brand asset instead of a codicon glyph ("brand:claude"). */
export const BRAND_PREFIX = "brand:";

/** Classes that draw a brand icon, or null when the key is not vendored — ui/Icon.tsx then
 *  falls back to a codicon, so a typo or a not-yet-vendored brand degrades to a glyph rather
 *  than an empty box. Keys are unique across sets (brandicons.test.ts), so one flat namespace
 *  keeps `Icon` taking a single `name` the way it always has. */
export function brandClass(key: string): string | null {
  for (const s of SETS) {
    if (s.keys.includes(key)) return `brandicon ${s.cls}${key}`;
  }
  return null;
}
