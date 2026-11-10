// Fold (collapse) flags of the left rail and its sections: pure UI preference that
// survives logout, but only as copies scoped to the signed-in (user, tenant).
//
// Lives apart from core/api/client.ts (which feeds it the scope) so components that
// persist a fold do not import the API client, and tests that mock it keep working.

export const FOLD_PREFIXES = ["af-proj-", "af-shared-wc-", "af-shared-proj-", "af-section-"];

let user = "";
let tenant = "";
export function setFoldScope(s: { user?: string; tenant?: string }): void {
  if (s.user !== undefined) user = s.user;
  if (s.tenant !== undefined) tenant = s.tenant;
}

// foldKey scopes a legacy key (`af-proj-<repo>`) to `af-proj-\0<user>\0<tenant>\0<repo>`.
// The NUL separators are what clearLocalState keys on: a legacy unscoped key never contains
// one, so it is still wiped on logout and cannot carry the previous account's folds to the
// next. Keys outside FOLD_PREFIXES pass through.
export function foldKey(legacy: string): string {
  const p = FOLD_PREFIXES.find((x) => legacy.startsWith(x));
  if (!p) return legacy;
  return `${p}\0${user}\0${tenant}\0${legacy.slice(p.length)}`;
}

/** True for a key clearLocalState must keep on logout. */
export const isScopedFold = (k: string): boolean => FOLD_PREFIXES.some((p) => k.startsWith(p + "\0"));

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
