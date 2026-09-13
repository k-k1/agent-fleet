// The gallery's pure half (ADR 0080): turn one directory listing into the cards the view
// draws. Kept apart from the view so the rules that are easy to get quietly wrong — what
// counts as an image, what "newest" means when the Agent does not send times, where the
// list is cut — are covered by node tests instead of by looking at a grid.
import { imageFormat } from "../../lib/filemeta.ts";

/**
 * An entry as `api/fs/tree` returns it. The listing type is declared locally on purpose:
 * every reader of that endpoint does the same (ProjectFiles, the pickers), there is no
 * shared type, and that is what lets the Agent add a field without breaking anyone.
 *
 * `mtime` (unix seconds) is exactly that kind of addition, so it is OPTIONAL here and
 * must stay so: the Agent ships separately from the Console (native / pinned versions),
 * so a workspace that does not send it is a normal state, not an error.
 */
export interface FsEntry {
  name: string;
  type?: string;
  size?: number;
  mtime?: number;
}

/** One card. `path` is browse-root-relative, which is what every fs endpoint wants. */
export interface GalleryImage {
  name: string;
  path: string;
  size: number;
  /** Unix seconds, when the Agent sent one. Absent = do not claim to know when. */
  mtime?: number;
}

export type GallerySort = "new" | "name";

/**
 * How many cards are drawn before "show more". Each one is a thumbnail request, so an
 * unbounded folder means hundreds of decodes (2 at a time, Agent-side) plus — on a
 * remount past the 60-second cache — one conditional request per card queued behind the
 * browser's 6-per-host limit. The number itself is a starting point, not a measurement
 * (ADR 0080 open question 1).
 */
export const PAGE_SIZE = 300;

/**
 * The images in a listing, as cards. A directory entry is skipped even when its name ends
 * in `.png` (P0 is one level — decision 9), and what counts as an image is `imageFormat()`
 * and nothing else: a second extension table here would mean files the file pane opens as
 * pictures and the gallery silently drops (decision 3).
 */
export function galleryImages(entries: FsEntry[] | null | undefined, dir: string): GalleryImage[] {
  const out: GalleryImage[] = [];
  for (const e of entries || []) {
    if (!e || typeof e.name !== "string" || !e.name) continue;
    if (e.type === "dir") continue;
    if (!imageFormat(e.name)) continue;
    out.push({
      name: e.name,
      path: dir ? dir + "/" + e.name : e.name,
      size: typeof e.size === "number" && e.size > 0 ? e.size : 0,
      ...(typeof e.mtime === "number" && e.mtime > 0 ? { mtime: e.mtime } : {}),
    });
  }
  return out;
}

/**
 * Whether "newest first" can be answered at all. It takes EVERY card having a time: with a
 * mixed list the ones without would silently sink to the bottom and read as the oldest
 * pictures in the folder, which is a worse answer than plain name order.
 */
export function hasTimes(images: GalleryImage[]): boolean {
  return images.length > 0 && images.every((i) => typeof i.mtime === "number");
}

/**
 * The sort actually used. "new" degrades to "name" when the listing carries no times —
 * the same stance as the thumbnail parameter: no capability probe, no version compare,
 * just fall back and don't show a relative time you cannot support.
 *
 * Deliberately NOT "name descending": generated images sort by time under their names
 * only because the generator puts a unixnano in them, and a folder of screenshots would
 * make that coincidence into a lie.
 */
export function effectiveSort(images: GalleryImage[], sort: GallerySort | undefined): GallerySort {
  return sort === "name" || !hasTimes(images) ? "name" : "new";
}

const byName = (a: GalleryImage, b: GalleryImage): number =>
  a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }) ||
  (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);

/** Sorted copy. Newest first for "new"; ties fall back to the name so the order is total
 *  (two images written in the same second must not swap places between refreshes). */
export function sortImages(images: GalleryImage[], sort: GallerySort | undefined): GalleryImage[] {
  const mode = effectiveSort(images, sort);
  const out = [...images];
  out.sort(mode === "new" ? (a, b) => (b.mtime || 0) - (a.mtime || 0) || byName(a, b) : byName);
  return out;
}

/** Count and bytes for the header — the whole folder, not the drawn page, so "300 of 812"
 *  and the size beside it describe the same thing. */
export function galleryTotals(images: GalleryImage[]): { count: number; bytes: number } {
  return { count: images.length, bytes: images.reduce((n, i) => n + i.size, 0) };
}

/** The cards to draw: the first `limit` of an already-sorted list. */
export function visibleImages(images: GalleryImage[], limit: number): GalleryImage[] {
  return limit >= images.length ? images : images.slice(0, Math.max(0, limit));
}

/**
 * Index of the image an opener asked to enlarge (`galleryFocus`), or -1.
 *
 * Both spellings are accepted because the callers are menus in other lanes: one has the
 * file name in hand, the next has the full path, and a focus that silently does nothing is
 * indistinguishable from one that was never passed.
 */
export function focusIndex(images: GalleryImage[], focus: string | undefined): number {
  if (!focus) return -1;
  return images.findIndex((i) => i.path === focus || i.name === focus);
}
