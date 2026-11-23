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
  /** A flattened listing that left pictures out (`api/fs/images`'s bounds). */
  truncated?: boolean;
}

/**
 * The cache key for a folder as one view shows it. A flattened folder is a different listing
 * of the same path — its own entries, scroll position and page size — so it is remembered
 * under its own key; NUL cannot appear in a path, so the two never collide.
 */
export function galleryKey(path: string, flat?: boolean): string {
  return flat ? path + "\u0000flat" : path;
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

/**
 * Read order, taken when a read STARTS. Answers can come back in any order (a hover prefetch
 * that is slow while the view's own read of the same folder is fast), and the cache must end up
 * holding the listing of the read that began last, not of the one that answered last. One
 * counter for every key and every pane, so prefetches and several panes on one folder are
 * ordered too.
 */
let readSeq = 0;

/** Per key, the order of the newest read whose answer was written, or of the last forget. A
 *  read that began before it cannot write. Separate from `cache` on purpose: LRU eviction of a
 *  listing must not lift the barrier for a read still in flight. An entry lives while the key
 *  is cached or has a read in flight (`active`), so it is bounded by what is actually open. */
const barrier = new Map<string, { n: number; forgot: boolean }>();

/** Reads in flight per key; the barrier of an uncached key is dropped when the last one ends. */
const active = new Map<string, number>();

/** In-flight prefetches, so sweeping the mouse across a row of folder cards asks once. */
const inflight = new Map<string, Promise<unknown>>();

export function readGallery(path: string): CacheEntry | undefined {
  const hit = cache.get(path);
  if (!hit) return undefined;
  cache.delete(path); // re-insert to refresh the LRU position
  cache.set(path, hit);
  return hit;
}

/** Take a place in the read order. Call before the request is sent, never after the await. */
function beginRead(): number {
  return ++readSeq;
}

/** Drop a key's barrier once nothing can need it: not cached, and no read in flight. */
function pruneBarrier(key: string): void {
  if (!cache.has(key) && !active.has(key)) barrier.delete(key);
}

function put(path: string, next: CacheEntry): void {
  cache.delete(path);
  cache.set(path, next);
  while (cache.size > MAX_FOLDERS) {
    const oldest = cache.keys().next();
    if (oldest.done) break;
    cache.delete(oldest.value);
    pruneBarrier(oldest.value);
  }
}

type WriteOutcome = "kept" | "superseded" | "forgotten";

function tryWrite(key: string, entries: FsEntry[], truncated: boolean | undefined, n: number): WriteOutcome {
  const prev = cache.get(key);
  const floor = barrier.get(key);
  if (floor && floor.n > n) return floor.forgot ? "forgotten" : "superseded";
  barrier.set(key, { n, forgot: false });
  put(key, {
    entries,
    at: Date.now(),
    scrollTop: prev?.scrollTop ?? 0,
    limit: prev?.limit ?? PAGE_SIZE,
    ...(truncated ? { truncated } : {}),
  });
  return "kept";
}

/** Remember a listing. The reader's place in the folder is kept — a background refresh that
 *  landed one new picture must not scroll them back to the top.
 *
 *  `seq` is the order the read began in; the write is dropped when a read that began later has
 *  already been written, or the folder was forgotten after this read began. Without `seq` the
 *  write counts as the newest read. Returns whether it was kept. */
export function writeGallery(key: string, entries: FsEntry[], truncated?: boolean, seq?: number): boolean {
  return tryWrite(key, entries, truncated, seq ?? beginRead()) === "kept";
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
  barrier.set(path, { n: ++readSeq, forgot: true }); // a read begun before this cannot bring the listing back
  pruneBarrier(path);
}

/** Tests only — the cache outlives a component, which is the whole point. */
export function clearGalleryCache(): void {
  cache.clear();
  inflight.clear();
  barrier.clear();
  active.clear();
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
 * How deep "include subfolders" walks and how many pictures it asks for. Three levels covers
 * the generated root (root → session folder → pictures) and a docs/img-style tree; the limit
 * is a few pages of PAGE_SIZE — the Agent bounds its walk on its own as well (internal/fsx/fs_images.go).
 */
const FLAT_DEPTH = 3;
const FLAT_LIMIT = 1000;

/** The flattened listing's URL. Same one-builder rule as galleryTreeURL. */
export function galleryImagesURL(path: string, warm: number): string {
  return `api/fs/images?path=${encodeURIComponent(path)}&depth=${FLAT_DEPTH}&limit=${FLAT_LIMIT}&warm=${warm}`;
}

/**
 * `ok` is a listing to show. `retry` says what a failure MEANS: true for a transport failure
 * or a 5xx (the CP's answer while the agent restarts — worth another try), false for an answer
 * that settled (gone, denied, or the request was aborted).
 */
export type ListingResult =
  | { ok: true; entries: FsEntry[]; truncated?: boolean }
  | { ok: false; retry: boolean; hard: boolean };

/**
 * Read one folder and cache it. `hard` distinguishes "the Agent answered, and the answer is no"
 * — which a first read must show as an error rather than keep a stale grid for — from a
 * transport failure, where what is already on screen is the better answer.
 *
 * `accept` is asked only for a listing, right before it is cached, and must claim it in the
 * same step (ReadTicket.land): false settles the read as aborted, so an answer the caller drops
 * (a newer read of the folder already landed, or the view moved on) never overwrites a newer
 * listing in the cache. Claiming later, after the await, would let an older answer write the
 * cache while a newer one is between its own write and its claim.
 *
 * `accept` only orders reads of ONE view. The cache orders the rest itself: a read that began
 * before the one whose listing is already cached (a hover prefetch that answers late, another
 * pane on the same folder) still returns its listing but does not write it.
 */
export async function fetchGalleryListing(
  path: string,
  warm: number,
  accept?: () => boolean,
  flat?: boolean,
): Promise<ListingResult> {
  const key = galleryKey(path, flat);
  const seq = beginRead();
  active.set(key, (active.get(key) ?? 0) + 1);
  try {
    return await readListing(path, warm, accept, flat, key, seq);
  } finally {
    const left = (active.get(key) ?? 1) - 1;
    if (left > 0) active.set(key, left);
    else active.delete(key);
    pruneBarrier(key);
  }
}

async function readListing(
  path: string,
  warm: number,
  accept: (() => boolean) | undefined,
  flat: boolean | undefined,
  key: string,
  seq: number,
): Promise<ListingResult> {
  let d: { entries?: FsEntry[]; truncated?: boolean; error?: { code?: string } };
  try {
    d = await api(flat ? galleryImagesURL(path, warm) : galleryTreeURL(path, warm));
  } catch {
    return { ok: false, retry: true, hard: false };
  }
  if (!d || isTransientErr(d)) return { ok: false, retry: true, hard: false };
  if (d.error || !Array.isArray(d.entries)) return { ok: false, retry: false, hard: true };
  if (accept && !accept()) return { ok: false, retry: false, hard: false };
  const truncated = d.truncated === true;
  const outcome = tryWrite(key, d.entries, truncated, seq);
  // The folder was reported gone after this read began, and nothing is cached to keep the
  // reader's place in it: showing the listing would leave a grid whose scroll position and
  // page size are never remembered. Settle it as the same "no" the other answer got.
  if (outcome === "forgotten") return { ok: false, retry: false, hard: true };
  // "superseded": a newer listing is cached (it keeps the place), and this one is still the
  // caller's to show.
  return { ok: true, entries: d.entries, ...(truncated ? { truncated } : {}) };
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
