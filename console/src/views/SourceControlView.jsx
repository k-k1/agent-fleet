import { useCallback, useEffect, useMemo, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, raw, rawJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import BranchModal from "../components/BranchModal.jsx";

// SourceControlView is the per-repo git workbench, opened by clicking a repo in the
// Repos section. Left column: changed files (stage / unstage / discard), a commit
// box, and recent history. Right pane: the diff of the selected change OR — when a
// history commit is clicked — that commit's detail (header + file list + patch),
// codeleaf CommitDetail style. The repo comes from context.
// repo comes from the owning pane's descriptor (falls back to context scmRepo when
// rendered standalone).
export default function SourceControlView({ repo, wrap }) {
  const { scmRepo: ctxRepo, bumpRepos, bumpFiles, showTerminal } = useApp();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
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
        <RightPane sel={sel} diff={diff} commit={commit} wrap={wrap} />
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

function RightPane({ sel, diff, commit, wrap }) {
  if (sel?.kind === "commit") return <CommitDetail commit={commit} wrap={wrap} />;
  return <Diff text={diff} wrap={wrap} />;
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
function CommitDetail({ commit, wrap }) {
  if (!commit) return <pre className="diff muted">読み込み中…</pre>;
  if (commit.error) return <pre className="diff muted">(コミット取得失敗)</pre>;
  // Show the first 5 lines of the message body; the rest folds behind "さらに表示"
  // (long commit bodies would otherwise push the diff far down). The changed-file
  // list is dropped — the diff below already lists every file.
  const [bodyOpen, setBodyOpen] = useState(false);
  const bodyLines = commit.body ? commit.body.split("\n") : [];
  const clampBody = bodyLines.length > 5 && !bodyOpen;
  return (
    <div className="commit-detail">
      <div className="cd-head">
        <div className="cd-subject">{commit.subject || "(no message)"}</div>
        <div className="cd-meta">
          {commit.author} · {(commit.date || "").slice(0, 10)} · <code>{commit.short}</code>
        </div>
        {commit.body && (
          <pre className="cd-body">{clampBody ? bodyLines.slice(0, 5).join("\n") : commit.body}</pre>
        )}
        {bodyLines.length > 5 && (
          <button type="button" className="cd-more" onClick={() => setBodyOpen((o) => !o)}>
            {bodyOpen ? "折りたたむ" : `さらに表示（残り ${bodyLines.length - 5} 行）`}
          </button>
        )}
      </div>
      <Diff text={commit.diff} embedded truncated={commit.truncated} wrap={wrap} />
    </div>
  );
}

// ---- diff rendering: split a unified diff into per-file blocks (codeleaf-style
// fold), each with old/new line-number gutters parsed from the @@ hunk headers ----

// splitDiffFiles breaks a unified diff into one entry per file — each `diff --git`
// starts a new file. A bare diff with no such header becomes a single entry.
function splitDiffFiles(text) {
  const files = [];
  let cur = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git ") || line.startsWith("diff --cc ") || !cur) {
      cur = { lines: [] };
      files.push(cur);
    }
    cur.lines.push(line);
  }
  for (const f of files) f.path = diffPath(f.lines);
  return files;
}

// unquotePath decodes git's C-quoted path form. With core.quotepath on (the default),
// git wraps paths with non-ASCII bytes in double quotes and octal-escapes each byte
// (e.g. "docs/\343\201\202" → "docs/あ"). Reads from the opening quote to the closing
// one (ignoring any trailing \t…), turns escapes back into bytes, decodes as UTF-8.
// Unquoted paths are returned as-is (minus a trailing tab).
function unquotePath(s) {
  if (!s || s[0] !== '"') return s.replace(/\t.*$/, "");
  const bytes = [];
  const simple = { n: 10, t: 9, r: 13, a: 7, b: 8, f: 12, v: 11, '"': 34, "\\": 92 };
  for (let i = 1; i < s.length; i++) {
    const c = s[i];
    if (c === '"') break; // closing quote
    if (c === "\\") {
      const n = s[i + 1];
      if (n >= "0" && n <= "7") {
        let oct = "";
        let j = i + 1;
        while (j < s.length && oct.length < 3 && s[j] >= "0" && s[j] <= "7") oct += s[j++];
        bytes.push(parseInt(oct, 8));
        i = j - 1;
      } else {
        bytes.push(simple[n] !== undefined ? simple[n] : (n || "").charCodeAt(0));
        i++;
      }
    } else if (c.charCodeAt(0) < 128) {
      bytes.push(c.charCodeAt(0));
    } else {
      for (const b of new TextEncoder().encode(c)) bytes.push(b);
    }
  }
  try {
    return new TextDecoder("utf-8").decode(new Uint8Array(bytes));
  } catch {
    return s;
  }
}

// diffPath picks the display path: prefer the new side (+++ b/…), fall back to the
// old side (--- a/…) for deletions, then the `diff --git` header. Paths are un-quoted
// so non-ASCII (e.g. Japanese) filenames render as text, not octal escapes.
function diffPath(lines) {
  let plus = "", minus = "", git = "";
  for (const l of lines) {
    if (l.startsWith("+++ ")) plus = unquotePath(l.slice(4)).replace(/^b\//, "");
    else if (l.startsWith("--- ")) minus = unquotePath(l.slice(4)).replace(/^a\//, "");
    else if (l.startsWith("diff --git ")) git = l;
  }
  if (plus && plus !== "/dev/null") return plus;
  if (minus && minus !== "/dev/null") return minus;
  const m = git.match(/ b\/(.+)$/);
  return m ? unquotePath(m[1]) : git.replace(/^diff --(git|cc) /, "");
}

// diffRows turns a file's lines into renderable rows, tracking old/new line numbers
// across hunks. Redundant file-meta lines (diff/index/---/+++/mode) are dropped —
// the fold header already shows the path; a binary/empty body yields no code rows.
function diffRows(lines) {
  const rows = [];
  let oldLn = 0, newLn = 0;
  for (const text of lines) {
    if (text.startsWith("@@")) {
      const m = text.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      if (m) { oldLn = +m[1]; newLn = +m[2]; }
      rows.push({ type: "hunk", text });
    } else if (text.startsWith("Binary ")) {
      rows.push({ type: "meta", text });
    } else if (
      text.startsWith("diff ") || text.startsWith("index ") ||
      text.startsWith("+++ ") || text.startsWith("--- ") ||
      text.startsWith("new file") || text.startsWith("deleted file") ||
      text.startsWith("old mode") || text.startsWith("new mode") ||
      text.startsWith("similarity ") || text.startsWith("rename ") || text.startsWith("copy ")
    ) {
      // redundant meta — skip
    } else if (text.startsWith("+")) {
      rows.push({ type: "add", newLn, text });
      newLn++;
    } else if (text.startsWith("-")) {
      rows.push({ type: "del", oldLn, text });
      oldLn++;
    } else {
      rows.push({ type: "ctx", oldLn, newLn, text });
      oldLn++;
      newLn++;
    }
  }
  return rows;
}

function Diff({ text, embedded, truncated, wrap }) {
  // Real diffs carry an @@ hunk or a `diff --git` header; anything else (placeholder
  // "(差分なし)", error strings, empty) is shown as a plain message, not parsed.
  if (!text || !/(^|\n)(@@ |diff --(git|cc) )/.test(text)) {
    const msg = text && text.trim() ? text : embedded ? "(差分なし)" : "ファイルまたはコミットを選ぶと差分を表示";
    return <pre className="diff muted">{msg}</pre>;
  }
  const files = splitDiffFiles(text);
  return (
    <div className={"diff-files" + (wrap ? " wrap" : "")}>
      {files.map((f, i) => (
        <FileDiff key={(f.path || "") + i} file={f} />
      ))}
      {truncated && <div className="diff-trunc">…（差分が大きいため省略）</div>}
    </div>
  );
}

// FileDiff: one foldable file block — a sticky header (chevron + path + ±counts)
// over a line-numbered patch body. Defaults open; click the header to collapse.
function FileDiff({ file }) {
  const [open, setOpen] = useState(true);
  const rows = useMemo(() => diffRows(file.lines), [file.lines]);
  const adds = rows.reduce((n, r) => n + (r.type === "add" ? 1 : 0), 0);
  const dels = rows.reduce((n, r) => n + (r.type === "del" ? 1 : 0), 0);
  return (
    <section className="filediff">
      <button className="filediff-head" type="button" onClick={() => setOpen((o) => !o)}>
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="filediff-path" title={file.path}>{file.path || "(diff)"}</span>
        <span className="filediff-stat">
          {adds > 0 && <span className="add">+{adds}</span>}
          {dels > 0 && <span className="del">−{dels}</span>}
        </span>
      </button>
      {open && (
        <pre className="diff-body">
          {rows.map((r, i) => (
            <span key={i} className={"dl " + (r.type === "ctx" ? "" : r.type === "meta" ? "fileh" : r.type)}>
              <span className="dl-num">{r.oldLn ?? ""}</span>
              <span className="dl-num">{r.newLn ?? ""}</span>
              <span className="dl-text">{r.text}</span>
            </span>
          ))}
        </pre>
      )}
    </section>
  );
}
