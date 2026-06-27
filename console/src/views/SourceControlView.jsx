import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, rawJSON } from "../api.js";

// SourceControlView is the per-repo git workbench: changed files (stage / unstage /
// discard), a colored diff of the selected file, a commit box, and recent history.
// The repo comes from context (set when the user picks a repo in the Repos section).
export default function SourceControlView() {
  const { scmRepo, bumpRepos } = useApp();
  const enc = encodeURIComponent(scmRepo || "");
  const [status, setStatus] = useState(null);
  const [changes, setChanges] = useState([]);
  const [log, setLog] = useState([]);
  const [diff, setDiff] = useState("");
  const [selected, setSelected] = useState(null); // { path, staged }
  const [msg, setMsg] = useState("");
  const [all, setAll] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setStatus(await api(`api/repos/${enc}/status`));
    } catch {}
    try {
      const d = await api(`api/repos/${enc}/changes`);
      setChanges(d.changes || []);
    } catch {
      setChanges([]);
    }
    try {
      const d = await api(`api/repos/${enc}/log?limit=50`);
      setLog(d.commits || []);
    } catch {
      setLog([]);
    }
  }, [enc]);

  useEffect(() => {
    setDiff("");
    setSelected(null);
    refresh();
  }, [refresh]);

  const showDiff = async (path, staged) => {
    setSelected({ path, staged });
    try {
      const d = await api(`api/repos/${enc}/diff?path=${encodeURIComponent(path)}${staged ? "&staged=1" : ""}`);
      setDiff(d.diff && d.diff.length ? d.diff : "(差分なし)");
    } catch {
      setDiff("(diff 取得失敗)");
    }
  };

  const op = async (name, paths) => {
    await apiJSON(`api/repos/${enc}/${name}`, "POST", { paths });
    refresh();
  };

  const commit = async () => {
    if (!msg.trim()) {
      alert("コミットメッセージが必要です");
      return;
    }
    const r = await rawJSON(`api/repos/${enc}/commit`, "POST", { message: msg.trim(), all });
    if (r.ok) {
      setMsg("");
      refresh();
      bumpRepos();
    } else {
      const e = await r.json().catch(() => ({}));
      alert("commit 失敗: " + (e.error?.message || r.status));
    }
  };

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title">⎇ {scmRepo}</span>
        {status && (
          <span className="muted">
            {status.branch || "?"}
            {status.ahead ? ` ↑${status.ahead}` : ""}
            {status.behind ? ` ↓${status.behind}` : ""}
          </span>
        )}
        <span className="spacer" />
        <button className="ghost" title="更新" onClick={refresh}>
          ⟳
        </button>
      </header>
      <div className="scmbody">
        <div className="scmleft">
          <div className="sub-head">変更</div>
          <ul className="changes">
            {changes.length === 0 && <li className="muted">変更なし</li>}
            {changes.map((c) => (
              <ChangeRow
                key={c.path + (c.untracked ? "?" : "")}
                c={c}
                selected={selected?.path === c.path}
                onOpen={showDiff}
                onOp={op}
              />
            ))}
          </ul>

          <div className="commitbox">
            <textarea
              rows={2}
              placeholder="コミットメッセージ"
              value={msg}
              onChange={(e) => setMsg(e.target.value)}
            />
            <label className="muted">
              <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> 追跡中を全て stage (-a)
            </label>
            <button onClick={commit}>Commit</button>
          </div>

          <div className="sub-head">履歴</div>
          <ul className="log">
            {log.map((c) => (
              <li key={c.short} title={c.subject}>
                <code>{c.short}</code> <span className="subj">{c.subject}</span>
                <span className="muted">
                  {"  "}
                  {c.author} · {(c.date || "").slice(0, 10)}
                </span>
              </li>
            ))}
          </ul>
        </div>
        <Diff text={diff} />
      </div>
    </div>
  );
}

function ChangeRow({ c, selected, onOpen, onOp }) {
  const staged = !c.untracked && c.index !== " ";
  const tag = c.untracked ? "U" : staged ? c.index : c.worktree;
  const cls = c.untracked ? "untracked" : staged ? "staged" : "unstaged";
  return (
    <li className={"change" + (selected ? " active" : "")}>
      <span className={"chg " + cls}>{tag}</span>
      <span className="chg-name" title={c.path} onClick={() => onOpen(c.path, staged)}>
        {c.path}
      </span>
      <span className="chg-acts">
        {staged ? (
          <button className="icon" title="unstage" onClick={() => onOp("unstage", [c.path])}>
            −
          </button>
        ) : (
          <button className="icon" title="stage" onClick={() => onOp("stage", [c.path])}>
            ＋
          </button>
        )}
        {!c.untracked && (
          <button
            className="icon danger"
            title="変更を破棄"
            onClick={() => {
              if (confirm(`${c.path} の変更を破棄しますか？元に戻せません。`)) onOp("discard", [c.path]);
            }}
          >
            ⤺
          </button>
        )}
      </span>
    </li>
  );
}

function Diff({ text }) {
  if (!text) return <pre className="diff muted">ファイルを選ぶと差分を表示</pre>;
  return (
    <pre className="diff">
      {text.split("\n").map((line, i) => {
        let cls = "";
        if (line.startsWith("+") && !line.startsWith("+++")) cls = "add";
        else if (line.startsWith("-") && !line.startsWith("---")) cls = "del";
        else if (line.startsWith("@@")) cls = "hunk";
        return (
          <span key={i} className={"dl " + cls}>
            {line + "\n"}
          </span>
        );
      })}
    </pre>
  );
}
