import { useEffect, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, raw } from "../../api.js";
import Section from "../Section.jsx";
import NewSessionModal from "../NewSessionModal.jsx";

// stateInfo maps a session to its line-2 status chip.
const stateInfo = (s) => {
  if (!s.alive) {
    // A stopped claude whose working dir was deleted can't be resumed (archive only).
    if (s.resumable === false) return { cls: "off dead", text: "フォルダ無し — 再開不可" };
    return { cls: "off", text: "停止中 — クリックで再開" };
  }
  if (s.kind === "shell") return { cls: "on", text: "● 起動中" };
  // claude (hooks) and opencode (plugin) both report working/idle. opencode has no
  // "question" state; for either, an empty state means idle (awaiting input).
  switch (s.state) {
    case "working":
      return { cls: "working", text: "● 進行中…" };
    case "question":
      return { cls: "question", text: "❓ 質問あり" };
    default:
      return { cls: "on", text: "✓ 入力待ち" };
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
  const [menuFor, setMenuFor] = useState(null); // session name whose ⋯ menu is open
  const prevStates = useRef({}); // name → last seen claude state, for arrival notifications

  // Archive: remove the session from the list (keeps its conversation log). A live
  // session is stopped first so it doesn't linger invisibly. Backed by the stop
  // endpoint (kill tmux if any + forget meta), which does not delete the jsonl.
  const archive = async (s) => {
    const msg = s.alive
      ? `実行中のセッション "${s.name}" を停止して一覧から削除しますか？`
      : `セッション "${s.name}" を一覧から削除しますか？\n（会話ログは保持されます）`;
    if (!confirm(msg)) return;
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" });
    if (!res.ok) {
      alert("アーカイブに失敗しました");
      return;
    }
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
          setSessions(list);
        })
        .catch(() => alive && setSessions([]));
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
          const dead = !s.alive && s.resumable === false; // dir gone → can't resume
          return (
          <li key={s.name} className={"session-row" + (session === s.name ? " active" : "") + (s.alive ? "" : " stopped") + (dead ? " dead" : "")}>
            <button
              className="session-btn"
              title={dead ? "作業フォルダが存在しないため再開できません" : s.dir || ""}
              disabled={dead}
              onClick={() => !dead && showTerminal(s.name)}
            >
              <span className="session-l1">
                <span className="session-display">{displayName(s)}</span>
              </span>
              <span className="session-l2">
                <span className="kind-tag">
                  {s.kind === "shell" ? "🐚 shell" : s.kind === "opencode" ? "◆ opencode" : "✦ claude"}
                </span>
                <span className="session-name">{s.name}</span>
                {(() => {
                  const st = stateInfo(s);
                  return <span className={"session-state " + st.cls}>{st.text}</span>;
                })()}
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
                    className="session-menu-item"
                    onClick={() => {
                      setMenuFor(null);
                      archive(s);
                    }}
                  >
                    アーカイブする（一覧から消す）
                  </button>
                  {/* opencode resume is --continue (no fresh-start flag), so 作り直す
                      would not actually discard its conversation — hide it there. */}
                  {!dead && s.kind !== "opencode" && (
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
    </Section>
  );
}
