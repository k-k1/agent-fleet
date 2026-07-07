// ArchivedModal — archived sessions (hidden from the list but kept on disk):
// restore (back as a stopped session) or delete permanently. Grouped by working
// dir, filterable, bulk-prunable by age (>30 days).
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, raw } from "../../core/api/client.ts";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { displayName } from "../../lib/sessionview.ts";
import type { Session } from "../../types/session.ts";

type ArchivedSession = Session & { started?: string };

interface ArchivedModalProps {
  onClose?: () => void;
  onRestored?: () => void;
}

const groupLabel = (dir: string) => (dir ? dir.split("/").filter(Boolean).pop() || dir : "その他");

// "Old" cutoff for bulk-prune. No createdAt = never pruned by age.
const OLD_DAYS = 30;
const isOld = (s: ArchivedSession, now: number) => {
  if (!s.createdAt) return false;
  const t = new Date(s.createdAt).getTime();
  return !isNaN(t) && now - t > OLD_DAYS * 86400_000;
};

export function ArchivedModal({ onClose, onRestored }: ArchivedModalProps) {
  const [items, setItems] = useState<ArchivedSession[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const askConfirm = useConfirm();
  const toast = useToast();

  const load = () =>
    api("api/sessions/archived")
      .then((d) => setItems(d.sessions || []))
      .catch(() => setItems([]));
  useEffect(() => {
    void load();
  }, []);

  const filtered = useMemo(() => {
    const list = items || [];
    const needle = q.trim().toLowerCase();
    if (!needle) return list;
    return list.filter((s) =>
      [displayName(s), s.dir || "", kindLabel(s.kind)].join(" ").toLowerCase().includes(needle),
    );
  }, [items, q]);

  // Group by dir; groups by newest session desc, rows by createdAt desc.
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
      onRestored?.();
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
      <div className="ui-modal-body">
        {items === null && <p className="sm-muted">読み込み中…</p>}
        {items && total === 0 && <p className="sm-muted">アーカイブはありません。</p>}
        {items && total > 0 && (
          <>
            <div className="arch-toolbar">
              <div className="arch-search">
                <Icon name="search" />
                <input
                  type="search"
                  placeholder="タイトル / フォルダ / 種別で絞り込み"
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  autoFocus
                />
              </div>
              <Button
                small
                variant="danger"
                icon="clear-all"
                disabled={busy || oldCount === 0}
                title={oldCount ? `${OLD_DAYS}日以上前の ${oldCount} 件を削除` : `${OLD_DAYS}日以上前のアーカイブはありません`}
                onClick={delOld}
              >
                古いものを削除{oldCount ? `（${oldCount}）` : ""}
              </Button>
            </div>

            {groups.length === 0 && <p className="sm-muted">一致するアーカイブはありません。</p>}
            <ul className="arch-list">
              {groups.map((g) => {
                const isCollapsed = collapsed.has(g.dir);
                return (
                  <li key={g.dir || "__nodir"}>
                    <button
                      type="button"
                      className="sess-group-btn"
                      onClick={() => toggleGroup(g.dir)}
                      title={g.dir || "作業ディレクトリなし"}
                    >
                      <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} />
                      <Icon name="folder" />
                      <span className="sess-group-name">{groupLabel(g.dir)}</span>
                      <span className="sess-group-count">{g.list.length}</span>
                    </button>
                    {!isCollapsed &&
                      g.list.map((s) => (
                        <div key={s.name} className="arch-row">
                          <div className="arch-info" title={"ID: " + s.name}>
                            <span className="arch-name">{displayName(s)}</span>
                            <span className="arch-sub">
                              <span className={"kind-tag kind-" + kindClass(s.kind)}>
                                <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                              </span>
                              {s.started ? " · " + s.started : ""}
                              {s.resumable === false ? " · フォルダ無し" : ""}
                            </span>
                          </div>
                          <div className="arch-actions">
                            <Button small disabled={busy} onClick={() => restore(s.name)}>
                              復帰
                            </Button>
                            <IconButton
                              icon="trash"
                              label="完全に削除"
                              variant="danger"
                              disabled={busy}
                              onClick={() => del(s.name, displayName(s))}
                            />
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
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          閉じる
        </Button>
      </footer>
    </Modal>
  );
}
