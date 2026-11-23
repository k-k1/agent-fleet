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
  /** Transient failures in a row since the last answer, and whose requests they were. The
   *  section waits longer after each, but never because another owner's requests failed. */
  failures: number;
  failOwner: string;
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
  failOwner: "",
  async refresh() {
    const owner = spacesOwner();
    const mine = ++generation;
    const current = () => mine === generation && spacesOwner() === owner;
    const failed = () => set((s) => ({ failures: (s.failOwner === owner ? s.failures : 0) + 1, failOwner: owner }));
    let d: { spaces?: unknown; error?: unknown };
    try {
      d = await api("api/spaces");
    } catch {
      if (current()) failed();
      return;
    }
    if (!current()) return;
    if (isTransientErr(d)) {
      failed();
      return;
    }
    // A 4xx (an Agent without the route) will not change on a retry: the folders stand in.
    const spaces = d && !d.error && Array.isArray(d.spaces) ? (d.spaces as Space[]) : [];
    set({ spaces, owner, failures: 0, failOwner: "" });
  },
  clear() {
    generation++;
    set({ spaces: [], owner: "", failures: 0, failOwner: "" });
  },
}));

/** The spaces list as `owner` may use it: another workspace's list names nothing here, even a
 *  folder that happens to exist in both. */
export function spacesFor(st: { spaces: Space[]; owner: string }, owner: string): Space[] {
  return st.owner === owner ? st.spaces : NO_SPACES;
}
const NO_SPACES: Space[] = [];

/** Transient failures in a row for `owner`'s requests. */
export function failuresFor(st: { failures: number; failOwner: string }, owner: string): number {
  return st.failOwner === owner ? st.failures : 0;
}

/** The name a space is shown under: its display name, or its folder when it has none. */
export function spaceLabel(folder: string, spaces: Space[]): string {
  return spaces.find((s) => s.folder === folder)?.name || folder;
}

/** How long to wait before asking again after `failures` transient failures in a row. */
export function spacesRetryDelay(failures: number): number {
  return failures <= 0 ? 0 : Math.min(60_000, 2_000 * 2 ** (failures - 1));
}
