// The spaces list a view names its spaces from. Which sessions run in a space comes from the
// session list (Session.space); the display names come from GET /api/spaces, which is asked
// again when a session names a space the list does not know yet (made since the last answer) or
// the list is another workspace's, and after a transient failure with a growing wait. Until the
// answer arrives the folders stand in. Shared by every view that shows spaces, so none of them
// reads a stale list.
import { useEffect, useMemo } from "react";
import type { Session } from "../../types/session.ts";
import { failuresFor, spacesFor, spacesOwner, spacesRetryDelay, useSpacesStore } from "./store.ts";
import type { Space } from "./store.ts";

export function useSpaceNames(sessions: Session[]): Space[] {
  // Only a list asked for this workspace names anything: until the new owner's answer arrives
  // after a tenant or member switch, the folders stand in.
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const hasSpaceSessions = sessions.some((s) => !!s.space);
  const unknown = useMemo(() => {
    const known = new Set(spaces.map((s) => s.folder));
    const missing = new Set<string>();
    for (const s of sessions) if (s.space && !known.has(s.space)) missing.add(s.space);
    return [...missing].sort().join("\n");
  }, [sessions, spaces]);
  const storeOwner = useSpacesStore((s) => s.owner);
  const failures = useSpacesStore((s) => failuresFor(s, owner));
  const want = hasSpaceSessions && (unknown !== "" || storeOwner !== owner);
  useEffect(() => {
    if (!want) return;
    const t = setTimeout(() => void useSpacesStore.getState().refresh(), spacesRetryDelay(failures));
    return () => clearTimeout(t);
  }, [want, unknown, owner, storeOwner, failures]);
  return spaces;
}
