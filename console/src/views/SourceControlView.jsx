import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, raw, rawJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import BranchModal from "../components/BranchModal.jsx";

// SourceControlView is the per-repo git workbench, opened by clicking a repo in the
// Repos section. Left column: changed files (stage / unstage / discard), a commit
// box, and recent history. Right pane: the diff of the selected change OR — when a
// history commit is clicked — that commit's detail (header + file list + patch),
// codeleaf CommitDetail style. The repo comes from context.
export default function SourceControlView() {
  const { scmRepo, bumpRepos, bumpFiles, showTerminal } = useApp();
  const enc = encodeURIComponent(scmRepo || "");
  const [status, setStatus] = useState(null);
  const [changes, setChanges] = useState([]);
  const [log, setLog] = useState([]);
  const [showBranch, setShowBranch] = useState(false);
  // sel drives the right pane: { kind:'file', path, staged } | { kind:'commit', sha }.
  const [sel, setSel] = useState(null);
  const [diff, setDiff] = useState(""); // file diff (kind:'file')
  const [commit, setCommit] = useState(null); // commit detail (kind:'commit')
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
    setSel(null);
    setDiff("");
    setCommit(null);
    refresh();
  }, [refresh]);

  const showDiff = async (path, staged) => {
    setSel({ kind: "file", path, staged });
    setCommit(null);
    try {
      const d = await api(`api/repos/${enc}/diff?path=${encodeURIComponent(path)}${staged ? "&staged=1" : ""}`);
      setDiff(d.diff && d.diff.length ? d.diff : "(差分なし)");
    } catch {
      setDiff("(diff 取得失敗)");
    }
  };

  const showCommit = async (sha) => {
    setSel({ kind: "commit", sha });
    setCommit(null);
    try {
      setCommit(await api(`api/repos/${enc}/show?sha=${encodeURIComponent(sha)}`));
    } catch {
      setCommit({ error: true });
    }
  };

  const op = async (name, paths) => {
    await apiJSON(`api/repos/${enc}/${name}`, "POST", { paths });
    refresh();
  };

  // Repo-level actions, moved here from the Repos list row: fetch, delete the
  // working copy (then leave the now-gone repo view), and branch switch (modal).
  const fetchRepo = async () => {
    await apiJSON(`api/repos/${enc}/fetch`, "POST", { prune: true });
    refresh();
    bumpRepos();
  };
  const del = async () => {
    if (!confirm(`ワーキングコピー "${scmRepo}" を削除しますか？（履歴・リモートはそのまま）`)) return;
    await raw(`api/repos/${enc}`, { method: "DELETE" });
    bumpRepos();
    bumpFiles();
    showTerminal();
  };

  const commitOp = async () => {
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
        <span className="view-title"><Icon name="repo" /> {scmRepo}</span>
        <button className="branch" type="button" title="ブランチ切替" onClick={() => setShowBranch(true)}>
          <Icon name="git-branch" /> {status?.branch || "?"} <Icon name="chevron-down" />
        </button>
        {status && (status.ahead || status.behind) ? (
          <span className="ab">
            {status.ahead ? `↑${status.ahead}` : ""}
            {status.behind ? `↓${status.behind}` : ""}
          </span>
        ) : null}
        <span className="spacer" />
        <button className="ghost" title="git fetch --prune" onClick={fetchRepo}>
          <Icon name="cloud-download" /> fetch
        </button>
        <button className="ghost" title="更新" onClick={refresh}>
          <Icon name="refresh" />
        </button>
        <button className="ghost danger" title="ワーキングコピーを削除" onClick={del}>
          <Icon name="trash" />
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
                selected={sel?.kind === "file" && sel.path === c.path}
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
            <button onClick={commitOp}>Commit</button>
          </div>

          <div className="sub-head">履歴</div>
          <ul className="log">
            {log.map((c) => (
              <li
                key={c.short}
                title={c.subject}
                className={"log-row" + (sel?.kind === "commit" && sel.sha === c.short ? " active" : "")}
                onClick={() => showCommit(c.short)}
              >
                <code>{c.short}</code> <span className="subj">{c.subject}</span>
                <span className="muted">
                  {"  "}
                  {c.author} · {(c.date || "").slice(0, 10)}
                </span>
              </li>
            ))}
          </ul>
        </div>
        <RightPane sel={sel} diff={diff} commit={commit} />
      </div>
      {showBranch && (
        <BranchModal
          repoName={scmRepo}
          onClose={() => setShowBranch(false)}
          onChecked={() => {
            setShowBranch(false);
            refresh();
            bumpRepos();
          }}
        />
      )}
    </div>
  );
}

function RightPane({ sel, diff, commit }) {
  if (sel?.kind === "commit") return <CommitDetail commit={commit} />;
  return <Diff text={diff} />;
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
            <Icon name="remove" />
          </button>
        ) : (
          <button className="icon" title="stage" onClick={() => onOp("stage", [c.path])}>
            <Icon name="add" />
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
            <Icon name="discard" />
          </button>
        )}
      </span>
    </li>
  );
}

// CommitDetail renders one commit (codeleaf style): header (subject/body/meta), the
// changed-file list, then the full colored patch.
function CommitDetail({ commit }) {
  if (!commit) return <pre className="diff muted">読み込み中…</pre>;
  if (commit.error) return <pre className="diff muted">(コミット取得失敗)</pre>;
  const files = commit.files || [];
  return (
    <div className="commit-detail">
      <div className="cd-head">
        <div className="cd-subject">{commit.subject || "(no message)"}</div>
        <div className="cd-meta">
          {commit.author} · {(commit.date || "").slice(0, 10)} · <code>{commit.short}</code>
        </div>
        {commit.body && <pre className="cd-body">{commit.body}</pre>}
        {files.length > 0 && (
          <ul className="cd-files">
            {files.map((f) => (
              <li key={f.path}>
                <span className={"cd-st cd-st-" + (f.status[0] || "x").toLowerCase()}>{f.status}</span>
                <span className="cd-path" title={f.path}>
                  {f.path}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <Diff text={commit.diff} embedded truncated={commit.truncated} />
    </div>
  );
}

function Diff({ text, embedded, truncated }) {
  if (!text)
    return embedded ? (
      <pre className="diff muted">(差分なし)</pre>
    ) : (
      <pre className="diff muted">ファイルまたはコミットを選ぶと差分を表示</pre>
    );
  return (
    <pre className="diff">
      {text.split("\n").map((line, i) => {
        let cls = "";
        if (line.startsWith("+") && !line.startsWith("+++")) cls = "add";
        else if (line.startsWith("-") && !line.startsWith("---")) cls = "del";
        else if (line.startsWith("@@")) cls = "hunk";
        else if (line.startsWith("diff --git") || line.startsWith("diff ")) cls = "fileh";
        return (
          <span key={i} className={"dl " + cls}>
            {line + "\n"}
          </span>
        );
      })}
      {truncated && <span className="dl muted">{"…（差分が大きいため省略）\n"}</span>}
    </pre>
  );
}
