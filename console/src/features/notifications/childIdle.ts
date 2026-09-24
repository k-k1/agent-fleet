// childIdleMuted answers "should this device stay silent about <session> going idle", the
// childIdleNotify setting's single predicate. Both notification paths ask it — the CP feed
// (store.ts deliver) and the unsupported-feed fallback (useSessionNotifications) — so the two
// cannot disagree about what counts as a child.
//
// A child is origin "session" only: a fork (origin handoff) or a launched handoff proposal
// (origin user) also carries originSession but has a person on it. A session not in the list
// (not loaded yet, just deleted) is not muted — when in doubt, notify.
import { getSettings } from "../../lib/settings.ts";
import type { Session } from "../../types/session.ts";
import { useSessionsStore } from "../sessions/store.ts";

export function childIdleMuted(name: string, session?: Pick<Session, "origin">): boolean {
  if (getSettings().childIdleNotify) return false;
  const s = session ?? useSessionsStore.getState().sessions.find((x) => x.name === name);
  return s?.origin === "session";
}
