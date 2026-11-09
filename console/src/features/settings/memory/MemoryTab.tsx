// MemoryTab — Settings > Agent memory: Agent Fleet's own shared memory (ADR 0108), the memory
// every kind of agent reads and writes through af's memory_* tools. The version control of each
// kind's native memory (ADR 0022) is the neighbouring entry, MemorySnapshotsTab.
//
// This file is only the shell: one status header, a view-nav, and the view that is showing.
// The memory viewer, the change list and the proposals live in the Memory pane (features/memory,
// #3720); the same components are mounted there. This entry keeps the switches, the status and
// import/export, and links to the pane.
//
// While the workspace is stopped only Settings works: the switches are ui-prefs the member sets
// for the next start (#1735), and everything else needs the Agent, so those views offer Start.

import { useState } from "react";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { ViewNav, useSectionView, type ViewDef } from "./viewNav.tsx";
import { AgentMemoryStatus } from "./views/AgentMemoryStatus.tsx";
import { openMemoryView } from "../../memory/open.ts";
import { ImportExportView } from "./views/ImportExportView.tsx";
import { AgentSettingsView } from "./views/AgentSettingsView.tsx";

export const MEMORY_SECTION = "memory";
// Order is the nav order. "memories", "changes" and "proposals" moved to the Memory pane.
export const MEMORY_VIEWS = ["settings", "importexport"] as const;
const VIEWS: readonly string[] = MEMORY_VIEWS;

export function MemoryTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  // Bumped by any view that changed AF memory, so the status header re-reads.
  const [reload, setReload] = useState(0);
  const bump = () => setReload((n) => n + 1);
  const [view, setView] = useSectionView(MEMORY_SECTION, VIEWS, "settings", running);

  const defs: ViewDef[] = [
    { key: "settings", label: tr("mem.view_settings") },
    { key: "importexport", label: tr("mem.view_importexport") },
  ];

  return (
    <div className="mem-tab">
      <AgentMemoryStatus running={running} reload={reload} view={view} onOpenSettings={() => setView("settings")} />
      <p className="mem-open-view">
        <button
          type="button"
          className="linklike"
          onClick={() => {
            openMemoryView("memories");
          }}
        >
          {tr("mem.open_view")}
        </button>{" "}
        <span className="muted">{tr("mem.open_view_hint")}</span>
      </p>
      <ViewNav label={tr("mem.views_label")} views={defs} value={view} onChange={setView} prefix="mem-af" />
      <div role="tabpanel" id="mem-af-panel" aria-labelledby={`mem-af-tab-${view}`} className="mem-view">
        {view === "settings" ? (
          <AgentSettingsView running={running} reload={reload} />
        ) : !running ? (
          <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
            </Button>
          </EmptyState>
        ) : (
          <ImportExportView reload={reload} onChanged={bump} />
        )}
      </div>
    </div>
  );
}
