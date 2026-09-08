// paneReconcile — closes the tabs of sessions that are no longer there.
//
// Archiving from the session menu closes that session's panes on the spot
// (useSessionActions), but that is only one of the ways a session leaves the list: the
// Cleanup modal, another browser tab or another device, the operator, a peer session and
// the Agent's own tidy-up all archive (or delete) sessions behind this tab's back. The tab
// that was showing one then stayed open forever, reading "No session" (pane.no_session)
// over a terminal that can never attach again.
//
// The session list is the only signal available — nothing pushes "session X was archived"
// — so the rule has to survive a list that lies:
//
//   - An EMPTY list is indistinguishable from a failure. The CP answers [] when its DB
//     mirror cannot be read, and the push path applies `data.sessions || []`, so a
//     malformed frame looks the same. Never close on one, and never record it either:
//     the next list would then read as "everything came back".
//   - A session must have been SEEN before it can be seen missing. A pane bound to a
//     session no list has ever carried is one that was just created here, and closing that
//     tab because a list request older than the session came back is the one way this
//     could destroy something the user wanted.
//
// The pass at boot is the deliberate exception to the second rule: a tab restored from the
// saved layout is evidence that the session existed when the layout was written, so the
// first trusted list is entitled to close it. It runs once per layout load, and only once
// the layout is hydrated — a push frame can arrive before the restored layout does, and
// judging tabs that are not on screen yet would let the stale ones through.
import { useLayoutStore } from "../../layout/store.ts";
import { allViews } from "../../layout/ops.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { displayName } from "../../lib/sessionview.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";
import { useSessionsStore } from "./store.ts";
import type { Session } from "../../types/session.ts";

/** Session names the layout binds a terminal (or chat mirror) view to. Documents opened
 *  from a session are NOT included: a plan or a diff keeps its content on its own and
 *  stays readable after the session it came from is gone. */
export function boundSessions(): Set<string> {
  const views = allViews(useLayoutStore.getState().layout);
  return new Set(
    views.flatMap((v) => (v.content.kind === "terminal" && v.session ? [v.session] : [])),
  );
}

/**
 * Wires this into the stores; App calls it exactly once. The return value unsubscribes and
 * is StrictMode-safe.
 */
export function wireSessionPaneReconcile(): () => void {
  // The last list that was trusted, by name — null until one has arrived. Rows and not
  // just names: the toast names the session the user lost a tab for, and the row is the
  // only place its title is left once it has dropped out of the list.
  let seen: Map<string, Session> | null = null;
  let bootPassDone = false;

  const close = (names: string[], labelOf: (name: string) => string | null) => {
    const bound = boundSessions();
    const hits = names.filter((n) => bound.has(n));
    if (hits.length === 0) return;
    useLayoutStore.getState().closeGoneSessionPanes(hits);
    // Say it, once for the batch. A tab vanishing on its own is otherwise indistinguishable
    // from the Console having lost it.
    const label = hits.length === 1 ? labelOf(hits[0]) : null;
    toast(
      label
        ? t("sess.tab_closed_gone", { name: label })
        : t("sess.tabs_closed_gone", { count: hits.length }),
      { kind: "info" },
    );
  };

  const bootPass = () => {
    if (bootPassDone || !seen) return;
    if (!useLayoutStore.getState().hydrated) return; // the restored layout has not landed yet
    bootPassDone = true;
    const known = seen;
    close([...boundSessions()].filter((n) => !known.has(n)), () => null);
  };

  const unSessions = useSessionsStore.subscribe((s, prev) => {
    if (s.sessions === prev.sessions) return;
    if (s.sessions.length === 0) return; // see the header: an empty list is not evidence
    const before = seen;
    const now = new Map(s.sessions.map((x) => [x.name, x] as const));
    seen = now;
    if (before) {
      const gone = [...before.keys()].filter((n) => !now.has(n));
      if (gone.length) close(gone, (n) => displayName(before.get(n) as Session));
    }
    bootPass();
  });

  // The other order: a list arrived before the layout was restored. Hydration is what makes
  // that list judgeable, so run the boot pass on it too.
  const unLayout = useLayoutStore.subscribe((s, prev) => {
    if (s.hydrated && !prev.hydrated) bootPass();
  });

  // A tenant (or identity) switch replaces both the session list and the layout, so the
  // ledger from the previous one says nothing about this one — and its boot pass has not
  // run yet either.
  const unTenant = useTenantStore.subscribe((s, prev) => {
    if (s.tenant === prev.tenant && s.identityRev === prev.identityRev) return;
    seen = null;
    bootPassDone = false;
  });

  return () => {
    unSessions();
    unLayout();
    unTenant();
  };
}
