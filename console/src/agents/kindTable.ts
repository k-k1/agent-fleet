// The kind table: one row per session kind, and the only place a new kind's identity is typed.
//
// Everything that restates the Agent's kind catalog or a kind's colour derives from it:
//   - SessionKind (a literal union) and SESSION_KINDS (display order) — types/session.ts re-exports them;
//   - FALLBACK_KIND_FACTS in kindCatalog.ts, the static copy of the catalog the Console answers
//     from until (and unless) GET /agents/kinds arrives;
//   - the --kind-<slug> tokens and the `.kind-<slug>` map in styles/tokens.css, which the build
//     expands from markers (expandKindTokens, run by the Vite plugin in vite.config.js).
// Presentation, capabilities and wording stay with the descriptors in registry.ts, which is typed
// Record<SessionKind, …> and therefore fails to compile until a new row here has its descriptor.
//
// Leaf module by design: it imports nothing, so the Vite config and the dev scripts under
// scripts/kindcolor can load it directly, and registry.ts and kindCatalog.ts can both depend on it
// without a cycle. Keep it to erasable TypeScript (no enums) for the same reason.
//
// The facts (managedOnly / managedDefault / mcpClient) are a copy of
// control-plane/internal/agentkinds/catalog.json; kindCatalog.test.ts holds the two equal, so
// a drift on either side fails a test instead of reaching a user.

export type Tri = "yes" | "no" | "unverified";

export interface KindRow {
  readonly id: string;
  /** The kind has no Terminal (CLI) route at all. */
  readonly managedOnly: boolean;
  /** The snapshot's value as sent; the fallback folds managedOnly into it (managed-only implies managed). */
  readonly managedDefault: boolean;
  readonly mcpClient: Tri;
  /** Kind colour per theme (hex). Contrast and distinguishability were measured against the
   *  other hues and the semantic colours in both themes; see the notes on the rows. */
  readonly hue: { readonly dark: string; readonly light: string };
}

