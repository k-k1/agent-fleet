// sessionRefs — what a ledger row's session is now (#1108). The ledger only records the slug a
// ticket was started in, so the name the user knows it by and whether it is still on the list
// have to be looked up: first in the live session list, then on the archived shelf. The shelf
// is only read when a slug is missing from the live list, since an archive listing reads every
// archived session's metadata.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { displayName } from "../../lib/sessionview.ts";
import type { Session } from "../../types/session.ts";

/** live = on the session list; archived = on the shelf, restorable; gone = neither (deleted,
 * or in the trash); unknown = not checked yet, or the shelf could not be read (a stopped
 * workspace), so nothing is claimed about it. */
export type SessionRefState = "live" | "archived" | "gone" | "unknown";

export interface ResolvedSessionRef {
  state: SessionRefState;
  /** The display name, or "" when it is not known — callers fall back to the slug. */
  title: string;
}

export function resolveSessionRef(name: string, live: Session[], archived: Session[] | null): ResolvedSessionRef {
  const s = live.find((x) => x.name === name);
  if (s) return { state: "live", title: displayName(s) };
  if (!archived) return { state: "unknown", title: "" };
  const a = archived.find((x) => x.name === name);
  if (a) return { state: "archived", title: displayName(a) };
  return { state: "gone", title: "" };
}

/** The archived shelf, read once per set of `missing` slugs (null until read, or when it could
 * not be). Nothing is read while every slug is on the live list. */
export function useArchivedFor(missing: string[]): Session[] | null {
  const [archived, setArchived] = useState<Session[] | null>(null);
  const key = [...missing].sort().join(" ");
  useEffect(() => {
    if (!key) return;
    let cancelled = false;
    void api("api/sessions/archived")
      .then((d) => {
        if (!cancelled) setArchived(d.sessions || []);
      })
      .catch(() => {
        if (!cancelled) setArchived(null);
      });
    return () => {
      cancelled = true;
    };
  }, [key]);
  return archived;
}
