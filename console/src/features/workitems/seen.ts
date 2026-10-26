// Per-browser memory of what the user last saw of each work item (#2779), so the rail can mark
// rows that moved since. Not per-user server state: this is a reading aid, and losing it costs one
// unmarked round, never a wrong marker.
//
// The baseline of an item is the updatedAt and comment count it had when the user last looked
// (opened its detail modal) — or when it first appeared on the rail. Seeding on first sight is
// what keeps a first-ever load from marking every row unread.
import { create } from "zustand";
import type { WorkItem } from "./read.ts";

export const SEEN_KEY = "af.workitems.seen.v1";
/** Oldest baselines are evicted past this, so the stored map cannot grow with every ticket ever
 * listed. An evicted item that comes back is re-seeded, i.e. shown as read. */
export const SEEN_MAX = 500;

export interface SeenBaseline {
  u: string;
  c: number;
}
/** Insertion order is the age order: a touched entry is re-inserted last. Keys are
 * "provider:key", never integer-like, so the engine preserves that order. */
export type SeenMap = Record<string, SeenBaseline>;

export type Unread = "" | "updated" | "comments";

export const seenId = (i: Pick<WorkItem, "provider" | "key">) => `${i.provider}:${i.key}`;
const commentsOf = (i: WorkItem) => (typeof i.commentCount === "number" && i.commentCount > 0 ? i.commentCount : 0);

/** Reads the stored map; anything that is not the expected shape (corrupt JSON, a stale format,
 * hand-edited values) is dropped — whole when the container is wrong, entry by entry otherwise. */
export function parseSeen(raw: string | null): SeenMap {
  if (!raw) return {};
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return {};
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return {};
  const out: SeenMap = {};
  for (const [k, e] of Object.entries(v as Record<string, unknown>)) {
    const b = e as Partial<SeenBaseline> | null;
    if (b && typeof b.u === "string" && typeof b.c === "number" && Number.isFinite(b.c)) out[k] = { u: b.u, c: b.c };
  }
  return out;
}

function trim(m: SeenMap): SeenMap {
  const keys = Object.keys(m);
  if (keys.length <= SEEN_MAX) return m;
  const out: SeenMap = {};
  for (const k of keys.slice(keys.length - SEEN_MAX)) out[k] = m[k];
  return out;
}

/** Adds a baseline for every item not yet known; known ones are left alone. Returns the same
 * object when nothing was added, so callers can skip the write. */
export function seedSeen(m: SeenMap, items: WorkItem[]): SeenMap {
  let next: SeenMap | null = null;
  for (const i of items) {
    const id = seenId(i);
    if (id in m || (next && id in next)) continue;
    next ??= { ...m };
    next[id] = { u: i.updatedAt, c: commentsOf(i) };
  }
  return next ? trim(next) : m;
}

/** Records that the user has seen the item as it is now. */
export function markSeenIn(m: SeenMap, item: WorkItem): SeenMap {
  const id = seenId(item);
  const { [id]: _drop, ...rest } = m;
  return trim({ ...rest, [id]: { u: item.updatedAt, c: commentsOf(item) } });
}

const stamp = (s: string) => {
  const n = Date.parse(s);
  return Number.isFinite(n) ? n : null;
};

/** "" = nothing new (or no baseline), "comments" = the count grew, "updated" = updatedAt moved
 * past the baseline some other way (label, state, an edit). */
export function unreadOf(m: SeenMap, item: WorkItem): Unread {
  const b = m[seenId(item)];
  if (!b || !item.updatedAt) return "";
  const a = stamp(item.updatedAt);
  const was = stamp(b.u);
  const moved = a !== null && was !== null ? a > was : false;
  if (!moved) return "";
  return commentsOf(item) > b.c ? "comments" : "updated";
}

function load(): SeenMap {
  try {
    return parseSeen(localStorage.getItem(SEEN_KEY));
  } catch {
    return {};
  }
}
function save(m: SeenMap) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(m));
  } catch {
    // Quota or a disabled store: the markers then last until reload, which is acceptable.
  }
}

interface SeenState {
  map: SeenMap;
  seed(items: WorkItem[]): void;
  mark(item: WorkItem): void;
}

export const useSeenStore = create<SeenState>((set, get) => ({
  map: load(),
  seed(items) {
    const next = seedSeen(get().map, items);
    if (next === get().map) return;
    save(next);
    set({ map: next });
  },
  mark(item) {
    const next = markSeenIn(get().map, item);
    save(next);
    set({ map: next });
  },
}));
