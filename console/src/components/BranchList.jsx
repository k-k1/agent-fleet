import { useMemo, useState } from "react";

// relTime renders a unix seconds timestamp as a short Japanese "… ago" label.
export function relTime(unix) {
  if (!unix) return "";
  const s = Math.floor(Date.now() / 1000) - unix;
  if (s < 60) return "たった今";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}分前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}時間前`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}日前`;
  const mo = Math.floor(d / 30);
  if (mo < 12) return `${mo}ヶ月前`;
  return `${Math.floor(mo / 12)}年前`;
}

// BranchList: a filterable, newest-commit-first list of branches. Shared by the repo
// branch-switch modal (local branches, checkout on pick) and the new-session repo
// picker (remote branches, select on pick). Each branch row shows name, last-commit
// relative time, subject, and default/remote badges. `selected` highlights a branch;
// `onPick(name)` fires on click; `busy` (a name being applied) disables the list;
// `disableActive` prevents re-picking the highlighted branch.
//
// branches: null = loading, [] = none; each item is {name, unix, date, subject,
// default?, remote?, current?}.
export default function BranchList({ branches, selected, onPick, busy, disableActive, autoFocus }) {
  const [filter, setFilter] = useState("");

  const shown = useMemo(() => {
    const list = [...(branches || [])].sort((a, b) => (b.unix || 0) - (a.unix || 0));
    const q = filter.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (b) => b.name.toLowerCase().includes(q) || (b.subject || "").toLowerCase().includes(q),
    );
  }, [branches, filter]);

  return (
    <div className="branchlist-wrap">
      <input
        className="branch-filter"
        autoFocus={autoFocus}
        placeholder="フィルタ（ブランチ名 / コミット）"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      {branches === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : shown.length === 0 ? (
        <p className="muted pad">該当するブランチがありません</p>
      ) : (
        <ul className="branch-list">
          {shown.map((b) => {
            const active = b.name === selected || b.current;
            return (
              <li key={(b.remote ? "r:" : "l:") + b.name}>
                <button
                  type="button"
                  className={"branch-item" + (active ? " current" : "")}
                  disabled={!!busy || (active && disableActive)}
                  onClick={() => onPick(b.name)}
                  title={b.date || ""}
                >
                  <span className="bi-main">
                    <span className="bi-name">
                      {active ? "● " : ""}
                      {b.name}
                    </span>
                    {b.default && <span className="bi-remote">default</span>}
                    {b.remote && <span className="bi-remote">remote</span>}
                    <span className="bi-time">{relTime(b.unix)}</span>
                  </span>
                  {b.subject && <span className="bi-subject">{b.subject}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
