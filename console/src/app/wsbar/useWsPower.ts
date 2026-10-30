import { useEffect, useState } from "react";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";

// Workspace start / stop / restart for the bar. `running`, `busy` (wsStartBusy) and
// `powerStops` (wsPowerStops) are derived by the caller because the bar renders from them too.
export function useWsPower(running: boolean, busy: boolean, powerStops: boolean) {
  const startWs = useWorkspaceStore((s) => s.start);
  const stopWs = useWorkspaceStore((s) => s.stop);
  const restartWs = useWorkspaceStore((s) => s.restart);
  const openStart = useSessionsStore((s) => s.openStart);
  const askConfirm = useConfirm();
  const tr = useT();

  // The "start" entry while stopped: don't dead-end (launch flow Ph3) — confirm, start the
  // workspace, and open the hub once the 4s poll reports running. startQueued
  // survives the whole starting window; a second click while queued re-confirms
  // harmlessly (startWs is only re-fired from a genuinely stopped state).
  const [startQueued, setStartQueued] = useState(false);
  useEffect(() => {
    if (!startQueued) return;
    if (running) {
      setStartQueued(false);
      openStart();
    } else if (!busy) {
      // Settled on stopped/none/unknown (start failed, stopped externally, …): it will
      // never reach running now, so without clearing here the start button sticks on its
      // spinner. Right after the click start() queues "starting…" in the same batch, so
      // this cannot clear it early.
      setStartQueued(false);
    }
  }, [startQueued, running, busy, openStart]);
  const onStart = async () => {
    if (running) {
      openStart();
      return;
    }
    const ok = await askConfirm({
      title: tr("wsbar.confirm.start.title"),
      body: tr("wsbar.confirm.start.body"),
      confirmLabel: busy ? tr("wsbar.confirm.start.wait") : tr("wsbar.confirm.start.go"),
      danger: false,
    });
    if (!ok) return;
    setStartQueued(true);
    if (!busy) void startWs();
  };

  // Start is immediate; Stop is confirmed — it docker-removes the container, so
  // running sessions drop to "stopped" (resumable) and opencode web / preview disconnect.
  // Reversible (Start recreates; data persists in the bind mount), so it's a caution,
  // not a red destructive action.
  const onToggle = async () => {
    if (!powerStops) {
      void startWs();
      return;
    }
    const ok = await askConfirm({
      title: tr("wsbar.stop_ws"),
      // Stopping a start that has not landed yet loses nothing — there are no sessions
      // to drop and no preview to disconnect — so it gets its own body rather than the
      // running one's warning about both.
      body: tr(running ? "wsbar.confirm.stop.body" : "wsbar.confirm.stop.starting_body"),
      confirmLabel: tr("wsbar.confirm.stop.go"),
      danger: false,
    });
    if (ok) void stopWs();
  };

  // Apply the backend update: stop→start, keeping repos and everything on disk
  // (NOT recreate). Sessions stop and are resumable, so name the count up front —
  // that's the whole cost of the action.
  const onRestart = async () => {
    const live = useSessionsStore.getState().sessions.filter((s) => s.alive).length;
    const ok = await askConfirm({
      title: tr("wsbar.confirm.restart.title"),
      body: live > 0 ? tr("wsbar.confirm.restart.body_live", { n: live }) : tr("wsbar.confirm.restart.body"),
      confirmLabel: tr("wsbar.confirm.restart.go"),
      danger: false,
    });
    if (ok) void restartWs();
  };

  return { startQueued, onStart, onToggle, onRestart };
}
