import { memo, useEffect, useMemo, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { startSharedSessionsPolling, startMySharesPolling, useMySharesStore, useSharedSessionsStore } from "./store.ts";
import { pendingHandoffCount, startHandoffPolling, useHandoffStore } from "./handoffStore.ts";
import { HandoffInboxModal } from "./HandoffInboxModal.tsx";
import { ShareListModal } from "./ShareListModal.tsx";
import { SharedProjectNode } from "./SharedProjectNode.tsx";
import { groupedSharedSessions } from "./sharedProject.ts";
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

export const SharedSessionsSection = memo(function SharedSessionsSection() {
  const tr = useT();
  const sessions = useSharedSessionsStore((s) => s.sessions);
  const ownedShares = useMySharesStore((s) => s.shares.length);
  const [proposals, setProposals] = useState<Proposal[]>([]);
  const [open, setOpen] = useState(false);
  const [manageOpen, setManageOpen] = useState(false);
  const [inboxOpen, setInboxOpen] = useState(false);
  const handoffs = useHandoffStore((s) => pendingHandoffCount(s.received));
  const [reloading, setReloading] = useState(false);
  const loadProposals = () => api("api/session-share-proposals").then((d) => {
    if (!d?.error) setProposals((d.proposals || []).filter((p: Proposal) => p.status === "pending" || p.status === "processing"));
  }).catch(() => {});
  const reload = async () => {
    setReloading(true);
    await Promise.all([useSharedSessionsStore.getState().refresh(true), loadProposals()]);
    setReloading(false);
  };
  useEffect(() => {
    const stop = startSharedSessionsPolling();
    const stopMine = startMySharesPolling();
    const stopHandoff = startHandoffPolling();
    void loadProposals();
    const timer = window.setInterval(() => void loadProposals(), 5000);
    return () => { stop(); stopMine(); stopHandoff(); window.clearInterval(timer); };
  }, []);

  const decide = async (id: string, decision: "approve" | "reject") => {
    await api(`api/session-share-proposals/${encodeURIComponent(id)}/${decision}`, { method: "POST" });
    await loadProposals();
  };
  const groups = useMemo(() => groupedSharedSessions(sessions), [sessions]);

  if (sessions.length === 0 && proposals.length === 0 && ownedShares === 0 && handoffs === 0) return null;
  return (
    <>
      <Section id="shared-sessions" title={tr("share.shared_sessions")} icon="broadcast" count={sessions.length}
        actions={<>
          {proposals.length > 0 && <IconButton icon="mail" label={tr("share.pending", { count: proposals.length })} onClick={() => setOpen(true)} />}
          {/* The mail badge above points the other way: it counts RW proposals *this* user
              made that are awaiting approval. Folding handovers (docs/log/77) into it would
              destroy that meaning, so they get their own icon. */}
          {handoffs > 0 && (
            <IconButton icon="git-branch" label={tr("handoff.inbox_pending", { count: handoffs })} onClick={() => setInboxOpen(true)} />
          )}
          {/* Explicit reload. The periodic polling only reads the CP snapshot, and the owner
              Workspace's inventory is re-fetched at most once every 60s (docs/log/59 §3), so
              this is the way out when a state change or an added/removed session must show
              up now. */}
          <IconButton icon={reloading ? "loading" : "refresh"} spin={reloading} label={tr("share.reload")}
            disabled={reloading} onClick={() => void reload()} />
          <IconButton icon="settings-gear" label={tr("share.list_title")} onClick={() => setManageOpen(true)} />
        </>}>
        <ul className="proj-tree sess-list">
          {groups.map((g) => <SharedProjectNode key={`${g.ownerUserKey}:${g.copies[0].workingCopyId}`} group={g} />)}
        </ul>
      </Section>
      {inboxOpen && <HandoffInboxModal onClose={() => setInboxOpen(false)} />}
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
      {manageOpen && <ShareListModal onClose={() => setManageOpen(false)} />}
    </>
  );
});
