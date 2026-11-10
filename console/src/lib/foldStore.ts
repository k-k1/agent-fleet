// Fold (collapse) flags of the left rail and its sections: pure UI preference that
// survives logout, but only as copies scoped to the signed-in (user, tenant).
//
// Lives apart from core/api/client.ts (which feeds it the scope) so components that
// persist a fold do not import the API client, and tests that mock it keep working.

// `af-files-home` has no name part: its scoped key is `af-files-home\0<user>\0<tenant>\0`.
export const FOLD_PREFIXES = ["af-proj-", "af-shared-wc-", "af-shared-proj-", "af-section-", "af-files-home"];

let user = "";
let tenant = "";
let rev = 0;
const listeners = new Set<() => void>();

/** Bumps whenever the (user, tenant) scope changes; mounted fold hooks re-read on it. */
export const subscribeFoldScope = (fn: () => void): (() => void) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};
export const getFoldScopeRev = (): number => rev;

// NUL is the segment separator, so strip it from the identity parts (a value holding one
// could otherwise collide with another account's scope).
const clean = (x: string) => x.replaceAll("\0", "");

export function setFoldScope(s: { user?: string; tenant?: string }): void {
  const nu = s.user !== undefined ? clean(s.user) : user;
  const nt = s.tenant !== undefined ? clean(s.tenant) : tenant;
  if (nu === user && nt === tenant) return;
  // A different account in the same tab without a logout in between: unscoped legacy keys
  // are the previous account's, so they must not be offered to the new one as a fallback.
  if (user !== "" && nu !== user) dropLegacyFolds();
  user = nu;
  tenant = nt;
  rev++;
  listeners.forEach((fn) => fn());
}

function dropLegacyFolds(): void {
  try {
    const drop: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && FOLD_PREFIXES.some((p) => k.startsWith(p)) && !k.includes("\0")) drop.push(k);
    }
    for (const k of drop) localStorage.removeItem(k);
  } catch {}
}

// foldKey scopes a legacy key (`af-proj-<repo>`) to `af-proj-\0<user>\0<tenant>\0<repo>`.
// The NUL separators are what clearLocalState keys on: a legacy unscoped key never contains
// one, so it is still wiped on logout and cannot carry the previous account's folds to the
// next. Keys outside FOLD_PREFIXES pass through. The name part stays in clear text, so the
// previous account's repository names remain listable in devtools after logout; the scope
// only keeps them out of the UI.
export function foldKey(legacy: string): string {
  const p = FOLD_PREFIXES.find((x) => legacy.startsWith(x));
  if (!p) return legacy;
  return `${p}\0${user}\0${tenant}\0${legacy.slice(p.length)}`;
}

/**
 * True for a key clearLocalState must keep on logout. A key with an empty user segment was
 * written before whoami resolved, so it belongs to no account and every later account would
 * share it: it is not kept.
 */
export const isScopedFold = (k: string): boolean =>
  FOLD_PREFIXES.some((p) => {
    if (!k.startsWith(p + "\0")) return false;
    return k.split("\0")[1] !== "";
  });

// readFold returns the stored flag (null = never chosen). The legacy unscoped key is only
// a fallback for a scoped key that does not exist yet; writeFold drops it, so once the user
// folds or unfolds, the old value cannot come back.
export function readFold(legacy: string): boolean | null {
  try {
    const v = localStorage.getItem(foldKey(legacy)) ?? localStorage.getItem(legacy);
    return v === null ? null : v === "1";
  } catch {
    return null;
  }
}
export function writeFold(legacy: string, open: boolean): void {
  try {
    localStorage.setItem(foldKey(legacy), open ? "1" : "0");
    localStorage.removeItem(legacy);
  } catch {}
}
