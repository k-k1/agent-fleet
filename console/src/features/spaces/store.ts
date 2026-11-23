// Spaces store (zustand): the spaces under ~/spaces (ADR 0142), from GET /api/spaces. The
// Sessions view's Spaces section reads display names from it; which sessions run in a space
// comes from the session list itself (Session.space), so this list only has to name them.
import { create } from "zustand";
import { api, getTenant, getUser, isTransientErr } from "../../core/api/client.ts";

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
  /** Whose workspace `spaces` describes (spacesOwner when it was asked); "" before any answer.
   *  A folder name means nothing across workspaces, so a list is only used for its owner. */
  owner: string;
  /** Transient failures in a row since the last answer; the section waits longer after each. */
  failures: number;
  refresh(): Promise<void>;
  /** Forget the list, for a tenant switch: the spaces belong to the previous workspace. */
  clear(): void;
}

/** The workspace a request goes to: the tenant and the member. */
export const spacesOwner = (): string => getTenant() + "\n" + getUser();

// Bumped by every refresh() and by clear(), so a slow answer to an older request (or for a
// previous owner) never overwrites a newer one.
let generation = 0;

export const useSpacesStore = create<SpacesStore>((set) => ({
  spaces: [],
  owner: "",
  failures: 0,
  async refresh() {
    const owner = spacesOwner();
    const mine = ++generation;
    const current = () => mine === generation && spacesOwner() === owner;
    let d: { spaces?: unknown; error?: unknown };
    try {
      d = await api("api/spaces");
    } catch {
      if (current()) set((s) => ({ failures: s.failures + 1 }));
      return;
    }
    if (!current()) return;
    if (isTransientErr(d)) {
      set((s) => ({ failures: s.failures + 1 }));
      return;
    }
    // A 4xx (an Agent without the route) will not change on a retry: the folders stand in.
    const spaces = d && !d.error && Array.isArray(d.spaces) ? (d.spaces as Space[]) : [];
    set({ spaces, owner, failures: 0 });
  },
  clear() {
    generation++;
    set({ spaces: [], owner: "", failures: 0 });
  },
}));

/** The name a space is shown under: its display name, or its folder when it has none. */
export function spaceLabel(folder: string, spaces: Space[]): string {
  return spaces.find((s) => s.folder === folder)?.name || folder;
}

/** How long to wait before asking again after `failures` transient failures in a row. */
export function spacesRetryDelay(failures: number): number {
  return failures <= 0 ? 0 : Math.min(60_000, 2_000 * 2 ** (failures - 1));
}
