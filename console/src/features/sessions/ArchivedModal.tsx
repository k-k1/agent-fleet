// ArchivedModal — the shelf: archived sessions (hidden from the list but kept on
// disk). Restore (back as a stopped session), or delete — which reclaims the
// conversation through DELETE ?reclaim=1, so it is bundled to the cleanup trash
// (gz) first and stays restorable from the Cleanup modal's trash tab. Grouped by
// working dir, filterable, bulk-prunable by age (>7 days).
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, raw } from "../../core/api/client.ts";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { displayName } from "../../lib/sessionview.ts";
import { t, useLocale, useT } from "../../lib/i18n/index.ts";
import { compareText } from "../../lib/intl.ts";
import type { Session } from "../../types/session.ts";

type ArchivedSession = Session & { started?: string };

interface ArchivedModalProps {
  onClose?: () => void;
  onRestored?: () => void;
}

// Group heading, split into the base repo (a prefix) and a branch/folder label.
// A worktree's folder is "<repo>@<seg>", so for those show the base repo before
// the "@" plus the newest session's recorded branch — otherwise every worktree
// reads as a bare "temp/…" branch with no hint of which repo it belongs to. The
// archived endpoint never sends the `worktree` flag, so the "@" is the only wt
// signal. A plain clone has no repo prefix — just its folder name.
const groupHeading = (dir: string, head?: ArchivedSession): { repo?: string; label: string } => {
  const seg = dir ? dir.split("/").filter(Boolean).pop() || dir : "";
  if (!seg) return { label: t("arch.other") };
  const at = seg.indexOf("@");
  if (at >= 0) {
    return { repo: seg.slice(0, at) || undefined, label: head?.branch || seg.slice(at + 1) || seg };
  }
  return { label: seg };
};

// "Old" cutoff for bulk-prune. No createdAt = never pruned by age.
const OLD_DAYS = 7;
// 削除ロック（docs/log/45）済みは一括削除の対象外 — Agent が 403 で拒むので、件数にも入れない。
const isOld = (s: ArchivedSession, now: number) => {
  if (!s.createdAt || s.locked) return false;
  const ts = new Date(s.createdAt).getTime(); // ts: i18n の t を隠さない名前に
  return !isNaN(ts) && now - ts > OLD_DAYS * 86400_000;
};

