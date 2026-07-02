// Cached "which session kinds are ready" flags so the New Session modal can render
// the kind buttons instantly instead of flashing shell-only until api/connections +
// api/ssm/hosts resolve. Persisted in localStorage: the common case (a stable setup)
// shows the right options with zero delay across opens and reloads; the modal still
// refetches on open and reconciles, so a change (new auth / removed host) corrects
// within a beat.
const KEY = "af-kind-avail";

// kind → is-available flags (shell is implicitly always available, so it's omitted).
export type KindAvail = Record<string, boolean>;

export function readKindAvail(): KindAvail {
  try {
    return JSON.parse(localStorage.getItem(KEY) || "null") || {};
  } catch {
    return {};
  }
}

export function writeKindAvail(a: KindAvail): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(a));
  } catch {
    /* storage unavailable — just skip the cache */
  }
}
