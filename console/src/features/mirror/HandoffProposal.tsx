// A session can propose one or more successors' first prompts through the deliberately
// narrow session-side MCP — a single turn may fan a task out into several parallel
// follow-ups. The proposal is editable, but only the user can open the normal launch
// dialog (and therefore select agent/model and create a session).
//
// Each card renders at the point in the conversation where it was proposed (see
// handoffPlacement), NOT as the scroller's last child: a durable card pinned to the
// bottom owns the mirror's landing position forever, and every message sent afterwards
// looks lost behind it (2026-08-04 実障害). That is why the fetch lives in a hook the
// mirror owns — it needs created_at to place each card.
import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useLaunchSeed, useLaunchTarget, type Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

export interface Proposal {
  id: string;
  prompt: string;
  title?: string;
  created_at: number;
  /** Set once a session was really created from this proposal. The proposal is kept
   *  (re-reading a handoff is useful, and discarding it is the user's call), so this
   *  only drives the 起動済み badge. */
  launched_at?: number;
}

/** Poll this session's outstanding proposals (oldest first). Separate from the cards so
 *  the mirror can place each one chronologically. Clears on session change — a pane
 *  swaps sessions without remounting the mirror, so stale proposals would otherwise show
 *  for a tick. */
export function useHandoffProposals(session: string): [Proposal[], (ps: Proposal[]) => void] {
  const [proposals, setProposals] = useState<Proposal[]>([]);
  useEffect(() => {
    let alive = true;
    setProposals([]);
    const load = async () => {
      try {
        const d = await api(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`);
        if (d?.error || !alive) return;
        setProposals(Array.isArray(d?.proposals) ? (d.proposals as Proposal[]) : []);
      } catch {
        /* transient polling failure: keep the last visible proposals */
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [session]);
  return [proposals, setProposals];
}

/** markHandoffLaunched badges the proposal identified by id once a session really
 *  started from it. Called from the launch dialog's success path — a cancelled dialog
 *  must not badge. */
export async function markHandoffLaunched(session: string, id: string): Promise<void> {
  await apiJSON(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`, "POST", { id, launched: true }).catch(
    () => undefined, // best-effort badge: never let it break the launch it follows
  );
}

export function HandoffProposal({
  session,
  sessionMeta,
  proposal,
  onChange,
}: {
  session: string;
  sessionMeta?: Session | null;
  proposal: Proposal;
  onChange: (p: Proposal | null) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [draft, setDraft] = useState(proposal.prompt);
  const [title, setTitle] = useState(proposal.title || "");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);

  // Follow the server copy while not editing (the poll above refreshes it), so an edit
  // made in another tab shows up — but never clobber what the user is typing here.
  useEffect(() => {
    if (editing) return;
    setDraft(proposal.prompt);
    setTitle(proposal.title || "");
  }, [proposal, editing]);

  const save = async () => {
    if (!draft.trim() || !title.trim() || busy) return;
    setBusy(true);
    const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/handoff-proposal`, "POST", {
      id: proposal.id,
      prompt: draft,
      title,
    });
    setBusy(false);
    if (d?.error) {
      toast(tr("mirror.handoff_save_failed", { msg: errText(d.error) }));
      return;
    }
    onChange(d.proposal as Proposal);
    setEditing(false);
  };
  const discard = async () => {
    if (busy) return;
    setBusy(true);
    const d = await apiJSON(
      `api/sessions/${encodeURIComponent(session)}/handoff-proposal?id=${encodeURIComponent(proposal.id)}`,
      "DELETE",
    );
    setBusy(false);
    if (d?.error) {
      toast(tr("mirror.handoff_discard_failed", { msg: errText(d.error) }));
      return;
    }
    onChange(null);
  };
  const launch = () => {
    const path = sessionMeta?.dir || sessionMeta?.path || "";
    if (!path) {
      toast(tr("mirror.handoff_no_dir"));
      return;
    }
    const repo: Repo = {
      name: sessionMeta?.repo || path.split("/").filter(Boolean).at(-1) || session,
      path,
      branch: sessionMeta?.currentBranch || sessionMeta?.branch,
      worktree: sessionMeta?.worktree,
    };
    // Carry WHICH session/proposal this is, so the dialog's success path can badge it.
    useLaunchSeed.getState().set(proposal.prompt, proposal.title, session, proposal.id);
    useLaunchTarget.getState().open(repo);
  };
  return (
    <section className="mirror-handoff" aria-label={tr("mirror.handoff_title")}>
      <header className="mirror-handoff-head">
        <span>
          <Icon name="git-branch" /> {tr("mirror.handoff_title")}
        </span>
        {proposal.launched_at ? <span className="mirror-handoff-done">{tr("mirror.handoff_launched")}</span> : null}
      </header>
      <p className="muted">{proposal.launched_at ? tr("mirror.handoff_intro_launched") : tr("mirror.handoff_intro")}</p>
      {editing ? (
        <>
          <input
            className="mirror-handoff-title"
            value={title}
            maxLength={512}
            placeholder={tr("mirror.handoff_title_ph")}
            onChange={(e) => setTitle(e.target.value)}
          />
          <textarea className="mirror-handoff-edit" value={draft} onChange={(e) => setDraft(e.target.value)} spellCheck={false} />
        </>
      ) : (
        <>
          <strong className="mirror-handoff-session-title">{proposal.title || tr("mirror.handoff_title_auto")}</strong>
          <pre className="mirror-handoff-prompt">{proposal.prompt}</pre>
        </>
      )}
      <div className="mirror-handoff-actions">
        {editing ? (
          <>
            <button type="button" className="ghost xs" disabled={busy || !draft.trim() || !title.trim()} onClick={() => void save()}>
              {tr("common.save")}
            </button>
            <button
              type="button"
              className="ghost xs"
              disabled={busy}
              onClick={() => {
                setDraft(proposal.prompt);
                setTitle(proposal.title || "");
                setEditing(false);
              }}
            >
              {tr("common.cancel")}
            </button>
          </>
        ) : (
          <button type="button" className="ghost xs" disabled={busy} onClick={() => setEditing(true)}>
            <Icon name="edit" /> {tr("chat.plan.edit")}
          </button>
        )}
        <button type="button" className="ghost xs" disabled={busy} onClick={() => void discard()}>
          <Icon name="trash" /> {tr("mirror.handoff_discard")}
        </button>
        <button type="button" className={proposal.launched_at ? "ghost xs" : "primary xs"} disabled={busy} onClick={launch}>
          <Icon name="run" /> {proposal.launched_at ? tr("mirror.handoff_launch_again") : tr("mirror.handoff_launch")}
        </button>
      </div>
    </section>
  );
}
