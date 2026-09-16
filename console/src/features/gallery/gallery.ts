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
  /** On a DIRECTORY, the newest pictures inside it — the answer to `peek=<n>` (ADR 0080 P2).
   *  Optional for the same reason `mtime` is: an Agent that does not send it is a normal
   *  state, and a folder card then draws the plain icon it always drew. */
  preview?: { name?: string; mtime?: number }[];
  /** On a DIRECTORY, how many pictures are in it. Same optionality. */
  images?: number;
}

/** One card. `path` is browse-root-relative, which is what every fs endpoint wants. */
export interface GalleryImage {
  name: string;
  path: string;
  size: number;
  /** Unix seconds, when the Agent sent one. Absent = do not claim to know when. */
  mtime?: number;
}

/** One subfolder card. Same path convention as an image; no size, because a listing
 *  does not carry what is inside a directory and asking would be one request per card. */
export interface GalleryFolder {
  name: string;
  path: string;
  /** The picture to show on the card, when the Agent described the folder (`peek`). */
  cover?: GalleryImage;
  /** How many pictures are inside. `undefined` means "not answered", which is NOT zero —
   *  an empty folder and an Agent that cannot say must not read the same. */
  count?: number;
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

/** Name order, digits compared as numbers ("img9" before "img10") and case ignored, with a
 *  codepoint tiebreak so the order is total. Shared by the image list and the folder list. */
const byName = (a: { name: string }, b: { name: string }): number =>
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
 * The subfolders of a listing, always in name order.
 *
 * Deliberately NOT following the image sort: "newest first" over folders would reshuffle
 * the row someone is aiming at every time a picture lands inside one of them, and a
 * directory's mtime says when its contents last changed, which is not what the name of a
 * folder promises.
 */
export function galleryFolders(entries: FsEntry[] | null | undefined, dir: string): GalleryFolder[] {
  const out: GalleryFolder[] = [];
  for (const e of entries || []) {
    if (!e || typeof e.name !== "string" || !e.name) continue;
    if (e.type !== "dir") continue;
    const path = dir ? dir + "/" + e.name : e.name;
    // The cover is the first name the Agent peeked at, if it peeked at all. Validated the
    // same way a listing's own entries are: this is a wire field, and a folder card must not
    // build a download URL out of whatever arrived.
    const first = Array.isArray(e.preview) ? e.preview[0] : undefined;
    const cover =
      first && typeof first.name === "string" && first.name && imageFormat(first.name)
        ? {
            name: first.name,
            path: path + "/" + first.name,
            size: 0,
            ...(typeof first.mtime === "number" && first.mtime > 0 ? { mtime: first.mtime } : {}),
          }
        : undefined;
    out.push({
      name: e.name,
      path,
      ...(cover ? { cover } : {}),
      ...(typeof e.images === "number" && e.images >= 0 ? { count: e.images } : {}),
    });
  }
  out.sort(byName);
  return out;
}

/**
 * The folder above this one, or null at the top.
 *
 * `""` is the browse root and IS a gallery path: going up has to reach the same place the
 * file tree starts at, otherwise "Up" dies one level early and the reader is stranded in
 * `.cache`. The stored-layout validator accepts the empty string for exactly this reason —
 * the menus never produce it, navigation does.
 */
export function parentPath(dir: string): string | null {
  if (!dir) return null;
  const at = dir.lastIndexOf("/");
  return at < 0 ? "" : dir.slice(0, at);
}

/**
 * The breadcrumb: every folder from the root down to this one, each with the path that
 * jumps there. The root's own label is the caller's business (it has no name).
 */
export function breadcrumb(dir: string): GalleryFolder[] {
  const out: GalleryFolder[] = [];
  let acc = "";
  for (const seg of dir.split("/").filter(Boolean)) {
    acc = acc ? acc + "/" + seg : seg;
    out.push({ name: seg, path: acc });
  }
  return out;
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
