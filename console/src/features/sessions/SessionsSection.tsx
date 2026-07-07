// SessionsSection — the left-rail sessions list, ported from the old console
// onto the zustand stores. Two-line rows grouped by working dir (collapse
// persists), state pills, ⋯/right-click menu with the full session lifecycle
// (halt/archive/delete/fork/recreate/rename/branch-rename/SSM resume), ordinal
// badges cross-highlighting panes, desktop notifications on state changes.
//
// TODO(P5): chat-mirror opens (stopped-claude history / alive-claude default)
// land with MirrorView; until then every open goes to the terminal (open.ts).
// TODO(P2b): New Session modal — the 新規 button is disabled with a hint.
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { raw } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { Pill } from "../../ui/Pill.tsx";
import type { PillTone } from "../../ui/Pill.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { sessionPanes, ordClass, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "./store.ts";
import { openSessionTerminal, openSessionTerminalSplit } from "./open.ts";
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

const STATE_TONE: Record<string, PillTone> = {
  on: "ok",
  working: "accent",
  question: "warn",
  bg: "warn",
  off: "muted",
  "off dead": "danger",
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
  const setActive = useLayoutStore((s) => s.setActive);
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const askConfirm = useConfirm();
  const toast = useToast();
  const { hover, setHover } = usePaneHover();

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
  const [showArchived, setShowArchived] = useState(false);
  const [resumeSsm, setResumeSsm] = useState<{ name: string; force: boolean } | null>(null);
  const [branchRenaming, setBranchRenaming] = useState<Session | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const prevStates = useRef<Record<string, string | undefined>>({});

  // Archive: hide from the list but KEEP it (restorable). Live sessions stop first.
  const archive = async (s: Session) => {
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" });
    if (!res.ok) {
      toast("アーカイブに失敗しました");
      return;
    }
    closeSessionPanes(s.name);
    void refreshSessions();
  };

  // Delete outright — offered for shell/ssm (caps.ephemeral): no conversation
  // worth keeping. /stop kills any live tmux + forgets the meta (irreversible).
  const deleteSession = async (s: Session) => {
    if (
      !(await askConfirm({
        title: "セッションを削除",
        body: `「${displayName(s)}」を削除します。この操作は取り消せません。`,
        confirmLabel: "削除する",
        danger: true,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" });
    if (!res.ok) {
      toast("削除に失敗しました");
      return;
    }
    closeSessionPanes(s.name);
    void refreshSessions();
  };

  // Clear all stopped: agent sessions archive (restorable); shell/ssm delete.
  const clearStopped = async () => {
    const stopped = sessions.filter((s) => !s.alive);
    if (stopped.length === 0) return;
    const ephemeral = stopped.filter((s) => agentOf(s.kind).caps.ephemeral);
    const keepable = stopped.filter((s) => !agentOf(s.kind).caps.ephemeral);
    const parts = [];
    if (keepable.length) parts.push(`${keepable.length} 件をアーカイブ`);
    if (ephemeral.length) parts.push(`shell/ssm ${ephemeral.length} 件を削除`);
    if (
      !(await askConfirm({
        title: "停止中のセッションを整理",
        body: `${parts.join("・")}します。`,
        confirmLabel: "整理する",
        danger: ephemeral.length > 0,
      }))
    )
      return;
    await Promise.all([
      ...keepable.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" }).catch(() => {}),
      ),
      ...ephemeral.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {}),
      ),
    ]);
    for (const s of stopped) closeSessionPanes(s.name);
    void refreshSessions();
  };

  // Halt into 停止中 (resumable): kills the live tmux, keeps the meta. Frees a
  // concurrency-quota slot. ≠ archive (hides), ≠ recreate (discards conversation).
  const halt = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: "セッションを停止",
        body: `「${display}」を停止します。会話は保持され、あとで再開できます。`,
        confirmLabel: "停止する",
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" });
    if (!res.ok) {
      toast("停止に失敗しました");
      return;
    }
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  // Recreate: the Agent mints a NEW live session (fresh slug, same title/dir/
  // model) and archives the old conversation — open the replacement.
  const recreate = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: "新しい会話で作り直す",
        body: (
          <>
            「{display}」を新しいセッションで開始します。
            <br />
            今の会話は<strong>アーカイブに退避</strong>し、あとで復帰できます。
          </>
        ),
        confirmLabel: "作り直す",
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/recreate`, { method: "POST" });
    if (!res.ok) {
      let msg = "作り直しに失敗しました";
      try {
        const j = await res.json();
        if (j?.error?.message) msg += "：" + j.error.message;
      } catch {}
      toast(msg);
      void refreshSessions();
      return;
    }
    const created = await res.json().catch(() => null);
    const newName = created?.name || name;
    if (newName !== name) closeSessionPanes(name);
    openSessionTerminal(newName); // TODO(P5): claude → chat mirror
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  // Fork (分岐): branch a claude conversation into a NEW session inheriting the
  // history; the source stays untouched. Opens the fork in a fresh split.
  const fork = async (name: string) => {
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/fork`, { method: "POST" });
    const j = await res.json().catch(() => ({}) as any);
    if (!res.ok || !j.name) {
      toast(j?.error?.message || j?.error || "分岐に失敗しました");
      return;
    }
    void refreshSessions();
    openSessionTerminalSplit(j.name); // TODO(P5): chat mirror split
    setTimeout(() => void refreshSessions(), 1200);
  };

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

  // Close the ⋯ menu on outside click / Escape (containment check, not
  // stopPropagation — so opening another menu closes this one).
  useDismiss(menuRef, !!menuFor, () => setMenuFor(null));

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
            onClick={clearStopped}
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
                g.list.map((s) => {
                  const dead = !s.alive && s.resumable === false; // dir gone → can't resume
                  const selected = activeSession === s.name;
                  const opens = openBy.get(s.name) || [];
                  const open = opens.length > 0;
                  const hl = open && hover?.session === s.name;
                  const st = stateInfo(s);
                  return (
                    <li
                      key={s.name}
                      className={
                        "sess-row" +
                        (selected ? " active" : "") +
                        (hl ? " hover" : "") +
                        (s.alive ? "" : " stopped") +
                        (dead ? " dead" : "")
                      }
                      onMouseEnter={open ? () => setHover({ session: s.name }) : undefined}
                      onMouseLeave={open ? () => setHover(null) : undefined}
                      // Right-click opens the same ⋯ menu (open on the trailing
                      // contextMenu event so the outside-click listener doesn't
                      // immediately close it).
                      onContextMenu={(e) => {
                        e.preventDefault();
                        setMenuFor(s.name);
                      }}
                    >
                      <button
                        type="button"
                        className="sess-btn"
                        title={
                          (dead
                            ? "作業フォルダが存在しないため再開できません"
                            : !s.alive
                              ? "停止中（クリックで開いて再開ボタン / Ctrl・中クリックで新ペイン）"
                              : (s.dir || "") + "（Ctrl/中クリックで新ペインに開く）") + `\nID: ${s.name}`
                        }
                        aria-disabled={dead || undefined}
                        onClick={(e) => {
                          if (dead) return;
                          // TODO(P5): alive/stopped claude opened the chat mirror here.
                          (e.ctrlKey || e.metaKey ? openSessionTerminalSplit : openSessionTerminal)(s.name);
                        }}
                        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                        onAuxClick={(e) => {
                          if (e.button !== 1 || dead) return;
                          e.preventDefault();
                          openSessionTerminalSplit(s.name);
                        }}
                      >
                        <span className="sess-l1">{displayName(s)}</span>
                        <span className="sess-l2">
                          <span className={"kind-tag kind-" + kindClass(s.kind)}>
                            <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                          </span>
                          <Pill tone={STATE_TONE[st.cls] || "muted"} icon={st.icon}>
                            {st.text}
                          </Pill>
                          {/* Branch drift: the working copy left the branch this session
                              started on — the agent's tree may be swapped out under it. */}
                          {s.branchDrift && (
                            <span
                              className="sess-drift"
                              title={`このセッションの作業コピーは起動時のブランチ「${s.branch}」から「${s.currentBranch}」へ切り替わっています。稼働中エージェントの作業ツリーが入れ替わり、編集や差分が食い違っている可能性があります。`}
                            >
                              <Icon name="warning" /> {s.currentBranch}
                            </span>
                          )}
                        </span>
                      </button>
                      {/* Ordinal badges: pane numbers for a session shown in ≥1 panes;
                          click focuses that pane. Only while split. */}
                      {multi && opens.length > 0 && (
                        <div className="sess-ords">
                          {opens.map((o) => (
                            <button
                              key={o.id}
                              type="button"
                              className={"rail-ord " + ordClass(o.ordinal)}
                              title={`ペイン${o.ordinal}にフォーカス`}
                              onClick={(e) => {
                                e.stopPropagation();
                                setActive(o.id);
                              }}
                              onMouseEnter={() => setHover({ session: s.name, paneId: o.id })}
                              onMouseLeave={() => setHover(null)}
                            >
                              {o.ordinal}
                            </button>
                          ))}
                        </div>
                      )}
                      <div className="sess-menu-wrap" ref={menuFor === s.name ? menuRef : undefined}>
                        <button
                          type="button"
                          className="sess-menu-btn"
                          title="メニュー"
                          onClick={() => setMenuFor(menuFor === s.name ? null : s.name)}
                        >
                          <Icon name="ellipsis" />
                        </button>
                        {menuFor === s.name && (
                          <div className="ui-menu sess-menu">
                            {/* Resume — kinds with no in-chat resume. SSM resumes through
                                the login modal (SSO handshake before attach). */}
                            {!s.alive && !dead && running && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  if (s.kind === "ssm") setResumeSsm({ name: s.name, force: false });
                                  else openSessionTerminal(s.name);
                                }}
                              >
                                <Icon name="play" /> 再開する
                              </button>
                            )}
                            {!s.alive && !dead && running && s.kind === "ssm" && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  setResumeSsm({ name: s.name, force: true });
                                }}
                              >
                                <Icon name="key" /> 再ログインして再開
                              </button>
                            )}
                            {s.alive && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  void halt(s.name, displayName(s));
                                }}
                              >
                                <Icon name="debug-stop" /> 停止する（あとで再開できる）
                              </button>
                            )}
                            {s.remoteUrl && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  window.open(s.remoteUrl, "_blank", "noopener");
                                }}
                              >
                                <Icon name="link-external" /> リモートセッションを開く
                              </button>
                            )}
                            <button
                              type="button"
                              className="ui-menu-item"
                              onClick={() => {
                                setMenuFor(null);
                                setRenaming(s);
                              }}
                            >
                              <Icon name="edit" /> タイトルを変更
                            </button>
                            {/* Worktree sessions only: rename the worktree's branch
                                (deferred naming); AI suggestion uses THIS session. */}
                            {s.worktree && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  setBranchRenaming(s);
                                }}
                              >
                                <Icon name="repo-forked" /> ブランチ名を変更
                              </button>
                            )}
                            {agentOf(s.kind).caps.fork && !dead && running && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  void fork(s.name);
                                }}
                              >
                                <Icon name="git-branch" /> 分岐（会話を引き継いで新規）
                              </button>
                            )}
                            {agentOf(s.kind).caps.ephemeral ? (
                              <button
                                type="button"
                                className="ui-menu-item danger"
                                onClick={() => {
                                  setMenuFor(null);
                                  void deleteSession(s);
                                }}
                              >
                                <Icon name="trash" /> 削除する
                              </button>
                            ) : (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  void archive(s);
                                }}
                              >
                                <Icon name="archive" /> アーカイブする（一覧から消す）
                              </button>
                            )}
                            {!dead && (
                              <button
                                type="button"
                                className="ui-menu-item"
                                onClick={() => {
                                  setMenuFor(null);
                                  void recreate(s.name, displayName(s));
                                }}
                              >
                                <Icon name="refresh" /> 作り直す（今の会話はアーカイブへ）
                              </button>
                            )}
                          </div>
                        )}
                      </div>
                    </li>
                  );
                })}
            </Fragment>
          );
        })}
      </ul>

      {showModal && (
        <NewSessionModal
          onClose={() => setShowModal(false)}
          onCreated={(name) => {
            // TODO(P2c): clone した場合の Repos/Files ツリー更新はセクション移植後に配線。
            // TODO(P5): claude はチャットミラーで開く。
            void refreshSessions();
            openSessionTerminal(name);
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
