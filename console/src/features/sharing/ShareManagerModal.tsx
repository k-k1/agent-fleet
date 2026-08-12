import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { agentOf } from "../../agents/registry.ts";
import type { Session } from "../../types/session.ts";

interface ShareRow {
  id: string;
  recipientUserKey: string;
  scope: { type: string; key: string };
  permission: "ro" | "rw";
}

export function ShareManagerModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const [shares, setShares] = useState<ShareRow[]>([]);
  const [archived, setArchived] = useState<Session[]>([]);
  const [target, setTarget] = useState("");
  const [recipient, setRecipient] = useState("");
  const [permission, setPermission] = useState<"ro" | "rw">("ro");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const candidates = useMemo(() => [
    ...[...sessions, ...archived].filter((s) => agentOf(s.kind).caps.transcript).map((s) => ({
      value: `session:${s.name}`, label: `${tr("share.session_scope")}: ${s.title || s.label || s.name}`,
    })),
    ...repos.filter((r) => r.workingCopyId).map((r) => ({
      value: `${r.worktree ? "worktree" : "repo"}:${r.workingCopyId}`,
      label: `${r.worktree ? tr("share.worktree_scope") : tr("share.repo_scope")}: ${r.name}`,
    })),
  ], [repos, sessions, archived, tr]);
  const load = () => api("api/session-shares").then((d) => setShares(d.shares || [])).catch(() => setShares([]));
  useEffect(() => {
    void load();
    void api("api/sessions/archived").then((d) => setArchived(d.sessions || [])).catch(() => setArchived([]));
  }, []);
  useEffect(() => { if (!target && candidates[0]) setTarget(candidates[0].value); }, [candidates, target]);

  const submit = async () => {
    const [type, key] = target.split(":", 2);
    if (!recipient.trim() || !type || !key) return;
    setSaving(true); setError("");
    const d = await apiJSON("api/session-shares", "POST", { recipientUserKey: recipient.trim(), scope: { type, key }, permission })
      .catch(() => ({ error: { message: tr("share.save_failed") } }));
    setSaving(false);
    if (d?.error) setError(errText(d.error)); else { setRecipient(""); await load(); }
  };
  const remove = async (id: string) => { await api(`api/session-shares/${encodeURIComponent(id)}`, { method: "DELETE" }); await load(); };
  const change = async (id: string, p: "ro" | "rw") => { await apiJSON(`api/session-shares/${encodeURIComponent(id)}`, "PATCH", { permission: p }); await load(); };

  return (
    <Modal title={tr("share.manage_title")} onClose={onClose} lockClose={saving} className="share-manage-modal">
      <div className="ui-modal-body">
        <p className="muted">{tr("share.exposure_warning")}</p>
        <label>{tr("share.target")}<select value={target} onChange={(e) => setTarget(e.target.value)}>{candidates.map((c) => <option value={c.value} key={c.value}>{c.label}</option>)}</select></label>
        <label>{tr("share.recipient")}<input value={recipient} onChange={(e) => setRecipient(e.target.value)} placeholder="user-login-id" /></label>
        <label>{tr("share.permission")}<select value={permission} onChange={(e) => setPermission(e.target.value as "ro" | "rw")}><option value="ro">RO</option><option value="rw">RW ({tr("share.approval_required")})</option></select></label>
        {error && <p className="error">{error}</p>}
        <div className="ui-modal-actions"><Button variant="primary" icon="broadcast" disabled={!target || !recipient.trim()} onClick={() => void submit()}>{tr("share.create")}</Button></div>
        <h4>{tr("share.existing")}</h4>
        {shares.map((s) => <div className="share-existing" key={s.id}><span>{s.recipientUserKey} · {s.scope.type}</span><select value={s.permission} onChange={(e) => void change(s.id, e.target.value as "ro" | "rw")}><option value="ro">RO</option><option value="rw">RW</option></select><Button small onClick={() => void remove(s.id)}>{tr("share.unshare")}</Button></div>)}
      </div>
    </Modal>
  );
}
