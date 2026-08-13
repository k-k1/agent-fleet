// ShareListModal — 所有者が作成した共有の一覧(左ペインの broadcast アイコン / 共有
// セクションの管理アイコンから開く)。行の見た目・footer の主操作+閉じるという型は
// ArchivedModal(sessions/ArchivedModal.tsx)に合わせている。作成は ShareCreateModal
// (このモーダルの「+ 新規共有」、または各行の右クリックメニュー)に分離。
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { api, apiJSON } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { ShareCreateModal } from "./ShareCreateModal.tsx";
import { useMySharesStore } from "./store.ts";
import "./sharing.css";

export function ShareListModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const shares = useMySharesStore((s) => s.shares);
  const askConfirm = useConfirm();
  const [createOpen, setCreateOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  useEffect(() => { void useMySharesStore.getState().refresh(); }, []);

  const targetInfo = useMemo(() => (scope: { type: string; key: string }) => {
    if (scope.type === "session") {
      const s = sessions.find((x) => x.name === scope.key);
      return { icon: "comment-discussion", label: s ? s.title || s.label || s.name : scope.key };
    }
    const r = repos.find((x) => x.workingCopyId === scope.key);
    return {
      icon: scope.type === "worktree" ? "git-branch" : "root-folder",
      label: r ? r.name : scope.key,
    };
  }, [sessions, repos]);

  const remove = async (id: string) => {
    if (!(await askConfirm({
      title: tr("share.unshare_confirm_title"),
      body: tr("share.unshare_confirm_body"),
      confirmLabel: tr("share.unshare"),
      danger: true,
    }))) return;
    setBusy(id);
    await api(`api/session-shares/${encodeURIComponent(id)}`, { method: "DELETE" });
    setBusy(null);
    await useMySharesStore.getState().refresh();
  };
  const change = async (id: string, p: "ro" | "rw") => {
    setBusy(id);
    await apiJSON(`api/session-shares/${encodeURIComponent(id)}`, "PATCH", { permission: p });
    setBusy(null);
    await useMySharesStore.getState().refresh();
  };

  return (
    <Modal title={tr("share.list_title")} onClose={onClose} className="share-list-modal">
      <div className="ui-modal-body">
        {shares.length === 0 ? (
          <p className="sm-muted">{tr("share.no_shares")}</p>
        ) : (
          <ul className="share-list">
            {shares.map((s) => {
              const info = targetInfo(s.scope);
              return (
                <li key={s.id} className="share-row">
                  <div className="share-info" title={info.label}>
                    <Icon name={info.icon} />
                    <span className="share-target-name">{info.label}</span>
                    <span className="share-recipient">{s.recipientUserKey}</span>
                  </div>
                  <div className="share-actions">
                    <select value={s.permission} disabled={busy === s.id} onChange={(e) => void change(s.id, e.target.value as "ro" | "rw")}>
                      <option value="ro">{tr("share.permission_ro")}</option>
                      <option value="rw">{tr("share.permission_rw")}</option>
                    </select>
                    <IconButton icon="trash" label={tr("share.unshare")} variant="danger" disabled={busy === s.id} onClick={() => void remove(s.id)} />
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="primary" icon="add" onClick={() => setCreateOpen(true)}>{tr("share.new")}</Button>
        <Button variant="ghost" onClick={onClose}>{tr("common.close")}</Button>
      </footer>
      {createOpen && (
        <ShareCreateModal
          onClose={() => setCreateOpen(false)}
          onCreated={() => void useMySharesStore.getState().refresh()}
        />
      )}
    </Modal>
  );
}
