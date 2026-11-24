// Spaces store (zustand): the spaces under ~/spaces (ADR 0142), from GET /api/spaces. The
// Sessions view's Spaces section reads display names from it; which sessions run in a space
// comes from the session list itself (Session.space), so this list only has to name them.
import { create } from "zustand";
import { api, apiJSON, getTenant, getUser, isTransientErr } from "../../core/api/client.ts";

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
  /** Start a GET /api/spaces somebody else makes (the hub's support probe), so one opening of the
   *  hub asks once: the ticket records whose request it is and supersedes the ones before it. */
  begin(): SpacesTicket;
  /** Take that request's answer. Ignored (false) unless it is a list and nothing newer has started
   *  or cleared the store since (another workspace, a later refresh): the caller then reads the
   *  list again for the workspace it is in now rather than go without one. */
  adopt(ticket: SpacesTicket, d: unknown): boolean;
  /** Forget the list, for a tenant switch: the spaces belong to the previous workspace. */
  clear(): void;
}

/** The workspace a request goes to: the tenant and the member. */
export const spacesOwner = (): string => getTenant() + "\n" + getUser();

// Bumped by every refresh() and by clear(), so a slow answer to an older request (or for a
// previous owner) never overwrites a newer one.
let generation = 0;

/** A request made outside refresh(): the workspace it was for and its place in the order. */
export interface SpacesTicket {
  owner: string;
  gen: number;
}

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
  begin() {
    return { owner: spacesOwner(), gen: ++generation };
  },
  adopt(ticket, d) {
    if (ticket.gen !== generation || ticket.owner !== spacesOwner()) return false;
    const list = d as { spaces?: unknown; error?: unknown } | null | undefined;
    if (!list || list.error || !Array.isArray(list.spaces)) return false;
    set({ spaces: list.spaces as Space[], owner: ticket.owner, failures: 0, failOwner: "" });
    return true;
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

/** What renameSpace answers: the server's error, or `stale` when the workspace changed under the
 *  dialog (nothing was sent, or the answer belongs to a workspace no longer on screen). */
export type RenameResult = { error?: unknown; stale?: boolean } | null;

/** Rename a space's display name (the folder never changes) in the workspace `owner`, which is
 *  the one the dialog was opened in: a form left open across a tenant or member switch must not
 *  send its folder name to the new workspace, where the same folder may be another space. Null
 *  once the name is stored; the list is then read again, so every view names the space the new
 *  way and a space another tab removed drops out. */
export async function renameSpace(folder: string, name: string, owner: string): Promise<RenameResult> {
  if (spacesOwner() !== owner) return { stale: true };
  const d = await apiJSON(`api/spaces/${encodeURIComponent(folder)}`, "PATCH", { name });
  // The answer to a request made in a workspace that is no longer the one on screen says
  // nothing about this one: no toast, no list.
  if (spacesOwner() !== owner) return { stale: true };
  if (d?.error) return { error: d.error };
  // Show the new name at once; the answer to the refresh below replaces the list anyway.
  if (d?.space) {
    useSpacesStore.setState((s) =>
      s.owner === owner ? { spaces: s.spaces.map((x) => (x.folder === folder ? { ...x, name: d.space.name ?? name } : x)) } : s,
    );
  }
  void useSpacesStore.getState().refresh();
  return null;
}

/** The name a space is shown under: its display name, or its folder when it has none. */
export function spaceLabel(folder: string, spaces: Space[]): string {
  return spaces.find((s) => s.folder === folder)?.name || folder;
}

/** How long to wait before asking again after `failures` transient failures in a row. */
export function spacesRetryDelay(failures: number): number {
  return failures <= 0 ? 0 : Math.min(60_000, 2_000 * 2 ** (failures - 1));
}
