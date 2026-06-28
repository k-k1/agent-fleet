import { useEffect, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, raw } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import NewSessionModal from "../NewSessionModal.jsx";
import ArchivedModal from "../ArchivedModal.jsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.js";
import { pinFirst } from "../../lib/listutil.js";

// stateInfo maps a session to its line-2 status chip (codicon + label).
const stateInfo = (s) => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", icon: "circle-slash", text: "フォルダ無し — 再開不可" };
    return { cls: "off", icon: "debug-pause", text: "停止中" };
  }
  if (s.kind === "shell") return { cls: "on", icon: "pulse", text: "起動中" };
  // claude (hooks), opencode (plugin) and codex (injected hooks) all report
  // working/idle. opencode/codex have no "question" state; an empty state = idle.
  switch (s.state) {
    case "working":
      return { cls: "working", icon: "loading", spin: true, text: "進行中…" };
    case "question":
      return { cls: "question", icon: "question", text: "質問あり" };
    default:
      return { cls: "on", icon: "check", text: "入力待ち" };
  }
};

const notify = (title, body) => {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
  }
};

// stamp formats a timestamp as MMDD-HHMM (matching the agent's claude --name), so
// shell rows show a launch time consistent with claude rows.
const stamp = (iso) => {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
};

// displayName: line-1 label = the claude session's --name (minus the "[AF] " tag);
// shell sessions (no --name) use "{repo} @MMDD-HHMM". The kind is shown by the
// line-2 badge, so no [AF]/[SH] prefix is needed.
const displayName = (s) => {
  if (s.kind !== "shell" && s.label) return s.label.replace(/^\[AF\]\s*/, "");
  return `${s.repo || s.name} @${stamp(s.createdAt)}`;
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
  const { sessionsKey, bumpSessions, bumpRepos, showTerminal, session } = useApp();
  const [sessions, setSessions] = useState([]);
  const [showModal, setShowModal] = useState(false);
  const [showArchived, setShowArchived] = useState(false);
  const [menuFor, setMenuFor] = useState(null); // session name whose ⋯ menu is open
  const prevStates = useRef({}); // name → last seen claude state, for arrival notifications
  const lastSer = useRef(""); // last serialized list, to skip no-op re-renders (cursor jank)

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

  // Delete all stopped sessions (forget the meta). The conversation log (jsonl) is
  // kept on disk, but the session leaves the list and is not in the archive.
  const deleteStopped = async () => {
    const stopped = sessions.filter((s) => !s.alive);
    if (stopped.length === 0) return;
    if (!confirm(`停止中の ${stopped.length} 件のセッションを一覧から削除しますか？\n（会話ログはディスクに残ります）`)) return;
    await Promise.all(
      stopped.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {})),
    );
    bumpSessions();
  };

  // Discard the conversation and start the same slot fresh (≠ resume).
  const recreate = async (name) => {
    if (!confirm(`セッション "${name}" の会話を破棄して新規に開始しますか？\n（元の会話は復元できません）`)) return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/recreate`, { method: "POST" });
    if (!res.ok) {
      alert("作り直しに失敗しました");
      return;
    }
    bumpSessions();
    showTerminal(name);
  };

  // Ask once for notification permission (best-effort; badges work regardless).
  useEffect(() => {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission().catch(() => {});
    }
  }, []);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api("api/sessions")
        .then((d) => {
          if (!alive) return;
          const list = d.sessions || [];
          // Notify on claude state arrivals (skip the session being viewed).
          const prev = prevStates.current;
          const seen = {};
          for (const s of list) {
            seen[s.name] = true;
            if (s.kind === "shell" || !s.alive) {
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
          // Only re-render when the list actually changed — an unconditional setState
          // every 4s repaints the tree and makes the cursor flicker "busy".
          const ser = JSON.stringify(list);
          if (ser !== lastSer.current) {
            lastSer.current = ser;
            setSessions(list);
          }
        })
        .catch(() => {
          if (alive && lastSer.current !== "[]") {
            lastSer.current = "[]";
            setSessions([]);
          }
        });
    load();
    const id = setInterval(load, 4000); // reflect state changes and TTL pruning
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [sessionsKey, session]);

  // Close the ⋯ menu on any outside click.
  useEffect(() => {
    if (!menuFor) return;
    const close = () => setMenuFor(null);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menuFor]);

  // Hoist the attached session to the top of the list (stable: the rest keep their
  // backend order) and mark it .pinned — highlighted, a pin badge in the top-right
  // corner, and sticky while the list scrolls.
  const ordered = pinFirst(sessions, (s) => s.name === session);

  return (
    <Section
      title="Sessions"
      actions={
        <>
          <button
            className="ghost"
            title="停止中をまとめて削除"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={deleteStopped}
          >
            <Icon name="clear-all" />
          </button>
          <button className="ghost" title="アーカイブを開く（復帰）" onClick={() => setShowArchived(true)}>
            <Icon name="archive" />
          </button>
          <button className="ghost" title="新規セッション" onClick={() => setShowModal(true)}>
            <Icon name="add" />
          </button>
        </>
      }
    >
      <ul className="list">
        {sessions.length === 0 && <li className="muted">セッションなし</li>}
        {ordered.map((s) => {
          const dead = !s.alive && s.resumable === false; // dir gone → can't resume
          const pinned = session === s.name; // currently attached → pinned to the top
          return (
          <li key={s.name} className={"session-row" + (pinned ? " active pinned" : "") + (s.alive ? "" : " stopped") + (dead ? " dead" : "")}>
            {pinned && <Icon name="pin" className="session-pin" title="接続中" />}
            <button
              className="session-btn"
              title={dead ? "作業フォルダが存在しないため再開できません" : !s.alive ? "停止中（⋯メニューから再開）" : s.dir || ""}
              disabled={!s.alive}
              onClick={() => s.alive && showTerminal(s.name)}
            >
              <span className="session-l1">
                <span className="session-display">{displayName(s)}</span>
              </span>
              <span className="session-l2">
                <span className="kind-tag">
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
            <div className="session-menu-wrap" onMouseDown={(e) => e.stopPropagation()}>
              <button
                className="session-menu-btn"
                title="メニュー"
                onClick={() => setMenuFor(menuFor === s.name ? null : s.name)}
              >
                <Icon name="ellipsis" />
              </button>
              {menuFor === s.name && (
                <div className="session-menu">
                  {!s.alive && !dead && (
                    <button
                      className="session-menu-item"
                      onClick={() => {
                        setMenuFor(null);
                        showTerminal(s.name);
                      }}
                    >
                      再開する
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
          onCreated={(name, cloned) => {
            bumpSessions();
            if (cloned) bumpRepos();
            showTerminal(name);
            setShowModal(false);
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
