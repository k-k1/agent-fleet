// Spaces store (zustand): the spaces under ~/spaces (ADR 0142), from GET /api/spaces. The
// Sessions view's Spaces section reads display names from it; which sessions run in a space
// comes from the session list itself (Session.space), so this list only has to name them.
import { create } from "zustand";
import { api, getTenant, isTransientErr } from "../../core/api/client.ts";

/** A space as GET /api/spaces reports it. */
export interface Space {
  /** The folder under ~/spaces: the space's identity, never renamed. */
  folder: string;
  /** The display name; "" when none was given. */
  name: string;
  dir: string;
  createdAt?: string;
}

interface SpacesStore {
  spaces: Space[];
  refresh(): Promise<void>;
  /** Forget the list, for a tenant switch: the spaces belong to the previous workspace. */
  clear(): void;
}

// Bumped by every refresh() and by clear(), so a slow answer to an older request (or to the
// previous tenant) never overwrites a newer one.
let generation = 0;

export const useSpacesStore = create<SpacesStore>((set) => ({
  spaces: [],
  async refresh() {
    const tenant = getTenant();
    const mine = ++generation;
    let d: { spaces?: unknown; error?: unknown };
    try {
      d = await api("api/spaces");
    } catch {
      return; // keep what the section shows; the next unknown space asks again
    }
    if (getTenant() !== tenant || mine !== generation) return;
    if (isTransientErr(d) || !d || d.error || !Array.isArray(d.spaces)) return;
    set({ spaces: d.spaces as Space[] });
  },
  clear() {
    generation++;
    set({ spaces: [] });
  },
}));

/** The name a space is shown under: its display name, or its folder when it has none. */
export function spaceLabel(folder: string, spaces: Space[]): string {
  return spaces.find((s) => s.folder === folder)?.name || folder;
}
