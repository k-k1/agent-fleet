// ShareCreateModal — 共有作成フォーム。右クリック(セッション/リポジトリ行)から開くと
// initialTarget で対象が固定され picker は隠れる。左ペインの共有一覧(ShareListModal)の
// 「+ 新規共有」からは initialTarget なしで開き、対象を自分で選ぶ。他の作成フォーム
// (NewRepoModal 等)と同じ as="form" + ui-field/ui-seg + ui-modal-foot の型に合わせる。
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { agentOf } from "../../agents/registry.ts";
import { useMySharesStore } from "./store.ts";
import type { Session } from "../../types/session.ts";
import "./sharing.css";

interface ShareCreateModalProps {
  /** "session:<name>" | "repo:<workingCopyId>" | "worktree:<workingCopyId>" — when set
   * (右クリック起点), the target is fixed and the picker is hidden. */
  initialTarget?: string;
  onClose: () => void;
  onCreated?: () => void;
}

export function ShareCreateModal({ initialTarget, onClose, onCreated }: ShareCreateModalProps) {
  const tr = useT();
  const toast = useToast();
  const repos = useReposStore((s) => s.repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const [archived, setArchived] = useState<Session[]>([]);
  const [target, setTarget] = useState(initialTarget ?? "");
  const [recipient, setRecipient] = useState("");
  const [permission, setPermission] = useState<"ro" | "rw">("ro");
  const [saving, setSaving] = useState(false);
  const locked = !!initialTarget;
  const candidates = useMemo(() => [
    ...[...sessions, ...archived].filter((s) => agentOf(s.kind).caps.transcript).map((s) => ({
      value: `session:${s.name}`, label: `${tr("share.session_scope")}: ${s.title || s.label || s.name}`,
    })),
    ...repos.filter((r) => r.workingCopyId).map((r) => ({
      value: `${r.worktree ? "worktree" : "repo"}:${r.workingCopyId}`,
      label: `${r.worktree ? tr("share.worktree_scope") : tr("share.repo_scope")}: ${r.name}`,
    })),
  ], [repos, sessions, archived, tr]);
  useEffect(() => {
    // 右クリック起点(locked)は対象が既に確定しているので、候補集めのためだけの
    // アーカイブ一覧取得は不要。
    if (locked) return;
    void api("api/sessions/archived").then((d) => setArchived(d.sessions || [])).catch(() => setArchived([]));
  }, [locked]);
  useEffect(() => { if (!locked && !target && candidates[0]) setTarget(candidates[0].value); }, [locked, candidates, target]);
  const targetLabel = candidates.find((c) => c.value === target)?.label ?? target;
  const canSubmit = !!target && !!recipient.trim();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || saving) return;
    const [type, key] = target.split(":", 2);
    if (!type || !key) return;
    setSaving(true);
    const d = await apiJSON("api/session-shares", "POST", { recipientUserKey: recipient.trim(), scope: { type, key }, permission })
      .catch(() => ({ error: { message: tr("share.save_failed") } }));
    setSaving(false);
    if (d?.error) { toast(errText(d.error)); return; }
    void useMySharesStore.getState().refresh();
    onCreated?.();
    onClose();
  };

  return (
    <Modal title={tr("share.create_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving} className="share-create-modal">
      <div className="ui-modal-body">
        <p className="ui-field-hint">{tr("share.exposure_warning")}</p>
        {locked ? (
          <div className="ui-field">
            <span className="ui-field-label">{tr("share.target")}</span>
            <div className="share-target-fixed"><Icon name="broadcast" /> {targetLabel}</div>
          </div>
        ) : (
          <label className="ui-field">
            <span className="ui-field-label">{tr("share.target")}</span>
            <select value={target} onChange={(e) => setTarget(e.target.value)}>
              {candidates.map((c) => <option value={c.value} key={c.value}>{c.label}</option>)}
            </select>
          </label>
        )}
        <label className="ui-field">
          <span className="ui-field-label">{tr("share.recipient")}</span>
          <input value={recipient} onChange={(e) => setRecipient(e.target.value)} placeholder="user-login-id" autoFocus={locked} />
        </label>
        <div className="ui-field">
          <span className="ui-field-label">{tr("share.permission")}</span>
          <div className="ui-seg">
            <button type="button" className={"seg-btn" + (permission === "ro" ? " active" : "")} onClick={() => setPermission("ro")}>RO</button>
            <button type="button" className={"seg-btn" + (permission === "rw" ? " active" : "")} onClick={() => setPermission("rw")}>RW</button>
          </div>
          {permission === "rw" && <span className="ui-field-hint">{tr("share.approval_required")}</span>}
        </div>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>{tr("ui.cancel")}</Button>
        <Button variant="primary" type="submit" icon="broadcast" disabled={!canSubmit || saving}>{tr("share.create")}</Button>
      </footer>
    </Modal>
  );
}
