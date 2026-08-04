// A session can propose its successor's first prompt through the deliberately
// narrow session-side MCP.  The proposal is editable, but only the user can open
// the normal launch dialog (and therefore select agent/model and create a session).
import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useLaunchSeed, useLaunchTarget, type Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

interface Proposal { prompt: string; title?: string; created_at: number }

export function HandoffProposal({ session, sessionMeta }: { session: string; sessionMeta?: Session | null }) {
  const tr = useT();
  const toast = useToast();
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [draft, setDraft] = useState("");
  const [title, setTitle] = useState("");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const d = await api(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`);
        if (d?.error) return;
        const p = d?.proposal && typeof d.proposal.prompt === "string" ? d.proposal as Proposal : null;
        if (!alive) return;
        setProposal(p);
        if (!editing) { setDraft(p?.prompt || ""); setTitle(p?.title || ""); }
      } catch { /* transient polling failure: keep the last visible proposal */ }
    };
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => { alive = false; window.clearInterval(timer); };
  }, [session, editing]);

  if (!proposal) return null;
  const save = async () => {
    if (!draft.trim() || !title.trim() || busy) return;
    setBusy(true);
    const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`, "POST", { prompt: draft, title });
    setBusy(false);
    if (d?.error) { toast(tr("mirror.handoff_save_failed", { msg: errText(d.error) })); return; }
    setProposal(d.proposal as Proposal);
    setEditing(false);
  };
  const discard = async () => {
    if (busy) return;
    setBusy(true);
    const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`, "DELETE");
    setBusy(false);
    if (d?.error) { toast(tr("mirror.handoff_discard_failed", { msg: errText(d.error) })); return; }
    setProposal(null);
  };
  const launch = () => {
    const path = sessionMeta?.dir || sessionMeta?.path || "";
    if (!path) { toast(tr("mirror.handoff_no_dir")); return; }
    const repo: Repo = {
      name: sessionMeta?.repo || path.split("/").filter(Boolean).at(-1) || session,
      path,
      branch: sessionMeta?.currentBranch || sessionMeta?.branch,
      worktree: sessionMeta?.worktree,
    };
    useLaunchSeed.getState().set(proposal.prompt, proposal.title);
    useLaunchTarget.getState().open(repo);
  };
  return (
    <section className="mirror-handoff" aria-label={tr("mirror.handoff_title")}>
      <header className="mirror-handoff-head"><span><Icon name="git-branch" /> {tr("mirror.handoff_title")}</span></header>
      <p className="muted">{tr("mirror.handoff_intro")}</p>
      {editing ? <>
        <input className="mirror-handoff-title" value={title} maxLength={512} placeholder={tr("mirror.handoff_title_ph")} onChange={(e) => setTitle(e.target.value)} />
        <textarea className="mirror-handoff-edit" value={draft} onChange={(e) => setDraft(e.target.value)} spellCheck={false} />
      </> : <><strong className="mirror-handoff-session-title">{proposal.title || tr("mirror.handoff_title_auto")}</strong><pre className="mirror-handoff-prompt">{proposal.prompt}</pre></>}
      <div className="mirror-handoff-actions">
        {editing ? <>
          <button type="button" className="ghost xs" disabled={busy || !draft.trim() || !title.trim()} onClick={() => void save()}>{tr("common.save")}</button>
          <button type="button" className="ghost xs" disabled={busy} onClick={() => { setDraft(proposal.prompt); setTitle(proposal.title || ""); setEditing(false); }}>{tr("common.cancel")}</button>
        </> : <button type="button" className="ghost xs" disabled={busy} onClick={() => setEditing(true)}><Icon name="edit" /> {tr("chat.plan.edit")}</button>}
        <button type="button" className="ghost xs" disabled={busy} onClick={() => void discard()}><Icon name="trash" /> {tr("mirror.handoff_discard")}</button>
        <button type="button" className="primary xs" disabled={busy} onClick={launch}><Icon name="run" /> {tr("mirror.handoff_launch")}</button>
      </div>
    </section>
  );
}
