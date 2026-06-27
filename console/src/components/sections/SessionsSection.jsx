import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, raw } from "../../api.js";
import Section from "../Section.jsx";
import NewSessionModal from "../NewSessionModal.jsx";

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
  const [menuFor, setMenuFor] = useState(null); // session name whose ⋯ menu is open

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

  useEffect(() => {
    let alive = true;
    const load = () =>
      api("api/sessions")
        .then((d) => alive && setSessions(d.sessions || []))
        .catch(() => alive && setSessions([]));
    load();
    const id = setInterval(load, 4000); // reflect running→停止中 and TTL pruning
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [sessionsKey]);

  // Close the ⋯ menu on any outside click.
  useEffect(() => {
    if (!menuFor) return;
    const close = () => setMenuFor(null);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menuFor]);

  return (
    <Section
      title="Sessions"
      actions={
        <button className="ghost" title="新規セッション" onClick={() => setShowModal(true)}>
          ＋
        </button>
      }
    >
      <ul className="list">
        {sessions.length === 0 && <li className="muted">セッションなし</li>}
        {sessions.map((s) => (
          <li key={s.name} className={"session-row" + (session === s.name ? " active" : "") + (s.alive ? "" : " stopped")}>
            <button className="session-btn" title={s.dir || ""} onClick={() => showTerminal(s.name)}>
              <span className="session-l1">
                <span className="session-display">{displayName(s)}</span>
              </span>
              <span className="session-l2">
                <span className="kind-tag">{s.kind === "shell" ? "shell" : "claude"}</span>
                <span className="session-name">{s.name}</span>
                <span className={"session-state " + (s.alive ? "on" : "off")}>
                  {s.alive ? "● 起動中" : "停止中 — クリックで再開"}
                </span>
              </span>
            </button>
            <div className="session-menu-wrap" onMouseDown={(e) => e.stopPropagation()}>
              <button
                className="session-menu-btn"
                title="メニュー"
                onClick={() => setMenuFor(menuFor === s.name ? null : s.name)}
              >
                ⋯
              </button>
              {menuFor === s.name && (
                <div className="session-menu">
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
                    className="session-menu-item danger"
                    onClick={() => {
                      setMenuFor(null);
                      recreate(s.name);
                    }}
                  >
                    作り直す（会話を破棄）
                  </button>
                </div>
              )}
            </div>
          </li>
        ))}
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
    </Section>
  );
}
