// The session whose reply a Markdown body renders, as its $AF_WORK_DIR (lib/pathref.ts
// sessionWorkDir). Only the session's own surface (the mirror) provides it: the same text in a
// shared-session view or a document viewer has no throwaway directory to expand against, so
// `$AF_WORK_DIR/…` stays plain code there.
import { createContext, useContext } from "react";
import { sessionWorkDir } from "../../lib/pathref.ts";
import type { Session } from "../../types/session.ts";

const SessionWorkDirContext = createContext<string>("");

export const SessionWorkDirProvider = SessionWorkDirContext.Provider;

/** Home-relative $AF_WORK_DIR of the enclosing session, "" outside one. */
export function useSessionWorkDir(): string {
  return useContext(SessionWorkDirContext);
}

/**
 * The $AF_WORK_DIR a session's replies mean: ~/.af-work/<name>, for Terminal and Managed alike
 * (the Agent hands every kind its directory; paths.SessionWorkDir). Unknown metadata answers "" —
 * the mirror has not learned what the session is yet, and plain code beats linking a path that
 * may belong to something else.
 */
export function mirrorWorkDir(name: string, meta: Pick<Session, "driver"> | null | undefined): string {
  return meta ? sessionWorkDir(name) : "";
}
