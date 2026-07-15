// authExpired — a module-level latch for "the Control Plane login session expired".
//
// When the CP-native Google session cookie (af_session; absolute ~7-day TTL, no
// sliding refresh) lapses, authGate answers every XHR / WebSocket / SSE with 401
// (only a top-level HTML navigation is 302'd to /login). The old behavior bounced
// the whole page to /login on the first such 401 — silent and jarring, and it tore
// down the terminal sockets immediately. Instead we latch this signal once and let a
// React host (AuthExpiredModal) surface a dialog: the running sessions keep working
// in the workspace, and re-login is one explicit click.
//
// Kept out of React (and free of ./client imports, to avoid an import cycle) so both
// the fetch wrapper and the raw terminal WebSocket — which bypasses that wrapper —
// can trip it.

let expired = false;
const listeners = new Set<() => void>();

// isAuthExpired reports whether the latch has fired (used to suppress repeat probes).
export function isAuthExpired(): boolean {
  return expired;
}

// signalAuthExpired latches the expiry and notifies subscribers exactly once. Safe to
// call on every 401 — subsequent calls are no-ops.
export function signalAuthExpired(): void {
  if (expired) return;
  expired = true;
  for (const fn of listeners) {
    try {
      fn();
    } catch {
      /* a listener throwing must not block the others */
    }
  }
}

// subscribeAuthExpired registers a listener (fired once, when the latch flips) and
// returns an unsubscribe. If the latch already fired, the listener runs immediately.
export function subscribeAuthExpired(fn: () => void): () => void {
  listeners.add(fn);
  if (expired) {
    try {
      fn();
    } catch {
      /* ignore */
    }
  }
  return () => listeners.delete(fn);
}

// relogin navigates the whole page to the CP login landing, carrying ?next= so the
// user returns to the same view after Google re-auth. This is the only destructive
// step, and it is now user-initiated (the dialog button) rather than an automatic
// bounce. The URL is resolved against document.baseURI (mirrors client.rel) so it
// works both at the host root and behind a path-stripping proxy.
export function relogin(): void {
  const next = encodeURIComponent(location.pathname + location.search);
  const login = new URL("login", document.baseURI).toString();
  location.assign(login + "?next=" + next);
}
