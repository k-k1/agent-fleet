// The upstream CLI release watcher's liveness, as the environment tab reads it.
//
// `.github/workflows/cli-release-watch.yml` is what notices that a public agent-CLI
// version moved and dispatches the contract for it; the version its contract last passed
// is the `tested` state. On 2026-09-09 one unreadable release source aborted the whole
// job, so a new claude was detected and dropped, and `tested` stood still — which is
// exactly what a quiet upstream looks like. Nobody could tell the difference until the
// contract was run by hand the next morning.
//
// So the rule here is the restart badge's rule (control-plane/workspace_stale.go): never
// draw "all fine" from an answer we do not have, and do not warn about one either. A
// deployment with no route out never learns anything about the watcher, and shows no row
// at all rather than a broken one.
//
// The block is served by CP GET /api/env/ws-settings (control-plane/cli_release_watch.go).

// CLIRelease mirrors the CP's `cliReleaseWire`. Empty strings are how the CP says "the
// watcher never wrote this", so nothing here is optional on the wire.
export type CLIRelease = {
  watcherOkAt: string;
  watcherFailed: string[];
  watcherAt: string;
  tested: Record<string, string>;
  fetchedAt: string;
};

// A watcher that last succeeded longer ago than this is treated as broken. It runs daily,
// so two missed runs is the first point at which "it has not run" is a better explanation
// than "yesterday's run is not in yet".
export const WATCHER_STALE_MS = 48 * 60 * 60 * 1000;

export type WatcherHealth =
  | { kind: "unknown" }
  | { kind: "ok"; okAt: number }
  | { kind: "failed"; okAt: number; rows: string[] }
  | { kind: "stale"; okAt: number };

// watcherHealth decides what the row says. `okAt` is NaN when the watcher has never
// recorded a clean run, which the caller renders as "never" rather than as a date.
//
// Order matters: named failed rows beat staleness, because they say WHICH source could not
// be read — the reader's next move. Unknown is only ever "we have been told nothing at
// all"; a watcher that reports failures without ever having succeeded is a failure, not a
// mystery.
export function watcherHealth(cr: CLIRelease | null | undefined, now: number = Date.now()): WatcherHealth {
  if (!cr) return { kind: "unknown" };
  const okAt = Date.parse(cr.watcherOkAt || "");
  const rows = (cr.watcherFailed || []).filter(Boolean);
  if (isNaN(okAt) && rows.length === 0) return { kind: "unknown" };
  if (rows.length > 0) return { kind: "failed", okAt, rows };
  if (now - okAt > WATCHER_STALE_MS) return { kind: "stale", okAt };
  return { kind: "ok", okAt };
}
