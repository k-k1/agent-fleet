import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { raw } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import NewSessionModal from "../NewSessionModal.jsx";
import SsmLoginModal from "../SsmLoginModal.jsx";
import ArchivedModal from "../ArchivedModal.jsx";
import SessionTitleModal from "../SessionTitleModal.jsx";
import { useConfirm } from "../ConfirmProvider.jsx";
import { useToast } from "../ToastProvider.jsx";
import EmptyState from "../EmptyState.jsx";
import { useDismiss } from "../../lib/useDismiss.js";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.js";
import { displayName, stateInfo } from "../../lib/sessionview.js";
import { agentOf } from "../../agents/registry.ts";
import { sessionPanes, ordClass, paneCount } from "../../lib/panebadge.js";
import { usePaneHover } from "../../lib/panehover.jsx";
import type { Session } from "../../types/session.ts";

const notify = (title: string, body: string) => {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
  }
};

// Sessions are grouped by working directory. The header shows the dir's basename
// (full path on hover); sessions with no dir fall under "その他".
const groupLabel = (dir: string) => (dir ? dir.split("/").filter(Boolean).pop() || dir : "その他");

// Collapsed groups persist in localStorage (keyed by dir) so a folded folder stays
// folded across reloads.
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
  } catch {
    /* storage unavailable — just skip the cache */
  }
};

