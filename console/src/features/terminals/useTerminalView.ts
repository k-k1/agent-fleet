// The terminals the Terminal view lists under the active working set, and the shell count of each
// place. The view and every chip read it, so they cannot disagree.
import { useMemo } from "react";
import { useSessionsStore } from "../sessions/store.ts";
import { useReposStore } from "../repos/store.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { shellCounts, visibleTerminals } from "./visible.ts";

export function useTerminalView() {
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const wset = useActiveWorkingSet();
  return useMemo(() => {
    const visible = visibleTerminals(sessions, repos, wset);
    return { visible, counts: shellCounts(visible, repos) };
  }, [sessions, repos, wset]);
}
