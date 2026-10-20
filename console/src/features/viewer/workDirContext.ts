// The session whose reply a Markdown body renders, as its $AF_WORK_DIR (lib/pathref.ts
// sessionWorkDir). Only the session's own surface (the mirror) provides it: the same text in a
// shared-session view or a document viewer has no throwaway directory to expand against, so
// `$AF_WORK_DIR/…` stays plain code there.
import { createContext, useContext } from "react";

const SessionWorkDirContext = createContext<string>("");

export const SessionWorkDirProvider = SessionWorkDirContext.Provider;

/** Home-relative $AF_WORK_DIR of the enclosing session, "" outside one. */
export function useSessionWorkDir(): string {
  return useContext(SessionWorkDirContext);
}
