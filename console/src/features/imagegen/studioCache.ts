// studioCache — the last answers about studios this window has seen, kept outside any pane.
//
// A tabbed cell mounts only its selected view, so switching back to a studio tab remounts the
// pane from nothing: without these, it painted "no agent", "engine unavailable" and an empty
// picker for the half second before the first reads came back. The cached answers are only a
// first frame; every pane still reads the Agent on mount and replaces them.
//
// It is also where a tab title learns a studio's name and bound session: the tab strip does not
// mount the studio pane. Its own module (zustand, the api calls and the wire types) so the panes'
// tab strip can import it without pulling in the studio view.
import { create } from "zustand";
import { listStudios } from "./api.ts";
import type { ImagegenStatus, StudioSummary, StudioWire } from "./wire.ts";

interface StudioCache {
  /** The picker's list, as last read. */
  list: StudioSummary[];
  /** Each studio as last read in full, by id. */
  byId: Record<string, StudioWire>;
  /** The last `GET /imagegen/status`. */
  status: ImagegenStatus | null;
}

export const useStudioCache = create<StudioCache>(() => ({ list: [], byId: {}, status: null }));

let listInflight: Promise<void> | null = null;
// Set once any list answer landed: an empty list is an answer too, and must not be asked again
// on every render of the tab strip.
let listKnown = false;

export function cacheStudioList(list: StudioSummary[]): void {
  listKnown = true;
  useStudioCache.setState({ list });
}

export function cacheStudio(s: StudioWire): void {
  useStudioCache.setState((st) => ({ byId: { ...st.byId, [s.id]: s } }));
}

export function uncacheStudio(id: string): void {
  useStudioCache.setState((st) => {
    const byId = { ...st.byId };
    delete byId[id];
    return { byId, list: st.list.filter((s) => s.id !== id) };
  });
}

export const cacheImagegenStatus = (status: ImagegenStatus): void => useStudioCache.setState({ status });

/** What a tab needs to name a studio: the full read when there is one, else the list's row. */
export function cachedStudioSummary(st: StudioCache, id: string): Pick<StudioSummary, "id" | "title" | "created_at" | "updated_at" | "session"> | null {
  const full = st.byId[id];
  if (full) return full;
  return st.list.find((s) => s.id === id) ?? null;
}

/** Read the list once for a window that has a studio tab but no studio pane mounted yet (the tab
 *  strip names studios before any of them is opened). One request at a time; a failure leaves the
 *  tabs on their generic name until a pane reads. */
export function ensureStudioList(): void {
  if (listInflight || listKnown) return;
  listInflight = listStudios()
    .then((r) => {
      if (r && !r.error && Array.isArray(r.studios)) cacheStudioList(r.studios);
    })
    .catch(() => undefined)
    .finally(() => {
      listInflight = null;
    });
}
