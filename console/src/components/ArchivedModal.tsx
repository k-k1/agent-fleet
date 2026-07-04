import { useEffect, useMemo, useState } from "react";
import { api, raw } from "../api.js";
import Icon from "./Icon.jsx";
import Modal from "./Modal.jsx";
import { useConfirm } from "./ConfirmProvider.jsx";
import { useToast } from "./ToastProvider.jsx";
import { kindIcon, kindLabel } from "../lib/sessionkind.js";
import { displayName } from "../lib/sessionview.js";
import type { Session } from "../types/session.ts";

// ArchivedModal lists archived sessions (hidden from the active list but kept on
// disk) and lets the user restore them (back into the list as a stopped session,
// click to resume) or delete them permanently. Backed by /api/sessions/archived,
// /restore, and /stop. Rows are grouped by working directory (like the left-pane
// Sessions list), filterable by an incremental search, and prunable in bulk by age.

// Archived sessions carry a `started` timestamp on top of the base Session shape.
type ArchivedSession = Session & { started?: string };

interface ArchivedModalProps {
  onClose?: () => void;
  onRestored?: () => void;
}

// Sessions with no dir fall under "その他"; the header shows the dir's basename.
const groupLabel = (dir: string) => (dir ? dir.split("/").filter(Boolean).pop() || dir : "その他");

// "Old" cutoff for the bulk-prune button: archived sessions created more than this
// many days ago. A session with no createdAt is treated as NOT old (never pruned by
// age — the button only targets clearly-dated rows).
const OLD_DAYS = 30;
const isOld = (s: ArchivedSession, now: number) => {
  if (!s.createdAt) return false;
  const t = new Date(s.createdAt).getTime();
  return !isNaN(t) && now - t > OLD_DAYS * 86400_000;
};

export default function ArchivedModal({ onClose, onRestored }: ArchivedModalProps) {
  const [items, setItems] = useState<ArchivedSession[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState(""); // incremental filter
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const askConfirm = useConfirm();
  const toast = useToast();

  const load = () =>
    api("api/sessions/archived")
      .then((d) => setItems(d.sessions || []))
      .catch(() => setItems([]));
  useEffect(() => {
    load();
  }, []);

  // Filter (incremental search over title / dir / kind) then group by working dir.
  // Groups are ordered by their newest session, rows within a group by createdAt desc.
  const filtered = useMemo(() => {
    const list = items || [];
    const needle = q.trim().toLowerCase();
    if (!needle) return list;
    return list.filter((s) =>
      [displayName(s), s.dir || "", kindLabel(s.kind)].join(" ").toLowerCase().includes(needle),
    );
  }, [items, q]);

  const groups = useMemo(() => {
    const by = new Map<string, ArchivedSession[]>();
    for (const s of filtered) {
      const key = s.dir || "";
      const list = by.get(key);
      if (list) list.push(s);
      else by.set(key, [s]);
    }
    const arr = [...by.entries()].map(([dir, list]) => {
      list.sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
      return { dir, list, newest: list[0]?.createdAt || "" };
    });
    arr.sort((a, b) => b.newest.localeCompare(a.newest));
    return arr;
  }, [filtered]);

  const toggleGroup = (dir: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(dir)) next.delete(dir);
      else next.add(dir);
      return next;
    });

  const restore = async (name: string) => {
    setBusy(true);
    try {
      const res = await raw(`api/sessions/${encodeURIComponent(name)}/restore`, { method: "POST" });
      if (!res.ok) {
        toast("復帰に失敗しました");
        return;
      }
      await load();
      onRestored && onRestored();
    } finally {
      setBusy(false);
    }
  };

  const del = async (name: string, display: string) => {
    const ok = await askConfirm({
      title: "アーカイブを完全に削除",
      body: `「${display}」を一覧から完全に削除します。会話ログのファイルは残ります。`,
      confirmLabel: "削除する",
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      await raw(`api/sessions/${encodeURIComponent(name)}/stop`, { method: "POST" }).catch(() => {});
      await load();
    } finally {
      setBusy(false);
    }
  };

  // Bulk-prune every archived session older than OLD_DAYS. Deletion (/stop) forgets the
  // meta but leaves the jsonl on disk, same as a single 削除. Confirmed once with the
  // count; no-op when nothing qualifies.
  const delOld = async () => {
    const now = Date.now();
    const old = (items || []).filter((s) => isOld(s, now));
    if (old.length === 0) return;
    const ok = await askConfirm({
      title: "古いアーカイブを一括削除",
      body: `${OLD_DAYS} 日以上前のアーカイブ ${old.length} 件を一覧から削除します。会話ログのファイルは残ります。`,
      confirmLabel: `${old.length} 件を削除`,
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      await Promise.all(
        old.map((s) => raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {})),
      );
      await load();
    } finally {
      setBusy(false);
    }
  };

  const oldCount = useMemo(() => {
    const now = Date.now();
    return (items || []).filter((s) => isOld(s, now)).length;
  }, [items]);

  const total = items?.length ?? 0;

  return (
    <Modal title="アーカイブ済みセッション" onClose={onClose}>
      <div className="modal-body">
        {items === null && <p className="muted">読み込み中…</p>}
        {items && total === 0 && <p className="muted">アーカイブはありません。</p>}
        {items && total > 0 && (
          <>
            <div className="archived-toolbar">
              <div className="archived-search">
                <Icon name="search" />
                <input
                  type="search"
                  placeholder="タイトル / フォルダ / 種別で絞り込み"
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  autoFocus
                />
              </div>
              <button
                className="ghost danger archived-prune"
                disabled={busy || oldCount === 0}
                title={oldCount ? `${OLD_DAYS}日以上前の ${oldCount} 件を削除` : `${OLD_DAYS}日以上前のアーカイブはありません`}
                onClick={delOld}
              >
                <Icon name="clear-all" />
                <span>古いものを削除{oldCount ? `（${oldCount}）` : ""}</span>
              </button>
            </div>

            {groups.length === 0 && <p className="muted">一致するアーカイブはありません。</p>}
            <ul className="archived-list">
              {groups.map((g) => {
                const isCollapsed = collapsed.has(g.dir);
                return (
                  <li key={g.dir || "__nodir"} className="archived-group-wrap">
                    <button
                      type="button"
                      className={"session-group-btn" + (isCollapsed ? " collapsed" : "")}
                      onClick={() => toggleGroup(g.dir)}
                      title={g.dir || "作業ディレクトリなし"}
                    >
                      <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} className="session-group-chevron" />
                      <Icon name="folder" className="session-group-folder" />
                      <span className="session-group-name">{groupLabel(g.dir)}</span>
                      <span className="session-group-count">{g.list.length}</span>
                    </button>
                    {!isCollapsed &&
                      g.list.map((s) => (
                        <div key={s.name} className="archived-row">
                          <div className="archived-info" title={"ID: " + s.name}>
                            <span className="archived-name">{displayName(s)}</span>
                            <span className="archived-sub muted">
                              <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                              {s.started ? " · " + s.started : ""}
                              {s.resumable === false ? " · フォルダ無し" : ""}
                            </span>
                          </div>
                          <div className="archived-actions">
                            <button disabled={busy} onClick={() => restore(s.name)}>
                              復帰
                            </button>
                            <button
                              className="ghost danger"
                              disabled={busy}
                              title="完全に削除"
                              onClick={() => del(s.name, displayName(s))}
                            >
                              <Icon name="trash" />
                            </button>
                          </div>
                        </div>
                      ))}
                  </li>
                );
              })}
            </ul>
          </>
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
