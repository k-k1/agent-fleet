import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api } from "../../api.js";
import Section from "../Section.jsx";
import NewSessionModal from "../NewSessionModal.jsx";

// Sessions: claude/shell sessions as two-line rows —
//   [表示名]            起動日時
//     セッション名  (kind, 状態)
// Clicking a row attaches it (→ terminal); a stopped (exited) session relaunches
// in the same session id (claude --resume). There is no kill button: quitting in
// the terminal flips a session to 停止中 (resumable), and it is pruned after a
// retention window (agent-side TTL). The list polls so state updates on its own.
export default function SessionsSection() {
  const { sessionsKey, bumpSessions, bumpRepos, showTerminal, session } = useApp();
  const [sessions, setSessions] = useState([]);
  const [showModal, setShowModal] = useState(false);

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
        {sessions.map((s) => {
          const prefix = s.kind === "shell" ? "[SH]" : "[AF]";
          const display = `${prefix} ${s.repo || s.name}`;
          return (
            <li key={s.name} className={"session-row" + (session === s.name ? " active" : "") + (s.alive ? "" : " stopped")}>
              <button className="session-btn" title={s.dir || ""} onClick={() => showTerminal(s.name)}>
                <span className="session-l1">
                  <span className="session-display">{display}</span>
                  <span className="session-time">{s.started}</span>
                </span>
                <span className="session-l2">
                  <span className="kind-tag">{s.kind === "shell" ? "sh" : "ai"}</span>
                  <span className="session-name">{s.name}</span>
                  <span className={"session-state " + (s.alive ? "on" : "off")}>
                    {s.alive ? "● 起動中" : "停止中 — クリックで再開"}
                  </span>
                </span>
              </button>
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
    </Section>
  );
}
