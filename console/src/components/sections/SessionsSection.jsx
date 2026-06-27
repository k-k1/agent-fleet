import { useEffect, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, raw } from "../../api.js";
import Section from "../Section.jsx";
import NewSessionModal from "../NewSessionModal.jsx";

// Sessions: list claude/shell sessions, open the New Session modal, attach
// (→ terminal), and stop. Clicking a session attaches it and shows the terminal;
// the active one is highlighted.
export default function SessionsSection() {
  const { sessionsKey, bumpSessions, bumpRepos, showTerminal, session } = useApp();
  const [sessions, setSessions] = useState([]);
  const [showModal, setShowModal] = useState(false);

  useEffect(() => {
    let alive = true;
    api("api/sessions")
      .then((d) => alive && setSessions(d.sessions || []))
      .catch(() => alive && setSessions([]));
    return () => {
      alive = false;
    };
  }, [sessionsKey]);

  const stop = async (name) => {
    await raw(`api/sessions/${encodeURIComponent(name)}/stop`, { method: "POST" });
    bumpSessions();
  };

  return (
    <Section
      title="Sessions"
      actions={
        <>
          <button className="ghost" title="新規セッション" onClick={() => setShowModal(true)}>
            ＋
          </button>
          <button className="ghost" title="更新" onClick={bumpSessions}>
            ⟳
          </button>
        </>
      }
    >
      <ul className="list">
        {sessions.length === 0 && <li className="muted">セッションなし</li>}
        {sessions.map((s) => (
          <li key={s.name} className={"session-row" + (session === s.name ? " active" : "")}>
            <button className="link grow" title={s.dir || ""} onClick={() => showTerminal(s.name)}>
              <span className="kind-tag">{s.kind === "shell" ? "sh" : "ai"}</span>
              {s.name}
            </button>
            <button className="icon danger" title="停止" onClick={() => stop(s.name)}>
              ✕
            </button>
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
