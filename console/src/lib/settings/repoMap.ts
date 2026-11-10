// The per-member "tracker project -> base repository" memory (the workItemRepoMap preference).
//
// ui-prefs saves are last-writer-wins per key, so a plain `scope -> repo` map loses whatever a
// stale tab or another device learned in between, and a deletion cannot be told from "never
// learned" and comes back from the server copy. Every entry therefore carries the time of its
// last change, an empty `repo` is a tombstone (forgotten), and two copies merge per entry with the
// newer change winning. The merge needs nothing but the two maps, so it never reads the shared
// localStorage record (which holds whichever tenant's tab wrote last).

export interface RepoMapEntry {
  /** Base repository folder; "" = forgotten (tombstone). */
  repo: string;
  /** Epoch ms of the change. 0 = a legacy entry from before the timestamps. */
  at: number;
}

export type RepoMap = Record<string, RepoMapEntry>;

/** Reads a stored value: legacy `scope -> "repo"` strings become entries with at = 0, anything
 * malformed is dropped. Idempotent. */
export function normalizeRepoMap(v: unknown): RepoMap {
  if (!v || typeof v !== "object" || Array.isArray(v)) return {};
  const out: RepoMap = {};
  for (const [k, x] of Object.entries(v)) {
    if (typeof x === "string") {
      if (x) out[k] = { repo: x, at: 0 };
    } else if (x && typeof x === "object") {
      const e = x as { repo?: unknown; at?: unknown };
      if (typeof e.repo === "string") out[k] = { repo: e.repo, at: typeof e.at === "number" && Number.isFinite(e.at) && e.at > 0 ? e.at : 0 };
    }
  }
  return out;
}

/** Per-entry merge of two copies: the newer change wins. On a tie the tombstone does (when in
 * doubt the member chooses once more rather than launching in a repository they forgot), and
 * between two live entries the larger repo name does, so the result never depends on argument
 * order and two devices converge. Tombstones are never pruned: one is tens of bytes and the map
 * is bounded by the number of tracker projects, while dropping one would let a long-offline
 * device bring a forgotten repository back, which is the wrong-repository launch this memory
 * exists to prevent. Stamps come from each device's wall clock, so a badly slow clock can lose a
 * merge against an entry it never saw; the member then picks once more. */
export function mergeRepoMaps(a: RepoMap, b: RepoMap): RepoMap {
  const out: RepoMap = { ...a };
  for (const [k, e] of Object.entries(b)) {
    const cur = out[k];
    if (!cur || e.at > cur.at) out[k] = e;
    else if (e.at === cur.at && e.repo !== cur.repo) {
      if (!e.repo || (!!cur.repo && e.repo > cur.repo)) out[k] = e;
    }
  }
  return out;
}

/** The map with `scope` set to `repo` ("" forgets it), stamped `now`. */
export function withRepoEntry(map: RepoMap, scope: string, repo: string, now = Date.now()): RepoMap {
  const out: RepoMap = { ...map };
  // A stamp that does not move past the entry it replaces (clock skew between devices) would lose
  // the merge against that entry.
  const prev = out[scope]?.at ?? 0;
  out[scope] = { repo, at: Math.max(now, prev + 1) };
  return out;
}

/** The live entries as the plain `scope -> repo` map the launch resolver reads. */
export function liveRepoMap(map: RepoMap): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, e] of Object.entries(map)) if (e.repo) out[k] = e.repo;
  return out;
}
