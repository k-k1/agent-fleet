// Gatekeeping for the stencil names the sandboxed drawio frame asks the parent to fetch.
// The frame is untrusted (it evaluates a 4 MB viewer over file content), so the names it sends
// are treated as input: they must have the shape of a manifest key, there are at most
// MAX_STENCIL_SETS per request, and the URL they resolve to must still sit under the stencils
// prefix. The CP checks the name against its manifest as well; this keeps the fetch, which
// carries the member's cookie, from ever being aimed elsewhere in the first place.
import { rel } from "../../core/api/client.ts";

const STENCIL_PREFIX = "api/drawio/stencils/";

/** The shape of a key in control-plane/assets/drawio-stencils.json: `aws4.xml`, `rack/f5.xml`. */
const STENCIL_NAME = /^[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_-]+)*\.xml$/;

/** The manifest holds ~200 sets and one diagram uses a handful; anything above this is not a diagram. */
export const MAX_STENCIL_SETS = 64;

/** Longest manifest key is 41 characters. */
const MAX_NAME_LENGTH = 96;

export function isStencilName(name: unknown): name is string {
  return typeof name === "string" && name.length <= MAX_NAME_LENGTH && STENCIL_NAME.test(name);
}

/**
 * The URL to fetch a stencil set from, or null when the name is not acceptable or the resolved
 * URL leaves the stencils prefix on this origin.
 */
export function stencilUrl(name: unknown): string | null {
  if (!isStencilName(name)) return null;
  const path = rel(STENCIL_PREFIX + name.split("/").map(encodeURIComponent).join("/"));
  const base = document.baseURI;
  const root = new URL(rel(STENCIL_PREFIX), base);
  const url = new URL(path, base);
  if (url.origin !== root.origin || !url.pathname.startsWith(root.pathname)) return null;
  return path;
}

/** The names to fetch from a `stencils` request: valid, de-duplicated, capped. */
export function acceptedStencilNames(sets: unknown): string[] {
  if (!Array.isArray(sets)) return [];
  const out: string[] = [];
  for (const s of sets) {
    if (out.length >= MAX_STENCIL_SETS) break;
    if (isStencilName(s) && stencilUrl(s) !== null && !out.includes(s)) out.push(s);
  }
  return out;
}
