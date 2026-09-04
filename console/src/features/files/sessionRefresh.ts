// sessionRefresh — the wiring that re-reads FILES for one session's working copy when
// that session's turn ends.
//
// Why it exists: the tree used to refresh only on the manual refresh button and on
// workspace start/stop (the tick in store.ts). Files an agent created or deleted stayed
// invisible until someone pressed that button, and anyone who had not noticed the button
// was stuck believing nothing had happened.
//
// Why the moment the session goes idle: files keep appearing and disappearing during a
// turn, so following that would need a watcher or periodic fetches, both of which cost
// round trips. The end of a turn, by contrast, is derivable from information already in
// hand — the session list carries state through applyList whether it arrived by push or
// by poll. So this costs no extra traffic, and it matches the expectation that the agent
// stopping means the results are in.
//
// The busy -> not-busy edge is detected the same way as in useSessionNotifications /
// waiting.ts, including not firing on the first observation, which keeps a reload from
// refreshing every session at once (see the note in waiting.ts).
//
// Two cases the end of a turn does not cover, each filled by its own trigger:
//   - Someone looking mid-way through a long turn: re-read only running sessions' working
//     copies every WORKING_TICK_MS (tickWorking below; the timer stops entirely when
//     nothing is running).
//   - Someone who was away: revalidation on tab return / window focus, which lives on the
//     tree side (ProjectFiles / FilesChanges). It is also effectively the only way to pick
//     up files touched by a shell session, which carries no state.
import { useSessionsStore } from "../sessions/store.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { COALESCE_MS, MIN_GAP_MS, WORKING_TICK_MS } from "./refreshPolicy.ts";
import { useFilesStore } from "./store.ts";
import type { Session } from "../../types/session.ts";

/** The running states. Everything else (idle / question / plan / permission / limited /
 *  blocked / auth ...) means the turn ended and it is the human's move, and all of them
 *  are worth re-reading: a session paused on a question still leaves behind the files it
 *  wrote up to that point. */
const BUSY_STATES = new Set(["working", "compacting"]);

/** Whether the session is running, i.e. may still be writing. backgroundBusy means idle
 *  as far as the hook is concerned but with a background task still going; reading then
 *  would miss files, so it counts as busy. */
export const isBusySession = (s: Pick<Session, "alive" | "state" | "backgroundBusy">): boolean =>
  !!s.alive && (BUSY_STATES.has(s.state || "") || !!s.backgroundBusy);

/** The range a session may rewrite, relative to home. A session with no working copy (a
 *  shell running in home, say) gets "" — no range can be determined, so nothing happens.
 *  Rounded to the working copy even when a subdir is set: agents routinely touch files
 *  outside their cwd. */
export const sessionPrefix = (s: Pick<Session, "repo">): string => (s.repo ? "repos/" + s.repo : "");

// The timing constants and the reasoning behind them live in refreshPolicy.ts, shared
// between the side that fires and the side that reads.

/**
 * Builds a pure function to call on every incoming list, returning the working copies
 * that should be re-read. Its ledger (name -> was busy last time) is updated per call.
 *
 * A first observation is only recorded, never fired on. Sessions that vanish from the
 * list (deleted, archived) do not fire either: the row takes its repo with it, so no
 * range can be derived.
 */
export function createTurnEndDetector(): (list: Session[]) => string[] {
  let before = new Map<string, boolean>();
  return (list: Session[]): string[] => {
    const out = new Set<string>();
    const next = new Map<string, boolean>();
    for (const s of list) {
      const busy = isBusySession(s);
      next.set(s.name, busy);
      // Only true -> false is an edge; undefined (first observation) passes through. A row
      // whose alive dropped also becomes busy=false, so turns ended by a stop or an exit
      // are picked up here too.
      if (before.get(s.name) === true && !busy) {
        const prefix = sessionPrefix(s);
        if (prefix) out.add(prefix);
      }
    }
    before = next;
    return [...out];
  };
}

/**
 * Wires this into the store; App calls it exactly once. The return value unsubscribes and
 * is StrictMode-safe.
 *
 * Firing is coalesced per working copy and kept above a minimum gap. It is not stopped
 * when the FILES section is closed: the signal only advances a counter in the store, and
 * with no tree mounted nobody goes and reads (ProjectFiles unmounts when closed). That
 * shape is what makes the low-frequency mid-turn refresh cost zero requests when nobody
 * is looking.
 */
export function wireFilesSessionRefresh(): () => void {
  const detect = createTurnEndDetector();
  const lastAt = new Map<string, number>();
  const timers = new Map<string, number>();
  let busyPrefixes: string[] = [];
  let ticker = 0;

  const fire = (prefix: string) => {
    timers.delete(prefix);
    lastAt.set(prefix, Date.now());
    useFilesStore.getState().refreshUnder(prefix);
  };
  const schedule = (prefix: string) => {
    if (timers.has(prefix)) return; // already scheduled; that one run covers this call too
    const since = Date.now() - (lastAt.get(prefix) || 0);
    const delay = Math.max(COALESCE_MS, MIN_GAP_MS - since);
    timers.set(prefix, window.setTimeout(() => fire(prefix), delay));
  };

  // The low-frequency mid-turn refresh. It does not fire when nothing is visible (tab in
  // the background, workspace stopped): this refreshes for someone who is looking, it is
  // not a watcher.
  const tickWorking = () => {
    if (document.hidden || !wsRunning(useWorkspaceStore.getState().state)) return;
    for (const prefix of busyPrefixes) schedule(prefix);
  };

  const unsub = useSessionsStore.subscribe((s) => {
    for (const prefix of detect(s.sessions)) schedule(prefix);
    busyPrefixes = [
      ...new Set(s.sessions.filter(isBusySession).map(sessionPrefix).filter(Boolean)),
    ];
    // Fold the timer away once nothing is running, so it never becomes resident.
    if (busyPrefixes.length && !ticker) ticker = window.setInterval(tickWorking, WORKING_TICK_MS);
    else if (!busyPrefixes.length && ticker) {
      window.clearInterval(ticker);
      ticker = 0;
    }
  });
  return () => {
    unsub();
    if (ticker) window.clearInterval(ticker);
    ticker = 0;
    for (const t of timers.values()) window.clearTimeout(t);
    timers.clear();
  };
}
