// MemoryView — the Memory pane (#3720): the change list and the consolidator's proposals at pane
// size. The memories themselves are listed by the left pane's Memory view and each opens in a pane
// of its own (ADR 0141 decisions 4 and 6). Settings > Agent memory keeps the switches, the status
// and import/export.
//
// Which tab is showing is pane content (`memoryView`), not React state: a tab switch unmounts the
// view, and a notice that asks for the proposals has to be able to retarget a pane that is already
// open (open.ts). A pane saved on the retired Memories tab says where the memories went.
//
// While the workspace is stopped only the Agent could answer, so every view offers Start.
import type { ReactNode } from "react";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { ViewNav, type ViewDef } from "../settings/parts/viewNav.tsx";
import { AgentChangesPanel } from "../settings/memory/memoryChanges.tsx";
import { ProposalDetail, ProposalsPanel } from "../settings/memory/memoryProposals.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { MEMORY_VIEW_KEYS, type MemoryViewKey } from "./open.ts";
import { useMemoryStore } from "./store.ts";

interface MemoryViewProps {
  paneId: string;
  view?: MemoryViewKey | "memories";
  /** One proposal, shown alone at full width (proposals only). */
  proposalId?: string;
  headerActions?: ReactNode;
}

export function MemoryView({ paneId, view, proposalId, headerActions }: MemoryViewProps) {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const running = wsState === "running";
  // Bumped by any write to memory, here or in the left pane's Memory view, so both re-read.
  const reload = useMemoryStore((s) => s.version);
  const bump = useMemoryStore((s) => s.bump);
  const legacy = view === "memories";
  const current: MemoryViewKey = view && view !== "memories" && MEMORY_VIEW_KEYS.includes(view) ? view : "proposals";
  const pick = (v: string) => setPaneTarget(paneId, { content: { kind: "memory", memoryView: v as MemoryViewKey } });

  const defs: ViewDef[] = [
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
        <ViewNav label={tr("mem.views_label")} views={defs} value={legacy ? "" : current} onChange={pick} prefix="mview" />
        <div role="tabpanel" id="mview-panel" aria-labelledby={`mview-tab-${current}`} className="mem-view">
          {legacy ? (
            <EmptyState icon="archive" title={tr("mem.mv_moved_title")} hint={tr("mem.mv_moved_hint")}>
              <Button icon="archive" onClick={() => useLeftRail.getState().showView("memory")}>
                {tr("mem.mv_show_view")}
              </Button>
            </EmptyState>
          ) : !running ? (
            <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
              <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
                {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
              </Button>
            </EmptyState>
          ) : current === "changes" ? (
            <AgentChangesPanel reload={reload} onChanged={bump} />
          ) : current === "proposals" && proposalId ? (
            <div className="mem-changes mem-cs mview-one">
              <button type="button" className="linklike mview-back" onClick={() => pick("proposals")}>
                {tr("mem.mv_all_proposals")}
              </button>
              <div className="mem-diff">
                <ProposalDetail id={proposalId} version={reload} onDecided={bump} />
              </div>
            </div>
          ) : (
            <ProposalsPanel reload={reload} onChanged={bump} />
          )}
        </div>
      </div>
    </div>
  );
}
