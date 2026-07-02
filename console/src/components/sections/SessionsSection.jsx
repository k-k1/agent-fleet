import { useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { raw } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import NewSessionModal from "../NewSessionModal.jsx";
import SsmLoginModal from "../SsmLoginModal.jsx";
import ArchivedModal from "../ArchivedModal.jsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.js";
import { displayName, stateInfo } from "../../lib/sessionview.js";
import { agentOf } from "../../agents/registry.ts";
import { sessionPanes, ordClass, paneCount } from "../../lib/panebadge.js";
import { usePaneHover } from "../../lib/panehover.jsx";

const notify = (title, body) => {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
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
  const { sessions, bumpSessions, bumpRepos, bumpFiles, revealInFiles, showTerminal, showTerminalSplit, showChat, showChatSplit, session, newSessionTick, wsState, layout, setActivePane } = useApp();
  const running = wsState === "running"; // WS down → attach/resume/create are inert
  const [showModal, setShowModal] = useState(false);
  const { hover, setHover } = usePaneHover();

  // session name → panes showing it ([{ ordinal, id }]). Drives the per-row ordinal
  // badge, the "open in a pane" mark, and the click-to-focus target. Dormant when
  // there's a single pane — nothing to disambiguate (the pane shows no chip either).
  const multi = paneCount(layout) > 1;
  const openBy = useMemo(() => (multi ? sessionPanes(layout) : new Map()), [multi, layout]);

  // Open the New Session dialog when something elsewhere requests it (the onboarding
  // card). Skip the initial 0 so it doesn't pop on load.
  useEffect(() => {
    if (newSessionTick > 0) setShowModal(true);
  }, [newSessionTick]);
  const [showArchived, setShowArchived] = useState(false);
  const [resumeSsm, setResumeSsm] = useState(null); // { name, force } — SSM resume via login modal
  const menuRef = useRef(null); // wrap of the currently-open ⋯ menu (outside-click test)
  const [menuFor, setMenuFor] = useState(null); // session name whose ⋯ menu is open
  const prevStates = useRef({}); // name → last seen claude state, for arrival notifications

  // Archive: hide the session from the list but KEEP it (restorable via the archive
  // modal). Live sessions are stopped first. Backed by /archive (sets a flag; meta +
  // jsonl kept), as opposed to deletion which forgets the meta.
  const archive = async (s) => {
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" });
    if (!res.ok) {
      alert("アーカイブに失敗しました");
      return;
    }
    bumpSessions();
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
    if (!confirm(`停止中のセッションを整理します（${parts.join("・")}）。よろしいですか？`)) return;
    await Promise.all([
      ...keepable.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" }).catch(() => {})),
      ...ephemeral.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {})),
    ]);
    bumpSessions();
  };

  // Halt a running session into 停止中 (resumable): kills the live tmux but keeps
  // the meta, so the row stays listed and can be resumed later (≠ archive, which
  // hides it; ≠ recreate, which discards the conversation). The button counterpart
  // of quitting in the terminal. Frees a concurrency quota slot.
  const halt = async (name) => {
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" });
    if (!res.ok) {
      alert("停止に失敗しました");
      return;
    }
    bumpSessions();
    setTimeout(() => bumpSessions(), 1200);
  };

  // Discard the conversation and start the same slot fresh, then re-attach the
  // terminal so it resumes running (≠ resume of the old conversation). The Agent
  // /recreate kills tmux, clears state, and relaunches; surface its real error so
  // a genuine failure (e.g. the working dir is gone) is visible instead of being
  // masked as a generic message and leaving the row "stopped".
  const recreate = async (name) => {
    if (!confirm(`セッション "${name}" の会話を破棄して新規に開始しますか？\n（元の会話は復元できません）`)) return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/recreate`, { method: "POST" });
    if (!res.ok) {
      let msg = "作り直しに失敗しました";
      try {
        const j = await res.json();
        if (j?.error?.message) msg += "：" + j.error.message;
      } catch {}
      alert(msg);
      bumpSessions();
      return;
    }
    // Attaching is what launches the slot (handlePTY → ensureSessionTmux), so it
    // resumes running. Re-poll shortly after so the row flips to 起動中 fast
    // instead of lingering on 停止中 until the 4s poll.
    showTerminal(name);
    bumpSessions();
    setTimeout(() => bumpSessions(), 1200);
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
    const seen = {};
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
        if (s.state === "idle" && before === "working") notify("回答が返ってきました", s.name);
        else if (s.state === "question") notify("質問が来ています", s.name);
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
  useEffect(() => {
    if (!menuFor) return;
    const close = (e) => { if (!menuRef.current?.contains(e.target)) setMenuFor(null); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menuFor]);

  // The attached session is shown selected (highlighted) in place — no reordering,
  // so the rows keep their backend order and don't jump around as state changes.

  return (
    <Section
      title="Sessions"
      icon="terminal"
      actions={
        <>
          <button
            className="ghost"
            title="停止中をまとめてアーカイブ（shell/ssm は削除）"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={clearStopped}
          >
            <Icon name="clear-all" />
          </button>
          <button className="ghost" title="アーカイブを開く（復帰）" onClick={() => setShowArchived(true)}>
            <Icon name="archive" />
          </button>
          <button
            className="ghost"
            title={running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
            disabled={!running}
            onClick={() => setShowModal(true)}
          >
            <Icon name="add" />
          </button>
        </>
      }
    >
      <ul className="list">
        {sessions.length === 0 && <li className="muted">セッションなし</li>}
        {sessions.map((s) => {
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
            {/* Ordinal gutter: color-matched pane numbers for a session shown in one
                or more panes; click focuses that pane. Present on every row while
                split (empty when the session isn't open) so row text stays aligned;
                absent entirely with a single pane. */}
            {multi && (
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
            <button
              className="session-btn"
              title={
                dead
                  ? "作業フォルダが存在しないため再開できません"
                  : !s.alive
                    ? agentOf(s.kind).caps.transcript
                      ? "会話履歴を表示（クリック / 再開はチャット内から・Ctrl/中クリックで新ペイン）"
                      : "停止中（⋯メニューから再開）"
                    : s.dir
                      ? s.dir + "（Ctrl/中クリックで新ペインに開く）"
                      : "Ctrl/中クリックで新ペインに開く"
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
                if (e.ctrlKey || e.metaKey) showTerminalSplit(s.name);
                else showTerminal(s.name);
              }}
              // Middle-click opens the session in a freshly split pane. Suppress the
              // mousedown default so the browser doesn't start autoscroll instead.
              onMouseDown={(e) => e.button === 1 && e.preventDefault()}
              onAuxClick={(e) => {
                if (e.button !== 1) return;
                // Middle-click: alive → terminal split; stopped claude → chat-history split.
                if (s.alive) {
                  e.preventDefault();
                  showTerminalSplit(s.name);
                } else if (!dead && agentOf(s.kind).caps.transcript) {
                  e.preventDefault();
                  showChatSplit(s.name);
                }
              }}
            >
              <span className="session-l1">
                <span className="session-display">{displayName(s)}</span>
              </span>
              <span className="session-l2">
                <span className={"kind-tag kind-" + kindClass(s.kind)}>
                  <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                </span>
                <span className="session-name">{s.name}</span>
                {(() => {
                  const st = stateInfo(s);
                  return (
                    <span className={"session-state " + st.cls}>
                      <Icon name={st.icon} spin={st.spin} /> {st.text}
                    </span>
                  );
                })()}
              </span>
            </button>
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
                  {!s.alive && !dead && running && (
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
                      再開する
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
                      再ログインして再開
                    </button>
                  )}
                  {s.alive && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        halt(s.name);
                      }}
                    >
                      停止する（あとで再開できる）
                    </button>
                  )}
                  {s.remoteUrl && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        window.open(s.remoteUrl, "_blank", "noopener");
                      }}
                    >
                      リモートセッションを開く ↗
                    </button>
                  )}
                  <button
                    className="session-menu-item"
                    onClick={() => {
                      setMenuFor(null);
                      archive(s);
                    }}
                  >
                    アーカイブする（一覧から消す）
                  </button>
                  {!dead && (
                    <button
                      className="session-menu-item danger"
                      onClick={() => {
                        setMenuFor(null);
                        recreate(s.name);
                      }}
                    >
                      作り直す（会話を破棄）
                    </button>
                  )}
                </div>
              )}
            </div>
          </li>
          );
        })}
      </ul>

      {showModal && (
        <NewSessionModal
          onClose={() => setShowModal(false)}
          onCreated={(name, cloned, repo) => {
            bumpSessions();
            if (cloned) {
              bumpRepos();
              // Clone finished server-side before this fired, so the files exist:
              // refresh the Files tree (and expand to the new repo when we know it).
              if (repo) revealInFiles("repos/" + repo);
              else bumpFiles();
            }
            showTerminal(name);
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
    </Section>
  );
}
