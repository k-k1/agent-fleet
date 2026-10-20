// MemoryTab — Settings > Agent memory: Agent Fleet's own shared memory (ADR 0108), the memory
// every kind of agent reads and writes through af's memory_* tools. The version control of each
// kind's native memory (ADR 0022) is the neighbouring entry, MemorySnapshotsTab.
//
// This file is only the shell: one status header, a view-nav, and the view that is showing.
// The views are standalone components (views/), so they can be mounted in a Memory view of their
// own later without a rewrite.
//
// While the workspace is stopped only Settings works: the switches are ui-prefs the member sets
// for the next start (#1735), and everything else needs the Agent, so those views offer Start.

import { useState } from "react";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { ViewNav, useSectionView, type ViewDef } from "./viewNav.tsx";
import { useReviewStatus } from "./memoryReviewQueue.ts";
import { AgentMemoryStatus } from "./views/AgentMemoryStatus.tsx";
import { MemoriesView } from "./views/MemoriesView.tsx";
import { ChangesView } from "./views/ChangesView.tsx";
import { ImportExportView } from "./views/ImportExportView.tsx";
import { AgentSettingsView } from "./views/AgentSettingsView.tsx";

export const MEMORY_SECTION = "memory";
// Order is the nav order; the first is the default when the workspace is running.
export const MEMORY_VIEWS = ["memories", "changes", "importexport", "settings"] as const;
const VIEWS: readonly string[] = MEMORY_VIEWS;

export function MemoryTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  // Bumped by any view that changed AF memory, so the status header re-reads.
  const [reload, setReload] = useState(0);
  const bump = () => setReload((n) => n + 1);
  const [view, setView] = useSectionView(MEMORY_SECTION, VIEWS, running ? "memories" : "settings", running);

  // The background review's queue: its length is the count beside "Changes".
  const { status: review, poll } = useReviewStatus(running, reload);
  const proposed = review?.pending ?? 0;

  const defs: ViewDef[] = [
    { key: "memories", label: tr("mem.view_memories") },
    {
      key: "changes",
      label: tr("mem.view_changes"),
      count: proposed,
      ariaLabel: tr("mem.view_changes_count", { n: proposed }),
    },
    { key: "importexport", label: tr("mem.view_importexport") },
    { key: "settings", label: tr("mem.view_settings") },
  ];

  return (
    <div className="mem-tab">
      <AgentMemoryStatus running={running} reload={reload} view={view} onOpenSettings={() => setView("settings")} />
      <ViewNav label={tr("mem.views_label")} views={defs} value={view} onChange={setView} prefix="mem-af" />
      <div role="tabpanel" id="mem-af-panel" aria-labelledby={`mem-af-tab-${view}`} className="mem-view">
        {view === "settings" ? (
          <AgentSettingsView running={running} />
        ) : !running ? (
          <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
            </Button>
          </EmptyState>
        ) : view === "changes" ? (
          <ChangesView reload={reload} onChanged={bump} review={review} poll={poll} />
        ) : view === "importexport" ? (
          <ImportExportView reload={reload} onChanged={bump} />
        ) : (
          <MemoriesView reload={reload} onChanged={bump} onOpenChanges={() => setView("changes")} />
        )}
      </div>
    </div>
  );
}
