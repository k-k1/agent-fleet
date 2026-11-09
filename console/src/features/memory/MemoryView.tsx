// MemoryView — the Memory pane (#3720): the memory viewer, the change list and the consolidator's
// proposals at pane size, instead of inside the Settings modal. It mounts the same components
// Settings used (MemoriesView, AgentChangesPanel, ProposalsPanel); nothing here re-implements
// them. Settings > Agent memory keeps the switches, the status and import/export.
//
// Which of the three is showing is pane content (`memoryView`), not React state: a tab switch
// unmounts the view, and a notice that asks for the proposals has to be able to retarget a pane
// that is already open (open.ts).
//
// While the workspace is stopped only the Agent could answer, so every view offers Start.
import type { ReactNode } from "react";
import { useState } from "react";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { ViewNav, type ViewDef } from "../settings/memory/viewNav.tsx";
import { MemoriesView } from "../settings/memory/views/MemoriesView.tsx";
import { AgentChangesPanel } from "../settings/memory/memoryChanges.tsx";
import { ProposalsPanel } from "../settings/memory/memoryProposals.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { MEMORY_VIEW_KEYS, type MemoryViewKey } from "./open.ts";

interface MemoryViewProps {
  paneId: string;
  view?: MemoryViewKey;
  headerActions?: ReactNode;
}

export function MemoryView({ paneId, view, headerActions }: MemoryViewProps) {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const running = wsState === "running";
  // Bumped by any view that changed memory, so the lists re-read.
  const [reload, setReload] = useState(0);
  const bump = () => setReload((n) => n + 1);
  const current: MemoryViewKey = view && MEMORY_VIEW_KEYS.includes(view) ? view : "memories";
  const pick = (v: string) => setPaneTarget(paneId, { content: { kind: "memory", memoryView: v as MemoryViewKey } });

  const defs: ViewDef[] = [
    { key: "memories", label: tr("mem.view_memories") },
    { key: "changes", label: tr("mem.view_changes") },
    { key: "proposals", label: tr("mem.view_proposals") },
  ];

  return (
    <div className="mview">
      <ViewHead actions={headerActions}>
        <span className="view-title">
          <Icon name="archive" /> {tr("pane.kind.memory")}
        </span>
        <button type="button" className="linklike" onClick={() => openSettings("memory")}>
          {tr("mem.mv_open_settings")}
        </button>
      </ViewHead>
      <div className="mview-body">
        <ViewNav label={tr("mem.views_label")} views={defs} value={current} onChange={pick} prefix="mview" />
        <div role="tabpanel" id="mview-panel" aria-labelledby={`mview-tab-${current}`} className="mem-view">
          {!running ? (
            <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
              <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
                {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
              </Button>
            </EmptyState>
          ) : current === "changes" ? (
            <AgentChangesPanel reload={reload} onChanged={bump} />
          ) : current === "proposals" ? (
            <ProposalsPanel reload={reload} onChanged={bump} />
          ) : (
            <MemoriesView reload={reload} onChanged={bump} onOpenChanges={() => pick("changes")} />
          )}
        </div>
      </div>
    </div>
  );
}
