import { useEffect, useState } from "react";
import { api, raw } from "../api.js";
import Icon from "./Icon.jsx";
import Modal from "./Modal.jsx";
import { kindIcon, kindLabel } from "../lib/sessionkind.js";

// ArchivedModal lists archived sessions (hidden from the active list but kept on
// disk) and lets the user restore them (back into the list as a stopped session,
// click to resume) or delete them permanently. Backed by /api/sessions/archived,
// /restore, and /stop.

export default function ArchivedModal({ onClose, onRestored }) {
  const [items, setItems] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    api("api/sessions/archived")
      .then((d) => setItems(d.sessions || []))
      .catch(() => setItems([]));
  useEffect(() => {
    load();
  }, []);

  const restore = async (name) => {
    setBusy(true);
    try {
      const res = await raw(`api/sessions/${encodeURIComponent(name)}/restore`, { method: "POST" });
      if (!res.ok) {
        alert("復帰に失敗しました");
        return;
      }
      await load();
      onRestored && onRestored();
    } finally {
      setBusy(false);
    }
  };

  const del = async (name) => {
    if (!confirm(`アーカイブ "${name}" を完全に削除しますか？\n（一覧から消えます。会話ログのファイルは残ります）`)) return;
    setBusy(true);
    try {
      await raw(`api/sessions/${encodeURIComponent(name)}/stop`, { method: "POST" }).catch(() => {});
      await load();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="アーカイブ済みセッション" onClose={onClose}>
      <div className="modal-body">
        {items === null && <p className="muted">読み込み中…</p>}
        {items && items.length === 0 && <p className="muted">アーカイブはありません。</p>}
        {items && items.length > 0 && (
          <ul className="archived-list">
            {items.map((s) => (
              <li key={s.name} className="archived-row">
                <div className="archived-info">
                  <span className="archived-name">{s.label ? s.label.replace(/^\[AF\]\s*/, "") : s.repo || s.name}</span>
                  <span className="archived-sub muted">
                    <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)} · {s.name}
                    {s.started ? " · " + s.started : ""}
                    {s.resumable === false ? " · フォルダ無し" : ""}
                  </span>
                </div>
                <div className="archived-actions">
                  <button disabled={busy} onClick={() => restore(s.name)}>
                    復帰
                  </button>
                  <button className="ghost danger" disabled={busy} title="完全に削除" onClick={() => del(s.name)}>
                    <Icon name="trash" />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose}>
          閉じる
        </button>
      </footer>
    </Modal>
  );
}
