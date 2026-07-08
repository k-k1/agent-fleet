// SessionsSection — the left-rail sessions list, ported from the old console onto
// the zustand stores. Two-line rows grouped by working dir (collapse persists),
// state pills, ⋯/right-click menu with the full session lifecycle. The row + menu
// (SessionRow) and the lifecycle ops (useSessionActions) are extracted so the same
// pieces render under the project tree's working-copy nodes; this section keeps the
// flat dir-grouped list, the new/archived/clear header, and the shared modals.
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { displayName } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { sessionPanes, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "./store.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { openSessionChat, openSessionTerminal } from "./open.ts";
import { useSessionActions } from "./useSessionActions.tsx";
import { SessionRow } from "./SessionRow.tsx";
import { ArchivedModal } from "./ArchivedModal.tsx";
import { SessionTitleModal } from "./SessionTitleModal.tsx";
import { BranchRenameModal } from "./BranchRenameModal.tsx";
import { SsmLoginModal } from "./SsmLoginModal.tsx";
import { NewSessionModal } from "./NewSessionModal.tsx";
import type { Session } from "../../types/session.ts";

const notify = (title: string, body: string) => {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
  }
};

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
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const layout = useLayoutStore((s) => s.layout);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const actions = useSessionActions();

  // The active pane's session → highlighted row; also skipped by notifications.
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

  const [showModal, setShowModal] = useState(false);
  // Global openNewSession signal (WS bar 新規 / onboarding card): the modal lives
  // here, so watch the tick and open. Skip the initial value (mount ≠ a request).
  const newSessionTick = useSessionsStore((s) => s.newSessionTick);
  const lastTickRef = useRef(newSessionTick);
  useEffect(() => {
    if (newSessionTick !== lastTickRef.current) {
      lastTickRef.current = newSessionTick;
      setShowModal(true);
    }
  }, [newSessionTick]);
  const [showArchived, setShowArchived] = useState(false);
  const [resumeSsm, setResumeSsm] = useState<{ name: string; force: boolean } | null>(null);
  const [branchRenaming, setBranchRenaming] = useState<Session | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const prevStates = useRef<Record<string, string | undefined>>({});

  // Ask once for notification permission (best-effort).
  useEffect(() => {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission().catch(() => {});
    }
  }, []);

  // Notify on claude state arrivals (skip the session being viewed).
  useEffect(() => {
    const prev = prevStates.current;
    const seen: Record<string, boolean> = {};
    for (const s of sessions) {
      seen[s.name] = true;
      if (agentOf(s.kind).caps.fixedAliveChip || !s.alive) {
        prev[s.name] = s.state;
        continue;
      }
      const before = prev[s.name];
      if (before !== undefined && before !== s.state && s.name !== activeSession) {
        if (s.state === "idle" && before === "working") notify("回答が返ってきました", displayName(s));
        else if (s.state === "question") notify("質問が来ています", displayName(s));
      }
      prev[s.name] = s.state;
    }
    for (const n of Object.keys(prev)) if (!seen[n]) delete prev[n];
  }, [sessions, activeSession]);

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
          <Button small variant="ghost" icon="archive" title="アーカイブを開く（復帰）" onClick={() => setShowArchived(true)}>
            アーカイブ
          </Button>
          <Button
            small
            variant="ghost"
            icon="add"
            title={running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
            disabled={!running}
            onClick={() => setShowModal(true)}
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
                    onRename={setRenaming}
                    onBranchRename={setBranchRenaming}
                    onResumeSsm={(name, force) => setResumeSsm({ name, force })}
                  />
                ))}
            </Fragment>
          );
        })}
      </ul>

      {showModal && (
        <NewSessionModal
          onClose={() => setShowModal(false)}
          onCreated={(name, cloned, repo, kind) => {
            void refreshSessions();
            if (cloned) {
              void useReposStore.getState().refresh();
              // Clone finished server-side: refresh the Files tree (reveal when known).
              if (repo) useFilesStore.getState().revealInFiles("repos/" + repo);
              else useFilesStore.getState().bump();
            }
            // A fresh claude session opens as chat (its CLI is already live).
            (agentOf(kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
            setShowModal(false);
          }}
        />
      )}
      {resumeSsm && (
        <SsmLoginModal
          name={resumeSsm.name}
          start
          force={resumeSsm.force}
          onReady={(n) => {
            setResumeSsm(null);
            openSessionTerminal(n);
            void refreshSessions();
            setTimeout(() => void refreshSessions(), 1200);
          }}
          onCancel={() => {
            setResumeSsm(null);
            void refreshSessions();
          }}
        />
      )}
      {showArchived && <ArchivedModal onClose={() => setShowArchived(false)} onRestored={() => void refreshSessions()} />}
      {renaming && (
        <SessionTitleModal
          name={renaming.name}
          title={renaming.title || ""}
          onClose={() => setRenaming(null)}
          onSaved={() => void refreshSessions()}
        />
      )}
      {branchRenaming && (
        <BranchRenameModal
          name={branchRenaming.name}
          branch={branchRenaming.branch || ""}
          onClose={() => setBranchRenaming(null)}
          onSaved={() => void refreshSessions()}
        />
      )}
    </Section>
  );
}
