// What the gallery remembers about a folder it has already read, so walking back into one
// costs no round trip (ADR 0080 P2).
//
// The problem this solves: `api/fs/tree` is one Console -> CP -> Agent round trip, and the view
// used to blank the grid and wait for it on EVERY change of folder — including the browser's
// Back button, where the answer is the listing that was on screen a second ago. The CP tags the
// response with a weak ETag (control-plane/etag.go) and `api()` replays the parsed body on a
// 304, so an unchanged folder already costs no BODY — but a round trip is still a round trip,
// and that window is what reads as slow.
//
// Module-level rather than in a store: nothing re-renders on it. The view reads it once per
// change of folder, writes it when a listing lands, and the cards themselves are re-rendered by
// the listing state as before. A zustand store here would re-render every gallery pane on every
// scroll event (the position is written from one).
import { api, isTransientErr } from "../../core/api/client.ts";
import { PAGE_SIZE, type FsEntry } from "./gallery.ts";

/** Everything about one folder worth restoring. `limit` and `scrollTop` are the reader's
 *  PLACE in it: coming back to a folder that was scrolled halfway down, expanded past the
 *  first page, and being dropped at the top of the first 300 again is its own kind of slow. */
interface CacheEntry {
  entries: FsEntry[];
  /** When the listing was read (epoch ms) — a prefetch skips a folder read moments ago. */
  at: number;
  scrollTop: number;
  limit: number;
}

/**
 * How many folders are remembered. Each entry is a name/size/mtime list, so even a 1000-image
 * folder is tens of KB — the bound exists so a session that walks a deep tree for an hour does
 * not hold every listing it ever saw, not because the entries are dear.
 */
const MAX_FOLDERS = 30;

/** A prefetch (hover / pointer-down on a folder card) skips a folder read this recently. The
 *  listing behind it still refreshes on the usual triggers once the folder is on screen. */
const PREFETCH_FRESH_MS = 10_000;

// Insertion order IS the LRU order: a read re-inserts, so the first key is the oldest use.
const cache = new Map<string, CacheEntry>();

/** In-flight prefetches, so sweeping the mouse across a row of folder cards asks once. */
const inflight = new Map<string, Promise<unknown>>();

export function readGallery(path: string): CacheEntry | undefined {
  const hit = cache.get(path);
  if (!hit) return undefined;
  cache.delete(path); // re-insert to refresh the LRU position
  cache.set(path, hit);
  return hit;
}

function put(path: string, next: CacheEntry): void {
  cache.delete(path);
  cache.set(path, next);
  while (cache.size > MAX_FOLDERS) {
    const oldest = cache.keys().next();
    if (oldest.done) break;
    cache.delete(oldest.value);
  }
}

/** Remember a listing. The reader's place in the folder is kept — a background refresh that
 *  landed one new picture must not scroll them back to the top. */
export function writeGallery(path: string, entries: FsEntry[]): void {
  const prev = cache.get(path);
  put(path, { entries, at: Date.now(), scrollTop: prev?.scrollTop ?? 0, limit: prev?.limit ?? PAGE_SIZE });
}

/** Remember where in the folder the reader is. Written from a scroll handler and from "show
 *  more", so it must stay a plain map write. */
export function rememberGalleryView(path: string, view: { scrollTop?: number; limit?: number }): void {
  const prev = cache.get(path);
  if (!prev) return; // nothing was read here; there is no listing to come back to
  if (typeof view.scrollTop === "number") prev.scrollTop = view.scrollTop;
  if (typeof view.limit === "number") prev.limit = view.limit;
}

/**
 * Forget one folder. Called when a first read answers with a hard error (gone, denied): a
 * stale listing of a folder that no longer exists is the one lie this cache could tell.
 */
export function forgetGallery(path: string): void {
  cache.delete(path);
}

/** Tests only — the cache outlives a component, which is the whole point. */
export function clearGalleryCache(): void {
  cache.clear();
  inflight.clear();
}

/**
 * How many pictures inside each subfolder the listing describes: one, the cover on its card
 * (ADR 0080 P2). The endpoint accepts up to four; asking for more than is drawn would make the
 * Agent stat and warm pictures nobody sees.
 */
const PEEK = 1;

/**
 * The listing URL. ONE builder, because `api()`'s ETag cache is keyed by the URL string: a
 * prefetch that spelled `warm` differently from the view's own read would cache under a
 * different key and throw away the 304 replay that makes an unchanged folder free.
 */
export function galleryTreeURL(path: string, warm: number): string {
  return `api/fs/tree?path=${encodeURIComponent(path)}&warm=${warm}&peek=${PEEK}`;
}

/**
 * `ok` is a listing to show. `retry` says what a failure MEANS: true for a transport failure
 * or a 5xx (the CP's answer while the agent restarts — worth another try), false for an answer
 * that settled (gone, denied, or the request was aborted).
 */
export type ListingResult = { ok: true; entries: FsEntry[] } | { ok: false; retry: boolean; hard: boolean };

/**
 * Read one folder and cache it. `hard` distinguishes "the Agent answered, and the answer is no"
 * — which a first read must show as an error rather than keep a stale grid for — from a
 * transport failure, where what is already on screen is the better answer.
 */
export async function fetchGalleryListing(path: string, warm: number, signal?: AbortSignal): Promise<ListingResult> {
  let d: { entries?: FsEntry[]; error?: { code?: string } };
  try {
    d = await api(galleryTreeURL(path, warm));
  } catch {
    return { ok: false, retry: true, hard: false };
  }
  if (signal?.aborted) return { ok: false, retry: false, hard: false };
  if (!d || isTransientErr(d)) return { ok: false, retry: true, hard: false };
  if (d.error || !Array.isArray(d.entries)) return { ok: false, retry: false, hard: true };
  writeGallery(path, d.entries);
  return { ok: true, entries: d.entries };
}

/**
 * Read a folder the reader has only POINTED at (hover, pointer-down on its card), so the
 * listing is already in hand when the click lands. Cheap on purpose: one request, deduped
 * while in flight, skipped for a folder read moments ago, and silent about failure — a
 * prefetch that guessed wrong must cost nothing but one request.
 */
export function prefetchGallery(path: string, warm: number): void {
  const hit = cache.get(path);
  if (hit && Date.now() - hit.at < PREFETCH_FRESH_MS) return;
  if (inflight.has(path)) return;
  const p = fetchGalleryListing(path, warm)
    .catch(() => {})
    .finally(() => inflight.delete(path));
  inflight.set(path, p);
}
