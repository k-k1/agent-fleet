import { useWorkspaceStore } from "../core/store/workspace.ts";
import { resetBrowserRuntime } from "../features/browser/service.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useReposStore } from "../features/repos/store.ts";
import { useFilesStore } from "../features/files/store.ts";
import { useChatStore } from "../features/chat/store.ts";

// Refresh FILES (and repos/sessions/chat list on start) whenever the workspace
// actually flips running↔stopped — including external changes the 4s sync catches
// (admin stop, OOM, restart). Keyed on the transition, and on the RUNNING edge (not
// the "starting…" click), so trees load once the agent is really up. Transient "…"
// states — and the server-reported "starting" (ECS cold pull) — are unsettled and
// ignored, so the refresh fires exactly once on the starting→running flip.
// Returns the unsubscribe (StrictMode-safe).
export function wireWorkspaceRefresh(): () => void {
  const settle = (s: string) => (s === "running" ? "running" : s === "none" || s === "stopped" ? "stopped" : "");
  let prevRaw = useWorkspaceStore.getState().state;
  return useWorkspaceStore.subscribe((s) => {
    const from = settle(prevRaw);
    const to = settle(s.state);
    prevRaw = s.state;
    if (to === "" || to === from) return;
    resetBrowserRuntime(to === "running");
    useFilesStore.getState().bump();
    if (to === "running") {
      void useReposStore.getState().refresh();
      void useSessionsStore.getState().refresh();
      // Chat conversations are proxied into the workspace agent, so the list fails
      // while it's still starting — re-fetch once it's really up.
      useChatStore.getState().bumpList();
    } else {
      // The poller rests while stopped and a missed push leaves the last live list on screen
      // (alive rows that are not): the mirror's list is what a stopped workspace shows.
      void useSessionsStore.getState().refresh();
    }
  });
}
