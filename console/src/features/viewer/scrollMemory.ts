// Remembers the file viewer's reading position per pane x file x surface, and returns to it when
// the reader comes back.
//
// Why it exists: a tabbed cell renders only the one selected view (PaneHost's selectedView).
// Switching tabs unmounts FileView entirely, and coming back re-fetches from api/fs/file and
// rebuilds — that is, starts from the top. The position cannot live inside React, so it lives
// outside the component. Toggling edit vs. view is the same case: there the surface's box goes
// away under `hidden` (display:none), and restoring it makes the browser reset scrollTop to 0.
//
// The stored value is raw px (scrollTop). Mirror (features/mirror/scrollMark.ts) anchors on a
// turn because the transcript it comes back to is a different one (the tail is re-fetched); a
// file comes back with the same content in the same order, so px is enough. Height, however,
// settles late (Markdown preview sets innerHTML in a passive effect, PDF needs page dimensions
// first), so one write-back is not enough — the retry lives in parts/useScrollMemory.ts.
//
// Store only: it touches neither React nor the DOM, so it runs in the node project.

/** How many surfaces to remember. A pane/file pair has a few (code, preview, ...), so this is
 *  worth some tens of files. On overflow the oldest goes first (Map keeps insertion order). */
const MAX_ENTRIES = 200;

/** Key -> last scrollTop. The memory lasts only while the tab lives and is lost on reload, so the
 *  next visit starts from the top (module scope, like echoStore / scrollMark). */
const positions = new Map<string, number>();

/** Key for one surface. A different pane means a separate memory (the same file can be open twice
 *  and read at two places), and so does a different file. */
export function scrollMemoryKey(paneId: string | undefined, filePath: string): string | null {
  if (!filePath) return null;
  // The separator is NUL, which can never appear in a path. Always write it escaped: a raw
  // control byte makes the file count as binary and drops it out of grep
  // (src/test/noRawControlChars.test.ts).
  return `${paneId || "-"}\u0000${filePath}`;
}

export function saveScrollPos(key: string, top: number): void {
  if (!key) return;
  positions.delete(key); // re-insert so the map stays in least-recently-used order
  positions.set(key, top);
  if (positions.size > MAX_ENTRIES) {
    const oldest = positions.keys().next();
    if (!oldest.done) positions.delete(oldest.value);
  }
}

export function loadScrollPos(key: string): number | null {
  if (!key) return null;
  const top = positions.get(key);
  return top === undefined ? null : top;
}

/** For tests. */
export function clearScrollPos(): void {
  positions.clear();
}