export function ArchivedModal({ onClose, onRestored }: ArchivedModalProps) {
  const [items, setItems] = useState<ArchivedSession[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const askConfirm = useConfirm();
  const toast = useToast();
  const tr = useT();
  const locale = useLocale(); // groupHeading の t() をロケール切替に追従させる

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
      [displayName(s), s.name, s.dir || "", kindLabel(s.kind)].join(" ").toLowerCase().includes(needle),
    );
  }, [items, q]);

  // Group by dir. Groups sorted by repo name (asc), rows by createdAt desc.
  const groups = useMemo(() => {
    void locale; // dep: groupHeading（"arch.other"）をロケール切替で作り直す
    const by = new Map<string, ArchivedSession[]>();
    for (const s of filtered) {
      const key = s.dir || "";
      const list = by.get(key);
      if (list) list.push(s);
      else by.set(key, [s]);
    }
    const arr = [...by.entries()].map(([dir, list]) => {
      list.sort((a, b) => compareText(b.createdAt || "", a.createdAt || ""));
      const head = list[0];
      const heading = groupHeading(dir, head);
      return {
        dir,
        list,
        newest: head?.createdAt || "",
        repo: heading.repo,
        label: heading.label,
        repoKey: head?.repo || heading.label,
      };
    });
    arr.sort((a, b) => compareText(a.repoKey, b.repoKey) || compareText(b.newest, a.newest));
    return arr;
  }, [filtered, locale]);

  const allCollapsed = groups.length > 0 && groups.every((g) => collapsed.has(g.dir));
  const toggleAll = () =>
    setCollapsed(allCollapsed ? new Set() : new Set(groups.map((g) => g.dir)));

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
        toast(t("arch.restore_failed"));
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
      title: tr("arch.delete_title"),
      body: tr("arch.delete_body", { name: display }),
      confirmLabel: tr("common.delete_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // reclaim=1: gz 退避してから meta+jsonl を回収（掃除のごみ箱タブから復元可）。
      // 旧実装の /stop は行だけ消して jsonl を残す最悪の中間状態だった。
      const res = await raw(`api/sessions/${encodeURIComponent(name)}?reclaim=1`, { method: "DELETE" }).catch(
        () => null,
      );
      if (!res?.ok) toast(t("common.delete_failed"));
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
      title: tr("arch.bulk_title"),
      body: tr("arch.bulk_body", { days: OLD_DAYS, count: old.length }),
      confirmLabel: tr("arch.bulk_confirm", { count: old.length }),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // Sequential like the cleanup modal: each delete writes a gz archive on the
      // Agent — no gain from bursting mutations at it.
      for (const s of old) {
        await raw(`api/sessions/${encodeURIComponent(s.name)}?reclaim=1`, { method: "DELETE" }).catch(() => {});
      }
      await load();
    } finally {
      setBusy(false);
    }
  };

  const oldCount = useMemo(() => {
    const now = Date.now();
    return (items || []).filter((s) => isOld(s, now)).length;
  }, [items]);

  const restorable = useMemo(() => (items || []).filter((s) => s.resumable !== false), [items]);

  const restoreAll = async () => {
    if (restorable.length === 0) return;
    setBusy(true);
    let restored = 0;
    let failed = 0;
    try {
      // Restore sequentially: this can cover many archived sessions, and there is
      // no benefit in sending a burst of mutations to the workspace Agent.
      for (const s of restorable) {
        try {
          const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/restore`, { method: "POST" });
          if (res.ok) restored += 1;
          else failed += 1;
        } catch {
          failed += 1;
        }
      }
      await load();
      if (restored > 0) onRestored?.();
      if (failed > 0) {
        toast(t("arch.restored_some", { restored, failed }));
      } else {
        toast(t("arch.restored_n", { restored }), { kind: "success" });
      }
    } finally {
      setBusy(false);
    }
  };

  const total = items?.length ?? 0;

  // Once there are enough rows to fill the panel, pin the panel height so typing
  // in the filter scrolls the list instead of resizing/recentering the modal.
  const tall = total > 6;

  return (
    <Modal title={tr("arch.title")} onClose={onClose} className={tall ? "arch-modal arch-modal-tall" : "arch-modal"}>
      <div className="ui-modal-body">
        {items === null && <p className="sm-muted">{tr("chat.ph_loading")}</p>}
        {items && total === 0 && <p className="sm-muted">{tr("arch.empty")}</p>}
        {items && total > 0 && (
          <>
            <div className="arch-toolbar">
              <div className="arch-search">
                <Icon name="search" />
                <input
                  type="search"
                  placeholder={tr("arch.filter_ph")}
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                />
              </div>
              <Button
                small
                variant="ghost"
                icon={allCollapsed ? "expand-all" : "collapse-all"}
                disabled={groups.length === 0}
                title={allCollapsed ? tr("arch.expand_all") : tr("arch.collapse_all")}
                onClick={toggleAll}
              >
                {allCollapsed ? tr("arch.expand_all_short") : tr("arch.collapse_all_short")}
              </Button>
              <Button
                small
                variant="danger"
                icon="clear-all"
                disabled={busy || oldCount === 0}
                title={oldCount ? tr("arch.delete_old_title", { days: OLD_DAYS, count: oldCount }) : tr("arch.no_old", { days: OLD_DAYS })}
                onClick={delOld}
              >
                {tr("arch.delete_old")}{oldCount ? tr("common.paren", { v: oldCount }) : ""}
              </Button>
            </div>

            {groups.length === 0 && <p className="sm-muted">{tr("arch.no_match")}</p>}
            <ul className="arch-list">
              {groups.map((g) => {
                const isCollapsed = collapsed.has(g.dir);
                return (
                  <li key={g.dir || "__nodir"}>
                    <button
                      type="button"
                      className="sess-group-btn"
                      onClick={() => toggleGroup(g.dir)}
                      title={g.dir || tr("arch.no_workdir")}
                    >
                      <Icon name={isCollapsed ? "chevron-right" : "chevron-down"} />
                      <Icon name="folder" />
                      <span className="sess-group-name">
                        {g.repo && <span className="sess-group-repo">{g.repo}</span>}
                        <span className="sess-group-branch">{g.label}</span>
                      </span>
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
                              {s.resumable === false ? tr("arch.folder_missing_suffix") : ""}
                            </span>
                          </div>
                          <div className="arch-actions">
                            <Button small disabled={busy} onClick={() => restore(s.name)}>
                              {tr("arch.restore")}
                            </Button>
                            <IconButton
                              icon={s.locked ? "lock" : "trash"}
                              label={s.locked ? tr("srow.locked_hint") : tr("arch.delete_perm")}
                              variant="danger"
                              disabled={busy || !!s.locked}
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
        <Button
          variant="primary"
          icon="debug-restart"
          disabled={busy || restorable.length === 0}
          title={restorable.length ? tr("arch.restore_all_title", { count: restorable.length }) : tr("arch.no_restorable")}
          onClick={restoreAll}
        >
          {tr("arch.restore_all")}{restorable.length ? tr("common.paren", { v: restorable.length }) : ""}
        </Button>
        <Button variant="ghost" onClick={onClose}>
          {tr("common.close")}
        </Button>
      </footer>
    </Modal>
  );
}
