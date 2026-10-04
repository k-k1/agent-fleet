// notificationEffectOn answers "does this notification raise <effect> on this device" from the
// notification settings table (prefs.ts). Both notification paths ask it — the CP feed (store.ts
// deliver and the mark-read-on-arrival sweep) and the unsupported-feed fallback
// (useSessionNotifications) — so the two cannot disagree about a cell or about what counts as a
// child.
//
// A child is origin "session" only: a fork (origin handoff) or a launched handoff proposal
// (origin user) also carries originSession but has a person on it. A session not in the list
// (not loaded yet, just deleted) is not a child — when in doubt, notify.
import { getSettings } from "../../lib/settings.ts";
import type { Session } from "../../types/session.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { notifyCell, notifyRowOf, type NotifyEffect, type NotifyRow } from "./prefs.ts";

export interface EffectTarget {
  kind: string;
  target: { type: string; id: string };
}

function isChild(name: string, session?: Pick<Session, "origin">): boolean {
  const s = session ?? useSessionsStore.getState().sessions.find((x) => x.name === name);
  return s?.origin === "session";
}

export function notificationRow(n: EffectTarget, session?: Pick<Session, "origin">): NotifyRow {
  // Only answer-ready is split, so the session lookup is skipped for every other kind.
  const child = n.kind === "answer-ready" && n.target.type === "session" && isChild(n.target.id, session);
  return notifyRowOf(n.kind, child);
}

export function notificationEffectOn(n: EffectTarget, effect: NotifyEffect, session?: Pick<Session, "origin">): boolean {
  return notifyCell(getSettings(), notificationRow(n, session), effect);
}
