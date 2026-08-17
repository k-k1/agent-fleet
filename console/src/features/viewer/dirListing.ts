// dirListing — a short-lived memo in front of GET fs/tree.
//
// The Markdown path auto-linker (MarkdownView) has to know whether a cited path exists
// BEFORE it may link it, and a mirror shows dozens of turns citing the same few
// directories. One listing per directory answers every path in it, and the memo keeps a
// scrolled-back conversation from re-asking on every re-render.
//
// The TTL is short on purpose: a file the agent has just written should become clickable
// on the next render, not minutes later. A failed listing is cached too (as null) so a
// path under an unreadable directory doesn't retry once per mention.

import { api } from "../../core/api/client.ts";

export type DirListing = Map<string, "file" | "dir">;

const TTL_MS = 15_000;
const MAX_DIRS = 64; // bounded like the api client's ETag cache; oldest entry evicted

const cache = new Map<string, { at: number; listing: Promise<DirListing | null> }>();

/** listDirListing returns name → type for one directory, or null when it can't be listed. */
export function listDirListing(dir: string): Promise<DirListing | null> {
  const hit = cache.get(dir);
  if (hit && Date.now() - hit.at < TTL_MS) {
    cache.delete(dir); // re-insert to refresh the LRU position
    cache.set(dir, hit);
    return hit.listing;
  }
  const listing = api(`api/fs/tree?path=${encodeURIComponent(dir)}`)
    .then((d: { entries?: { name: string; type: string }[] } | null) => {
      if (!d?.entries) return null;
      const out: DirListing = new Map();
      for (const e of d.entries) out.set(e.name, e.type === "dir" ? "dir" : "file");
      return out;
    })
    .catch(() => null);
  cache.delete(dir);
  cache.set(dir, { at: Date.now(), listing });
  if (cache.size > MAX_DIRS) {
    for (const oldest of cache.keys()) {
      cache.delete(oldest);
      break;
    }
  }
  return listing;
}

/** Test hook: drop the memo so a case can serve a different listing for the same path. */
export function clearDirListingCache(): void {
  cache.clear();
}
