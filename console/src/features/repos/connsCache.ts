// A tiny shared cache of the last successfully-resolved api/connections snapshot. The
// fetch shells out to each agent's auth check (`claude auth status`, agy's token file, …)
// and takes ~1.5-2s, so a leaf that fetches on open (e.g. HandoffModal) shows its
// connection-gated agent list "a beat late". The always-mounted repo rail already
// resolves this via useRepoRailContext; it writes the snapshot here so leaves can render
// the same list INSTANTLY. Readers subscribe with useSyncExternalStore. Only successful
// snapshots are cached (never a null/errored result), so the cache is never poisoned —
// a cold reader falls back to its own fetch.
import type { ConnectionsStatus } from "../../types/session.ts";

let cached: ConnectionsStatus | null = null;
const subs = new Set<() => void>();

export function setCachedConns(c: ConnectionsStatus | null): void {
  if (!c) return; // never cache a null/errored result — keep the last good snapshot
  cached = c;
  for (const fn of subs) fn();
}

export function getCachedConns(): ConnectionsStatus | null {
  return cached;
}

export function subscribeConns(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}