// Sessions: claude/shell sessions as two-line rows —
//   {claude --name}                 (line 1)
//     badge  セッション名  状態      (line 2)
// Clicking a row attaches it (→ terminal); a stopped (exited) session relaunches
// in the same session id (claude --resume). There is no kill button: quitting in
// the terminal flips a session to 停止中 (resumable), pruned after a retention
// window (agent-side TTL). The ⋯ menu holds destructive actions (作り直す). The
// list polls so state updates on its own.
export default function SessionsSection() {
  const { sessions, bumpSessions, bumpRepos, bumpFiles, revealInFiles, showTerminal, showTerminalSplit, showChat, showChatSplit, closeSessionPanes, closeNav, session, newSessionTick, wsState, layout, setActivePane } = useApp();
  const running = wsState === "running"; // WS down → attach/resume/create are inert
  const askConfirm = useConfirm();
  const toast = useToast();
  const [showModal, setShowModal] = useState(false);
  const { hover, setHover } = usePaneHover();

  // session name → panes showing it ([{ ordinal, id }]). Drives the per-row ordinal
  // badge, the "open in a pane" mark, and the click-to-focus target. Dormant when
  // there's a single pane — nothing to disambiguate (the pane shows no chip either).
  const multi = paneCount(layout) > 1;
  const openBy = useMemo(
    () => (multi ? sessionPanes(layout) : new Map<string, { ordinal: number; id: string }[]>()),
    [multi, layout],
  );

  // Group sessions by working dir. Groups are ordered by folder name ASCENDING (fixed),
  // and rows within a group by createdAt desc. Both are stable — the group order no
  // longer depends on the newest session's timestamp, so creating/removing a session
  // never reshuffles the groups; rows stay put too (createdAt doesn't change).
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
      // Empty dir ("その他") always sinks to the bottom; the rest sort ascending by the
      // displayed folder name (basename), tie-broken by full path for determinism.
      if (!a.dir !== !b.dir) return a.dir ? -1 : 1;
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

  // Open the New Session dialog when something elsewhere requests it (the onboarding
  // card). Skip the initial 0 so it doesn't pop on load.
  useEffect(() => {
    if (newSessionTick > 0) setShowModal(true);
  }, [newSessionTick]);
  const [showArchived, setShowArchived] = useState(false);
  const [resumeSsm, setResumeSsm] = useState<{ name: string; force: boolean } | null>(null); // SSM resume via login modal
  const [renaming, setRenaming] = useState<Session | null>(null); // session whose title-edit modal is open
  const menuRef = useRef<HTMLDivElement>(null); // wrap of the currently-open ⋯ menu (outside-click test)
  const [menuFor, setMenuFor] = useState<string | null>(null); // session name whose ⋯ menu is open
  const prevStates = useRef<Record<string, string | undefined>>({}); // name → last seen claude state

  // Archive: hide the session from the list but KEEP it (restorable via the archive
  // modal). Live sessions are stopped first. Backed by /archive (sets a flag; meta +
  // jsonl kept), as opposed to deletion which forgets the meta.
  const archive = async (s: Session) => {
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" });
    if (!res.ok) {
      toast("アーカイブに失敗しました");
      return;
    }
    closeSessionPanes(s.name); // it's leaving the list — close any pane still showing it
    bumpSessions();
    closeNav(); // mobile: acting from the left-pane menu closes the drawer (no-op on desktop)
  };

  // Delete: forget the session outright. Offered instead of archive for shell/ssm
  // (caps.ephemeral) — they carry no conversation worth keeping, so hiding-but-keeping
  // makes no sense. Backed by /stop (kills any live tmux + forgets the meta), which is
  // irreversible, so it's confirmed as a destructive action.
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
    closeSessionPanes(s.name); // it's leaving the list — close any pane still showing it
    bumpSessions();
    closeNav(); // mobile: acting from the left-pane menu closes the drawer (no-op on desktop)
  };

  // Clear all stopped sessions: agent sessions (claude/opencode/codex) are archived
  // (kept + restorable via the archive), while shell/ssm have no conversation worth
  // keeping, so they're deleted outright (/stop forgets the meta).
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
        // ephemeral (shell/ssm) are deleted outright → irreversible; agent sessions
        // are only archived (restorable), so a keep-only clear isn't destructive.
        danger: ephemeral.length > 0,
      }))
    )
      return;
    await Promise.all([
      ...keepable.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" }).catch(() => {})),
      ...ephemeral.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {})),
    ]);
    for (const s of stopped) closeSessionPanes(s.name); // all cleared rows leave the list
    bumpSessions();
    closeNav(); // mobile: close the drawer after acting from the left-pane menu
  };

  // Halt a running session into 停止中 (resumable): kills the live tmux but keeps
  // the meta, so the row stays listed and can be resumed later (≠ archive, which
  // hides it; ≠ recreate, which discards the conversation). The button counterpart
  // of quitting in the terminal. Frees a concurrency quota slot.
  const halt = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: "セッションを停止",
        body: `「${display}」を停止します。会話は保持され、あとで再開できます。`,
        confirmLabel: "停止する",
        danger: false, // resumable — a caution, not a destructive action
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" });
    if (!res.ok) {
      toast("停止に失敗しました");
      return;
    }
    bumpSessions();
    setTimeout(() => bumpSessions(), 1200);
    closeNav(); // mobile: close the drawer after acting from the left-pane menu
  };

  // Discard the conversation and start the slot fresh. The Agent /recreate now mints a
  // NEW session (fresh slug/sid, same title/dir/model) and pre-launches it live, so it
  // returns the new (alive) session — we open THAT: chat for claude, terminal for other
  // kinds. Surface a real error so a genuine failure (e.g. the working dir is gone) is
  // visible instead of leaving the row untouched.
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
        danger: false, // non-destructive: the old conversation is archived, not deleted
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
      bumpSessions();
      return;
    }
    const created = await res.json().catch(() => null);
    const newName = created?.name || name;
    // The old session was archived (退避) — close any pane still showing it before we
    // open the replacement, so a stale archived session doesn't linger in a split.
    if (newName !== name) closeSessionPanes(name);
    // Open the freshly created session: claude → chat mirror (live), other kinds →
    // terminal. Re-poll shortly so the new row appears fast.
    (created && agentOf(created.kind).caps.chat ? showChat : showTerminal)(newName);
    bumpSessions();
    setTimeout(() => bumpSessions(), 1200);
  };

  // Fork (分岐): branch this claude conversation into a NEW session that inherits the
  // history so far but then diverges — the source is left untouched (≠ recreate, which
  // starts empty and archives the old). The Agent runs `claude --resume <sid>
  // --fork-session`; on success we open the fork as chat in a fresh split so the source
  // (if visible) stays. Backend gates on a resumable conversation and returns a message
  // if there's nothing to fork yet.
  const fork = async (name: string) => {
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/fork`, { method: "POST" });
    const j = await res.json().catch(() => ({} as any));
    if (!res.ok || !j.name) {
      toast(j?.error?.message || j?.error || "分岐に失敗しました");
      return;
    }
    bumpSessions();
    showChatSplit(j.name);
    setTimeout(() => bumpSessions(), 1200);
    closeNav(); // mobile: close the drawer after acting from the menu
  };

  // Ask once for notification permission (best-effort; badges work regardless).
  useEffect(() => {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission().catch(() => {});
    }
  }, []);

  // Notify on claude state arrivals (skip the session being viewed). Driven by the
  // shared `sessions` list (polled in state.jsx), comparing each poll to the last.
  useEffect(() => {
    const prev = prevStates.current;
    const seen: Record<string, boolean> = {};
    for (const s of sessions) {
      seen[s.name] = true;
      // shell (and any kind with no working/idle state model) never transitions,
      // so there's no "回答が返ってきました" notification to raise for it.
      if (agentOf(s.kind).caps.fixedAliveChip || !s.alive) {
        prev[s.name] = s.state;
        continue;
      }
      const before = prev[s.name];
      if (before !== undefined && before !== s.state && s.name !== session) {
        if (s.state === "idle" && before === "working") notify("回答が返ってきました", displayName(s));
        else if (s.state === "question") notify("質問が来ています", displayName(s));
      }
      prev[s.name] = s.state;
    }
    for (const n of Object.keys(prev)) if (!seen[n]) delete prev[n];
  }, [sessions, session]);

  // Close the ⋯ menu on any outside click. Containment check (not
  // stopPropagation on the wrap): stopPropagation would swallow OTHER
  // dropdowns' document-level close listeners, so opening a REPOS 起動 menu
  // wouldn't close this one — both would stay open, both .pane-section would
  // lift to z-index:10, and the later REPOS section would paint over this menu.
  useDismiss(menuRef, !!menuFor, () => setMenuFor(null));

  // The attached session is shown selected (highlighted) in place — no reordering,
  // so the rows keep their backend order and don't jump around as state changes.

  return (
    <Section
      id="sessions"
      title="Sessions"
      icon="terminal"
      count={sessions.length}
      actions={
        <>
          <button
            className="ghost lblbtn"
            title="停止中をまとめてアーカイブ（shell/ssm は削除）"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={clearStopped}
          >
            <Icon name="clear-all" />
            <span className="lbl">整理</span>
          </button>
          <button className="ghost lblbtn" title="アーカイブを開く（復帰）" onClick={() => setShowArchived(true)}>
            <Icon name="archive" />
            <span className="lbl">アーカイブ</span>
          </button>
          <button
            className="ghost lblbtn"
            title={running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
            disabled={!running}
            onClick={() => setShowModal(true)}
          >
            <Icon name="add" />
            <span className="lbl">新規</span>
          </button>
        </>
      }
    >
      <ul className="list">
        {sessions.length === 0 && (
          <EmptyState
            icon="comment-discussion"
            message="セッションがありません"
            hint="エージェントを起動するとここに並びます"
            action={running ? { label: "新規セッション", icon: "add", onClick: () => setShowModal(true) } : undefined}
          />
        )}
        {groups.map((g) => {
          const isCollapsed = collapsed.has(g.dir);
          return (
            <Fragment key={g.dir || "__nodir"}>
              <li className="session-group">
                <button
                  type="button"
                  className={"session-group-btn" + (isCollapsed ? " collapsed" : "")}
                  onClick={() => toggleGroup(g.dir)}
                  title={g.dir || "作業ディレクトリなし"}
                >
                  <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} className="session-group-chevron" />
                  <Icon name="folder" className="session-group-folder" />
                  <span className="session-group-name">{groupLabel(g.dir)}</span>
                  <span className="session-group-count">{g.list.length}</span>
                </button>
              </li>
              {!isCollapsed &&
                g.list.map((s) => {
          const dead = !s.alive && s.resumable === false; // dir gone → can't resume
          const selected = session === s.name; // active pane's session → highlighted in place
          const opens = openBy.get(s.name) || []; // panes this session is shown in
          const open = opens.length > 0;
          const hl = open && hover?.session === s.name; // cross-highlight from a pane/map
          return (
          <li
            key={s.name}
            className={"session-row" + (selected ? " active" : "") + (open ? " open" : "") + (hl ? " hover" : "") + (s.alive ? "" : " stopped") + (dead ? " dead" : "") + (running ? "" : " ws-down")}
            onMouseEnter={open ? () => setHover({ session: s.name }) : undefined}
            onMouseLeave={open ? () => setHover(null) : undefined}
            // Right-click anywhere on the row opens the same ⋯ menu. preventDefault
            // suppresses the native context menu; the outside-click listener (a
            // mousedown handler) fires on this same right-click and would close the
            // menu, so open it on the trailing contextMenu event to win the race.
            onContextMenu={(e) => {
              e.preventDefault();
              setMenuFor(s.name);
            }}
          >
            <button
              className="session-btn"
              title={
                (dead
                  ? "作業フォルダが存在しないため再開できません"
                  : !s.alive
                    ? agentOf(s.kind).caps.transcript
                      ? "会話履歴を表示（クリック / 再開はチャット内から・Ctrl/中クリックで新ペイン）"
                      : "停止中（⋯メニューから再開）"
                    : s.dir
                      ? s.dir + "（Ctrl/中クリックで新ペインに開く）"
                      : "Ctrl/中クリックで新ペインに開く") + `\nID: ${s.name}`
              }
              // aria-disabled (not the disabled attribute): a truly disabled button
              // fires no events at all, so right-clicking a stopped session would
              // never reach the row's onContextMenu and the native menu would show.
              // A stopped claude session IS interactive (opens read-only chat history),
              // so only stopped non-claude (and dead) rows are aria-disabled.
              aria-disabled={((!s.alive && !(!dead && agentOf(s.kind).caps.transcript))) || undefined}
              // Ctrl/Cmd+click mirrors the middle-click: open in a freshly split pane.
              onClick={(e) => {
                if (!s.alive) {
                  // Stopped claude → open its conversation as read-only chat (no resume);
                  // resume happens from inside the chat. Ctrl/Cmd+click opens it in a
                  // fresh split, mirroring the alive path. Other kinds: resume via ⋯ menu.
                  if (!dead && agentOf(s.kind).caps.transcript) {
                    (e.ctrlKey || e.metaKey ? showChatSplit : showChat)(s.name);
                  }
                  return;
                }
                // Alive: a chat-capable kind (claude) opens the chat mirror by default —
                // the PTY still attaches in the background (CLI live), and the terminal
                // stays reachable via the ターミナル/チャット toggle. Other kinds open the
                // terminal directly.
                const split = e.ctrlKey || e.metaKey;
                const chat = agentOf(s.kind).caps.chat;
                (chat
                  ? split ? showChatSplit : showChat
                  : split ? showTerminalSplit : showTerminal)(s.name);
              }}
              // Middle-click opens the session in a freshly split pane. Suppress the
              // mousedown default so the browser doesn't start autoscroll instead.
              onMouseDown={(e) => e.button === 1 && e.preventDefault()}
              onAuxClick={(e) => {
                if (e.button !== 1) return;
                // Middle-click opens in a fresh split: alive claude → chat mirror (PTY
                // attached in bg), other alive kinds → terminal, stopped claude → chat
                // history.
                if (s.alive) {
                  e.preventDefault();
                  (agentOf(s.kind).caps.chat ? showChatSplit : showTerminalSplit)(s.name);
                } else if (!dead && agentOf(s.kind).caps.transcript) {
                  e.preventDefault();
                  showChatSplit(s.name);
                }
              }}
            >
              <span className="session-l1">
                <span className="session-display">{displayName(s)}</span>
              </span>
              {/* The slug (s.name) is the session's internal identity, not shown here
                  — the title (line 1) is the meaningful name. It's kept on the row's
                  tooltip (title attr, below) for support/debugging reference. */}
              <span className="session-l2">
                <span className={"kind-tag kind-" + kindClass(s.kind)}>
                  <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                </span>
                {(() => {
                  const st = stateInfo(s);
                  return (
                    <span className={"session-state " + st.cls}>
                      <Icon name={st.icon} spin={st.spin} /> {st.text}
                    </span>
                  );
                })()}
                {/* Branch drift: the working copy was checked out to a different branch
                    than this session started on (a checkout that slipped past the guard).
                    Can't be prevented at the git layer, so surface it loudly here. */}
                {s.branchDrift && (
                  <span
                    className="session-drift"
                    title={`このセッションの作業コピーは起動時のブランチ「${s.branch}」から「${s.currentBranch}」へ切り替わっています。稼働中エージェントの作業ツリーが入れ替わり、編集や差分が食い違っている可能性があります。`}
                  >
                    <Icon name="warning" /> {s.currentBranch}
                  </span>
                )}
              </span>
            </button>
            {/* Ordinal badges: color-matched pane numbers for a session shown in one
                or more panes; click focuses that pane. Aligned to the top-right (like
                the REPOS list) rather than a left gutter. Only present while split and
                the session is actually open. */}
            {multi && opens.length > 0 && (
              <div className="session-ords">
                {opens.map((o) => (
                  <button
                    key={o.id}
                    type="button"
                    className={"session-ord " + ordClass(o.ordinal)}
                    title={`ペイン${o.ordinal}にフォーカス`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setActivePane(o.id);
                    }}
                    onMouseEnter={() => setHover({ session: s.name, paneId: o.id })}
                    onMouseLeave={() => setHover(null)}
                  >
                    {o.ordinal}
                  </button>
                ))}
              </div>
            )}
            <div className="session-menu-wrap" ref={menuFor === s.name ? menuRef : undefined}>
              <button
                className="session-menu-btn"
                title="メニュー"
                onClick={() => setMenuFor(menuFor === s.name ? null : s.name)}
              >
                <Icon name="ellipsis" />
              </button>
              {menuFor === s.name && (
                <div className="session-menu">
                  {/* Resume: hidden for chat-capable kinds (claude) — their stopped row
                      opens the chat history, which carries its own 再開して続ける button,
                      so a separate "resume into terminal" item is redundant and off the
                      chat-default pattern. Kept for kinds with no in-chat resume
                      (opencode/codex/shell/ssm). */}
                  {!s.alive && !dead && running && !agentOf(s.kind).caps.chat && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        // SSM resumes through the login modal (SSO handshake before
                        // attach); other kinds attach directly (relaunch on attach).
                        if (s.kind === "ssm") setResumeSsm({ name: s.name, force: false });
                        else showTerminal(s.name);
                      }}
                    >
                      <Icon name="play" /> 再開する
                    </button>
                  )}
                  {!s.alive && !dead && running && s.kind === "ssm" && (
                    <button
                      className="session-menu-item"
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
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        halt(s.name, displayName(s));
                      }}
                    >
                      <Icon name="debug-stop" /> 停止する（あとで再開できる）
                    </button>
                  )}
                  {s.remoteUrl && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        window.open(s.remoteUrl, "_blank", "noopener");
                        closeNav(); // mobile: close the drawer after acting from the menu
                      }}
                    >
                      <Icon name="link-external" /> リモートセッションを開く
                    </button>
                  )}
                  <button
                    className="session-menu-item"
                    onClick={() => {
                      setMenuFor(null);
                      setRenaming(s);
                    }}
                  >
                    <Icon name="edit" /> タイトルを変更
                  </button>
                  {/* Fork: branch a claude conversation into a new session (source kept).
                      Needs the workspace up (it launches the fork) and a resumable
                      conversation — offered for claude (caps.fork), alive or stopped. */}
                  {agentOf(s.kind).caps.fork && !dead && running && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        fork(s.name);
                      }}
                    >
                      <Icon name="git-branch" /> 分岐（会話を引き継いで新規）
                    </button>
                  )}
                  {/* shell/ssm carry no conversation worth keeping → delete outright
                      (irreversible); agent sessions archive (hidden but restorable). */}
                  {agentOf(s.kind).caps.ephemeral ? (
                    <button
                      className="session-menu-item danger"
                      onClick={() => {
                        setMenuFor(null);
                        deleteSession(s);
                      }}
                    >
                      <Icon name="trash" /> 削除する
                    </button>
                  ) : (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        archive(s);
                      }}
                    >
                      <Icon name="archive" /> アーカイブする（一覧から消す）
                    </button>
                  )}
                  {!dead && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        recreate(s.name, displayName(s));
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
          onCreated={(name: string, cloned: boolean, repo: string, kind: string) => {
            bumpSessions();
            if (cloned) {
              bumpRepos();
              // Clone finished server-side before this fired, so the files exist:
              // refresh the Files tree (and expand to the new repo when we know it).
              if (repo) revealInFiles("repos/" + repo);
              else bumpFiles();
            }
            // A freshly created claude session opens as chat (its tmux/CLI is already
            // launched, so the mirror attaches live); other kinds open the terminal.
            (agentOf(kind).caps.chat ? showChat : showTerminal)(name);
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
            showTerminal(n);
            bumpSessions();
            setTimeout(() => bumpSessions(), 1200);
          }}
          onCancel={() => {
            setResumeSsm(null);
            bumpSessions();
          }}
        />
      )}

      {showArchived && (
        <ArchivedModal
          onClose={() => setShowArchived(false)}
          onRestored={() => bumpSessions()}
        />
      )}

      {renaming && (
        <SessionTitleModal
          name={renaming.name}
          title={renaming.title || ""}
          onClose={() => setRenaming(null)}
          onSaved={() => bumpSessions()}
        />
      )}
    </Section>
  );
}