// Order is the display order (New Session buttons and every list derived "in SESSION_KINDS
// order"). lcpp and muse sit just before shell/ssm: both are registered (ADR 0093 / ADR 0095) and
// launchable only once their own precondition is met — see each descriptor's available() in
// registry.ts and guide/ref/agents.md's footnotes.
//
// Light hues are darker than dark ones on purpose: .kind-tag paints its label on a 20% tint of its
// own hue, so the surface is darker than --panel, and every light hue clears 4.5:1 on its own tint
// and on --panel2. copilot stays the darkest neutral and opencode the lighter one (docs/log/43).
export const KIND_TABLE = [
  { id: "claude", managedOnly: false, managedDefault: false, mcpClient: "yes", hue: { dark: "#e0a45e", light: "#8f4f13" } },
  { id: "codex", managedOnly: false, managedDefault: true, mcpClient: "yes", hue: { dark: "#4ec97a", light: "#126b3b" } },
  // Rose/magenta: the only warm hue free of the other seven at the time (docs/log/40).
  { id: "cursor", managedOnly: false, managedDefault: true, mcpClient: "yes", hue: { dark: "#d96ba1", light: "#a02a63" } },
  // Neutral/charcoal grey, moved off purple so kiro could take it (docs/log/43); darker than opencode.
  { id: "copilot", managedOnly: false, managedDefault: true, mcpClient: "yes", hue: { dark: "#7d8590", light: "#30363d" } },
  // Purple, inheriting copilot's former hue (docs/log/43 §4-1); Kiro's brand colour.
  { id: "kiro", managedOnly: false, managedDefault: true, mcpClient: "yes", hue: { dark: "#a371f7", light: "#6b3ac9" } },
  { id: "agy", managedOnly: false, managedDefault: false, mcpClient: "yes", hue: { dark: "#4285f4", light: "#0f5ec4" } },
  // Light slate grey: lighter than copilot's charcoal for a clear separation.
  { id: "opencode", managedOnly: false, managedDefault: true, mcpClient: "yes", hue: { dark: "#aab4be", light: "#575e68" } },
  // The 10th hue (ADR 0093 decision 1). Red (#f85149) was rejected: it collided byte-for-byte with
  // the Console's error red, and a kind badge in the error hue reads as an error state on the
  // same screens. The candidate set was widened to the kind hues PLUS the semantic colours
  // (--warn, --ok, --accent, --err, the diff and code-grid marks), both themes, and landed on an
  // olive/mustard yellow at Lab hue ~55-58°, the one family none of them sits in: min dE 17.1 dark
  // / 14.4 light (vs. --warn). Contrast against --bg/--panel/--bar/--active-bg: 8.04 dark, 6.10
  // light (the older kinds' worst case is agy, 3.81 dark / 4.86 light). Confirmed by rendering
  // the chips next to the real diff.css/--err/--warn/--ok swatches headless, both themes.
  { id: "lcpp", managedOnly: true, managedDefault: true, mcpClient: "yes", hue: { dark: "#d9c952", light: "#5c5400" } },
  // The 11th hue (ADR 0095). Same method as lcpp, with the candidate set swept rather than
  // hand-picked (scripts/kindcolor/muse.mjs: Lab hue x chroma x lightness, inside the chroma band the
  // shipped kinds occupy, hues within 18° of a semantic colour excluded outright). The answer is a
  // PALE orchid at hue 320°: it wins on lightness, which puts distance between it and kiro's
  // saturated purple in the one gap left (kiro 308° -> cursor 350°). Dark: min dE 17.9 (vs.
  // cursor), contrast 7.11. The light value is deliberately NOT the sweep's own answer (#5f376b,
  // dE 15.9): rendered, that is a grey plum, a THIRD grey beside copilot and opencode. #6b2d7e
  // costs 3 points of dE and buys a colour (min dE 12.9 vs. kiro, contrast 7.21), still twice the
  // precedent floor. dE ranked the candidates; the picture (scripts/kindcolor/chips.mjs) decided.
  // Not Meta's brand blue: agy and ssm hold that region, and this palette chooses
  // distinguishability over brand fidelity.
  { id: "muse", managedOnly: true, managedDefault: true, mcpClient: "yes", hue: { dark: "#dbabe6", light: "#6b2d7e" } },
  { id: "shell", managedOnly: false, managedDefault: false, mcpClient: "no", hue: { dark: "#46c9d0", light: "#0f676d" } },
  { id: "ssm", managedOnly: false, managedDefault: false, mcpClient: "no", hue: { dark: "#6d8bf5", light: "#2c39a3" } },
] as const satisfies readonly KindRow[];

export type SessionKind = (typeof KIND_TABLE)[number]["id"];

export const SESSION_KINDS: readonly SessionKind[] = KIND_TABLE.map((r) => r.id);

// The CSS the table stands for. styles/tokens.css holds only the markers
// (`/* @af-kind-tokens dark */`, `light`, `map`); the build and the dev scripts expand them here.
// The map is the one place a `.kind-<slug>` class resolves to its colour: the use sites paint
// with var(--kind) behind the `kind` marker kindColorClasses() writes, and --kind is declared on
// the element itself so --kind-<slug> resolves against the nearest theme container.
export function kindTokenCss(part: "dark" | "light" | "map"): string {
  if (part === "map") return KIND_TABLE.map((r) => `.kind-${r.id} { --kind: var(--kind-${r.id}); }`).join("\n");
  return KIND_TABLE.map((r) => `--kind-${r.id}: ${r.hue[part]};`).join("\n  ");
}

const MARKER = /\/\* @af-kind-tokens (dark|light|map) \*\//g;

/** Replaces every kind-token marker in a stylesheet; other text is returned untouched. */
export function expandKindTokens(css: string): string {
  return css.replace(MARKER, (_m, part: "dark" | "light" | "map") => kindTokenCss(part));
}
