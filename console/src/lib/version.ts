// Client build identity + deploy-update detection.
//
// Background: the Console is served with Cache-Control: no-store, but a long-lived
// mobile PWA/tab keeps running whatever bundle it loaded — a stale cached index.html
// (or an in-memory app that never navigates) can pin the phone to old code across
// reloads, which is exactly what made a shipped fix look like it "didn't work". The
// build stamp below is baked in at build time (vite `define`), and dist/version.json
// carries the SAME stamp for the *currently served* build. Comparing the two lets the
// running app notice a newer deploy and reload past the stale cache (see reloadForUpdate
// / useUpdateCheck).

export interface BuildId {
  time: string; // ISO 8601 build time — the canonical, unique-per-build id
  sha: string; // short git SHA, best-effort (may be empty)
}

// __AF_BUILD__ is replaced by a literal at build time. Guard with typeof so any context
// where the define is somehow absent degrades to an empty stamp instead of throwing.
const RAW: BuildId = typeof __AF_BUILD__ !== "undefined" ? __AF_BUILD__ : { time: "", sha: "" };

export const buildInfo: BuildId = { time: RAW.time || "", sha: RAW.sha || "" };

// Human-readable label for display (account menu). Local time so it matches the user's
// clock; short SHA in parens when available. Falls back gracefully on a missing stamp.
export function buildLabel(): string {
  const { time, sha } = buildInfo;
  let when = time;
  if (time) {
    try {
      const d = new Date(time);
      const p = (n: number) => String(n).padStart(2, "0");
      when = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
    } catch {
      /* keep the raw ISO string */
    }
  }
  return sha ? `${when} (${sha})` : when || "unknown";
}

// Fetch the CURRENTLY SERVED build id. no-store bypasses the HTTP cache so this reflects
// the server even when the app itself was loaded from a stale cache. Returns null on any
// failure (offline, 401 after auth expiry, missing file in an old build) → treated as
// "no update", never an error.
export async function fetchServerBuild(): Promise<BuildId | null> {
  try {
    // Resolve against baseURI so it works behind the path-stripping proxy (same rule as
    // core/api/client.ts rel()). Inlined to keep this module import-free → node-testable.
    const url = new URL("version.json", document.baseURI).toString();
    const r = await fetch(url, { cache: "no-store" });
    if (!r.ok) return null;
    const j = (await r.json()) as Partial<BuildId> | null;
    if (j && typeof j.time === "string" && j.time) {
      return { time: j.time, sha: typeof j.sha === "string" ? j.sha : "" };
    }
    return null;
  } catch {
    return null;
  }
}

// True when the server is serving a different build than the one running here. Requires
// a stamped running build (skips the check in an unstamped/dev-without-define context).
export function hasNewBuild(server: BuildId | null): boolean {
  return !!server && !!buildInfo.time && server.time !== buildInfo.time;
}

// Set just before a version-update navigation so the terminal's beforeunload guard
// (TerminalView) knows this reload is intentional and doesn't prompt "leave site?".
export function markUpdating(): void {
  (window as unknown as { __afUpdating?: boolean }).__afUpdating = true;
}

// Reload onto a cache-busted URL (?v=<build tag>) so a stale cached index.html can't pin
// the app to the old bundle — a changing URL forces a fresh fetch (verified: adding a
// query string is what unstuck the phone). The tag is the server build's SHA (or its
// compacted timestamp) so re-navigations to the same version reuse one cache entry.
export function reloadForUpdate(server?: BuildId): void {
  markUpdating();
  const tag =
    ((server?.sha || server?.time || "") + "").replace(/[^A-Za-z0-9]/g, "").slice(0, 16) || String(Date.now());
  try {
    const u = new URL(window.location.href);
    u.searchParams.set("v", tag);
    window.location.replace(u.toString());
  } catch {
    window.location.reload();
  }
}
