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
  /** The row it was found in (live or archived), for its kind, folder and resumability. */
  session?: Session;
}

/** The shelf out of an api() answer, or null when it could not be read. api() RESOLVES an
 * error body instead of throwing (a stopped workspace answers 503 with `{error}`), and that is
 * not an empty shelf: taking it for one would call every archived session deleted. */
export function readShelf(d: unknown): Session[] | null {
  const body = d as { sessions?: unknown; error?: unknown } | null;
  if (!body || body.error || !Array.isArray(body.sessions)) return null;
  return body.sessions as Session[];
}

export function resolveSessionRef(name: string, live: Session[], archived: Session[] | null): ResolvedSessionRef {
  const s = live.find((x) => x.name === name);
  if (s) return { state: "live", title: displayName(s), session: s };
  if (!archived) return { state: "unknown", title: "" };
  const a = archived.find((x) => x.name === name);
  if (a) return { state: "archived", title: displayName(a), session: a };
  return { state: "gone", title: "" };
}

/** The archived shelf, read once per set of `missing` slugs (null until read, or when it could
 * not be). Nothing is read while every slug is on the live list. The answer is kept with the key
 * it was read for, so a shelf read for other slugs is never shown for these. */
export function useArchivedFor(missing: string[]): Session[] | null {
  const [shelf, setShelf] = useState<{ key: string; sessions: Session[] | null } | null>(null);
  const key = [...missing].sort().join(" ");
  useEffect(() => {
    if (!key) return;
    let cancelled = false;
    const done = (sessions: Session[] | null) => {
      if (!cancelled) setShelf({ key, sessions });
    };
    void api("api/sessions/archived")
      .then((d) => done(readShelf(d)))
      .catch(() => done(null));
    return () => {
      cancelled = true;
    };
  }, [key]);
  return shelf && shelf.key === key ? shelf.sessions : null;
}
