import { useMemo, useState } from "react";
import Icon from "./Icon.jsx";

// Shared git-diff rendering, split out of SourceControlView so the changes pane and the
// commit-detail pane render diffs identically. A unified diff is broken into per-file
// foldable blocks with old/new line-number gutters parsed from the @@ hunk headers.

export interface CommitData {
  error?: boolean;
  subject?: string;
  body?: string;
  author?: string;
  date?: string;
  short?: string;
  diff?: string;
  truncated?: boolean;
}
interface DiffFile {
  lines: string[];
  path?: string;
}
interface DiffRow {
  type: string;
  text: string;
  oldLn?: number;
  newLn?: number;
}

// splitDiffFiles breaks a unified diff into one entry per file — each `diff --git`
// starts a new file. A bare diff with no such header becomes a single entry.
function splitDiffFiles(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let cur: DiffFile | null = null;
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
function unquotePath(s: string): string {
  if (!s || s[0] !== '"') return s.replace(/\t.*$/, "");
  const bytes: number[] = [];
  const simple: Record<string, number> = { n: 10, t: 9, r: 13, a: 7, b: 8, f: 12, v: 11, '"': 34, "\\": 92 };
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
function diffPath(lines: string[]): string {
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
function diffRows(lines: string[]): DiffRow[] {
  const rows: DiffRow[] = [];
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

export function Diff({
  text,
  embedded,
  truncated,
  wrap,
}: {
  text?: string;
  embedded?: boolean;
  truncated?: boolean;
  wrap?: boolean;
}) {
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
function FileDiff({ file }: { file: DiffFile }) {
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

// CommitDetail renders one commit (codeleaf style): header (subject/body/meta), then
// the full colored patch (the diff below already lists every file).
export function CommitDetail({ commit, wrap }: { commit: CommitData | null; wrap?: boolean }) {
  const [bodyOpen, setBodyOpen] = useState(false);
  if (!commit) return <pre className="diff muted">読み込み中…</pre>;
  if (commit.error) return <pre className="diff muted">(コミット取得失敗)</pre>;
  // Show the first 5 lines of the message body; the rest folds behind "さらに表示".
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
