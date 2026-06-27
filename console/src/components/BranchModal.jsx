import { useEffect, useMemo, useState } from "react";
import { api, apiJSON } from "../api.js";

// relTime renders a unix seconds timestamp as a short Japanese "… ago" label.
function relTime(unix) {
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

// BranchModal: switch a repo's branch. Lists branches newest-commit-first (the Agent
// sorts) with a filter box over name + last-commit subject. Clicking one checks it
// out (a remote-only name DWIMs into a tracking branch). Backed by GET branches /
// POST checkout under api/repos/{name}.
export default function BranchModal({ repoName, onClose, onChecked }) {
  const [branches, setBranches] = useState(null); // null = loading
  const [err, setErr] = useState("");
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState("");

  useEffect(() => {
    let alive = true;
    api(`api/repos/${encodeURIComponent(repoName)}/branches`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) {
          setErr(d.error.message || d.error.code || "取得に失敗しました");
          setBranches([]);
        } else {
          setBranches(d.branches || []);
        }
      })
      .catch(() => {
        if (!alive) return;
        setErr("ブランチを取得できませんでした");
        setBranches([]);
      });
    return () => {
      alive = false;
    };
  }, [repoName]);

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const list = branches || [];
    if (!q) return list;
    return list.filter(
      (b) => b.name.toLowerCase().includes(q) || (b.subject || "").toLowerCase().includes(q),
    );
  }, [branches, filter]);

  const checkout = async (name) => {
    if (busy) return;
    setBusy(name);
    try {
      const res = await apiJSON(
        `api/repos/${encodeURIComponent(repoName)}/checkout`,
        "POST",
        { branch: name },
      );
      if (res && res.error) {
        alert("ブランチ切替に失敗: " + (res.error.message || res.error));
        return;
      }
      onChecked();
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onClose}>
      <div className="modal branch-modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-head">
          <h3 className="modal-title">ブランチ切替 — {repoName}</h3>
          <button type="button" className="icon" title="閉じる" onClick={onClose}>
            ✕
          </button>
        </header>
        <div className="modal-body">
          <input
            className="branch-filter"
            autoFocus
            placeholder="フィルタ（ブランチ名 / コミット）"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          {branches === null ? (
            <p className="muted pad">読み込み中…</p>
          ) : err ? (
            <p className="muted pad">{err}</p>
          ) : shown.length === 0 ? (
            <p className="muted pad">該当するブランチがありません</p>
          ) : (
            <ul className="branch-list">
              {shown.map((b) => (
                <li key={(b.remote ? "r:" : "l:") + b.name}>
                  <button
                    type="button"
                    className={"branch-item" + (b.current ? " current" : "")}
                    disabled={!!busy || b.current}
                    onClick={() => checkout(b.name)}
                    title={b.date || ""}
                  >
                    <span className="bi-main">
                      <span className="bi-name">
                        {b.current ? "● " : ""}
                        {b.name}
                      </span>
                      {b.remote && <span className="bi-remote">remote</span>}
                      <span className="bi-time">{relTime(b.unix)}</span>
                    </span>
                    {b.subject && <span className="bi-subject">{b.subject}</span>}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}
