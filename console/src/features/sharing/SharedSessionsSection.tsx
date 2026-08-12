import { useEffect, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { openSharedSession } from "./open.ts";
import { startSharedSessionsPolling, useSharedSessionsStore } from "./store.ts";
import "./sharing.css";

interface Proposal {
  id: string;
  sessionId: string;
  proposerUserKey: string;
  action: string;
  payload?: { prompt?: string; feedback?: string };
  status: string;
  expiresAt: string;
}

export function SharedSessionsSection() {
  const tr = useT();
  const sessions = useSharedSessionsStore((s) => s.sessions);
  const [proposals, setProposals] = useState<Proposal[]>([]);
  const [open, setOpen] = useState(false);
  const loadProposals = () => api("api/session-share-proposals").then((d) => {
    if (!d?.error) setProposals((d.proposals || []).filter((p: Proposal) => p.status === "pending" || p.status === "processing"));
  }).catch(() => {});
  useEffect(() => {
    const stop = startSharedSessionsPolling();
    void loadProposals();
    const timer = window.setInterval(() => void loadProposals(), 5000);
    return () => { stop(); window.clearInterval(timer); };
  }, []);

  const decide = async (id: string, decision: "approve" | "reject") => {
    await api(`api/session-share-proposals/${encodeURIComponent(id)}/${decision}`, { method: "POST" });
    await loadProposals();
  };

  if (sessions.length === 0 && proposals.length === 0) return null;
  return (
    <>
      <Section id="shared-sessions" title={tr("share.shared_sessions")} icon="broadcast" count={sessions.length}
        actions={proposals.length > 0 ? <IconButton icon="mail" label={tr("share.pending", { count: proposals.length })} onClick={() => setOpen(true)} /> : undefined}>
        <ul className="sess-list">
          {sessions.map((s) => (
            <li key={s.id}>
              <button className="shared-rail-row" type="button" title={`${s.ownerUserKey} · ${s.repo || s.name}`}
                onClick={(e) => openSharedSession(s.id, e.ctrlKey || e.metaKey)}>
                <Icon name="comment-discussion" />
                <span className="name">{s.title || s.label || s.name}</span>
                <small>{s.ownerUserKey} · {s.permission.toUpperCase()}</small>
                {s.workspaceState !== "running" && <Icon name="debug-pause" title={tr("share.owner_stopped")} />}
              </button>
            </li>
          ))}
        </ul>
      </Section>
      {open && (
        <Modal title={tr("share.pending_title")} onClose={() => setOpen(false)} className="share-proposals-modal">
          <div className="ui-modal-body">
            {proposals.map((p) => (
              <article className="share-proposal-card" key={p.id}>
                <strong>{p.proposerUserKey}</strong>
                <p>{p.payload?.prompt || p.payload?.feedback || p.action}</p>
                {p.status === "processing" && <p className="muted">{tr("share.outcome_unknown")}</p>}
                <div className="ui-modal-actions">
                  {p.status === "pending" && <Button onClick={() => void decide(p.id, "reject")}>{tr("share.reject")}</Button>}
                  <Button variant="primary" onClick={() => void decide(p.id, "approve")}>{p.status === "processing" ? tr("share.reconcile") : tr("share.approve")}</Button>
                </div>
              </article>
            ))}
          </div>
        </Modal>
      )}
    </>
  );
}
