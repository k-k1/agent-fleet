// The session whose reply a Markdown body renders, as its $AF_WORK_DIR (lib/pathref.ts
// sessionWorkDir). Only the session's own surface (the mirror) provides it: the same text in a
// shared-session view or a document viewer has no throwaway directory to expand against, so
// `$AF_WORK_DIR/…` stays plain code there.
import { createContext, useContext } from "react";
import { sessionWorkDir } from "../../lib/pathref.ts";
import { isManagedSession, type Session } from "../../types/session.ts";

const SessionWorkDirContext = createContext<string>("");

export const SessionWorkDirProvider = SessionWorkDirContext.Provider;

/** Home-relative $AF_WORK_DIR of the enclosing session, "" outside one. */
export function useSessionWorkDir(): string {
  return useContext(SessionWorkDirContext);
}

/**
 * The $AF_WORK_DIR a session's replies mean. Only a Terminal session gets the variable (the
 * Agent injects ~/.af-work/<name> at tmux launch); a Managed one falls back to a directory named
 * after its working copy, which the session name does not give. Unknown metadata or a Managed
 * driver answers "" — plain code beats linking a stale ~/.af-work/<name> left by an earlier
 * Terminal run to the wrong file.
 */
export function mirrorWorkDir(name: string, meta: Pick<Session, "driver"> | null | undefined): string {
  return meta && !isManagedSession(meta) ? sessionWorkDir(name) : "";
}
