// SessionsSection — the left-rail sessions list, ported from the old console onto
// the zustand stores. Two-line rows grouped by working dir (collapse persists),
// state pills, ⋯/right-click menu. The row + menu (SessionRow) and lifecycle ops
// (useSessionActions) are extracted so the same pieces render under the project
// tree's working-copy nodes; the session dialogs live app-level (SessionModals),
// so this section only owns the flat dir-grouped list and the new/archive/clear header.
import { Fragment, useMemo, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { sessionPanes, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "./store.ts";
import { useSessionUI } from "./ui.ts";
import { useSessionActions } from "./useSessionActions.tsx";
import { SessionRow } from "./SessionRow.tsx";
import type { Session } from "../../types/session.ts";

// Sessions group by working directory; header = the dir's basename.
const groupLabel = (dir: string) => (dir ? dir.split("/").filter(Boolean).pop() || dir : "その他");

// Collapsed groups persist (same key as the old console).
const COLLAPSE_KEY = "af-session-groups-collapsed";
const readCollapsed = (): Set<string> => {
  try {
    return new Set(JSON.parse(localStorage.getItem(COLLAPSE_KEY) || "[]"));
  } catch {
    return new Set();
  }
};
const writeCollapsed = (s: Set<string>) => {
  try {
    localStorage.setItem(COLLAPSE_KEY, JSON.stringify([...s]));
  } catch {}
};

export function SessionsSection() {
  const sessions = useSessionsStore((s) => s.sessions);
  const openNewSession = useSessionsStore((s) => s.openNewSession);
  const openArchived = useSessionUI((u) => u.openArchived);
  const layout = useLayoutStore((s) => s.layout);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const actions = useSessionActions();

  // The active pane's session → highlighted row.
  const activeSession = activePane(layout)?.session ?? null;

  // session name → panes showing it. Dormant when unsplit (nothing to disambiguate).
  const multi = paneCount(layout) > 1;
  const openBy = useMemo(
    () => (multi ? sessionPanes(layout) : new Map<string, { ordinal: number; id: string }[]>()),
    [multi, layout],
  );

  // Group by dir: groups sort ascending by folder name (stable — creating a
  // session never reshuffles), rows within by createdAt desc.
  const groups = useMemo(() => {
    const by = new Map<string, Session[]>();
    for (const s of sessions) {
      const key = s.dir || "";
      const list = by.get(key);
      if (list) list.push(s);
      else by.set(key, [s]);
    }
    const arr = [...by.entries()].map(([dir, list]) => {
      list.sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
      return { dir, list };
    });
    arr.sort((a, b) => {
      if (!a.dir !== !b.dir) return a.dir ? -1 : 1; // "その他" sinks to the bottom
      return groupLabel(a.dir).localeCompare(groupLabel(b.dir)) || a.dir.localeCompare(b.dir);
    });
    return arr;
  }, [sessions]);

  const [collapsed, setCollapsed] = useState<Set<string>>(readCollapsed);
  const toggleGroup = (dir: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(dir)) next.delete(dir);
      else next.add(dir);
      writeCollapsed(next);
      return next;
    });

  return (
    <Section
      id="sessions"
      title="Sessions"
      icon="terminal"
      count={sessions.length}
      actions={
        <>
          <Button
            small
            variant="ghost"
            icon="clear-all"
            title="停止中をまとめてアーカイブ（shell/ssm は削除）"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={actions.clearStopped}
          >
            整理
          </Button>
          <Button small variant="ghost" icon="archive" title="アーカイブを開く（復帰）" onClick={openArchived}>
            アーカイブ
          </Button>
          <Button
            small
            variant="ghost"
            icon="add"
            title={running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
            disabled={!running}
            onClick={openNewSession}
          >
            新規
          </Button>
        </>
      }
    >
      <ul className="sess-list">
        {sessions.length === 0 && (
          <EmptyState icon="comment-discussion" title="セッションがありません" hint="エージェントを起動するとここに並びます" />
        )}
        {groups.map((g) => {
          const isCollapsed = collapsed.has(g.dir);
          return (
            <Fragment key={g.dir || "__nodir"}>
              <li>
                <button
                  type="button"
                  className="sess-group-btn"
                  onClick={() => toggleGroup(g.dir)}
                  title={g.dir || "作業ディレクトリなし"}
                >
                  <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} />
                  <Icon name="folder" />
                  <span className="sess-group-name">{groupLabel(g.dir)}</span>
                  <span className="sess-group-count">{g.list.length}</span>
                </button>
              </li>
              {!isCollapsed &&
                g.list.map((s) => (
                  <SessionRow
                    key={s.name}
                    s={s}
                    selected={activeSession === s.name}
                    opens={openBy.get(s.name) || []}
                    multi={multi}
                    running={running}
                    actions={actions}
                  />
                ))}
            </Fragment>
          );
        })}
      </ul>
    </Section>
  );
}
