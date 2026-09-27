// RecreateWorktreeModal — put a deleted worktree back at its original path, then bring back
// the archived sessions that ran in it (issue #1040). The path is what matters: a session's
// id and every CLI's own store are keyed on it, so once the folder is back where it was,
// restore + the ordinary resume find the conversation for every kind.
//
// Two steps. "plan" shows where the commits would come from (GET /repos/{name}/recreate) and
// creates the worktree; a branch checked out in another copy asks for a new branch name
// instead, never a detached HEAD. "sessions" lists the shelf's sessions of that folder to
// restore — folders named after a branch are reused across generations, so the dates are
// shown and a start branch that differs from the recreated one is flagged.
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText, raw } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import type { Session } from "../../types/session.ts";

/** One way to put the folder back — gitx.RecreateCandidate on the wire. */
export interface RecreateCandidate {
  source: "deleted" | "local" | "remote" | "trash" | "merged" | "new";
  branch: string;
  sha?: string;
  ref?: string;
  pr?: number;
  in_use?: string;
  /** "deleted" only: the uncommitted work recorded at the delete, laid back over it. */
  snapshot?: string;
  /** "deleted" only: the branch has moved since the delete. */
  moved?: boolean;
}

/** Mirrors gitx.RecreateCandidate.NeedsNewBranch: the branch is held elsewhere, has moved since
 *  the delete, or there was none (a detached HEAD). */
const needsNewBranchFor = (c?: RecreateCandidate) => !!c && (!!c.in_use || !!c.moved || !c.branch);

interface RecreatePlan {
  name: string;
  path: string;
  parent: string;
  candidates: RecreateCandidate[];
}

type ArchivedSession = Session & { started?: string };

interface RecreateWorktreeModalProps {
  /** The folder that is gone, as the sessions recorded it. */
  dir: string;
  /** The shelf's sessions whose dir is exactly `dir`. */
  sessions: ArchivedSession[];
  onClose: () => void;
  /** Called after the worktree exists (and again after sessions are restored). */
  onChanged: () => void;
}

const folderOf = (dir: string) => dir.split("/").filter(Boolean).pop() || "";
const shortSha = (sha?: string) => (sha ? sha.slice(0, 8) : "");

export function RecreateWorktreeModal({ dir, sessions, onClose, onChanged }: RecreateWorktreeModalProps) {
  const tr = useT();
  const toast = useToast();
  const name = folderOf(dir);
  const [plan, setPlan] = useState<RecreatePlan | null>(null);
  const [planErr, setPlanErr] = useState("");
  const [pick, setPick] = useState(0);
  const [newBranch, setNewBranch] = useState("");
  const [busy, setBusy] = useState(false);
  // Set once the worktree exists: the branch it is on.
  const [created, setCreated] = useState<string | null>(null);
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(sessions.map((s) => s.name)));
  // Bumped to fetch the plan again (after recreate_stale: what was shown no longer resolves).
  const [planRev, setPlanRev] = useState(0);

  useEffect(() => {
    let live = true;
    api(`api/repos/${encodeURIComponent(name)}/recreate`)
      .then((d) => {
        if (!live) return;
        if (d?.error) setPlanErr(errText(d.error));
        else setPlan(d as RecreatePlan);
      })
      .catch(() => live && setPlanErr(tr("rwt.plan_failed")));
    return () => {
      live = false;
    };
  }, [name, tr, planRev]);

  const cand = plan?.candidates[pick];
  // The Agent resolves {name} under its own repos root; a session that recorded a folder
  // somewhere else cannot come back through this path, and recreating elsewhere would not
  // bring its conversation back.
  const elsewhere = !!plan && plan.path !== dir;
  const needsNewBranch = needsNewBranchFor(cand);

  useEffect(() => {
    if (!needsNewBranchFor(cand)) setNewBranch("");
    else setNewBranch(cand?.branch ? cand.branch + "-2" : name.slice(name.lastIndexOf("@") + 1));
  }, [cand, name]);

  const newBranchLabel = (c?: RecreateCandidate) =>
    !c
      ? ""
      : c.in_use
        ? tr("rwt.in_use", { branch: c.branch, folder: c.in_use })
        : c.moved
          ? tr("rwt.moved", { branch: c.branch })
          : tr("rwt.detached");

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!cand || busy || elsewhere) return;
    const nb = newBranch.trim();
    if (needsNewBranch && !nb) return;
    setBusy(true);
    try {
      const body: Record<string, string> = { source: cand.source, branch: cand.branch };
      if (needsNewBranch) body.new_branch = nb;
      const res = await apiJSON(`api/repos/${encodeURIComponent(name)}/recreate`, "POST", body);
      if (res?.error) {
        toast(errText(res.error));
        if (res.error.code === "recreate_stale") {
          setPlan(null);
          setPick(0);
          setPlanRev((n) => n + 1);
        }
        return;
      }
      setCreated(typeof res?.branch === "string" ? res.branch : cand.branch);
      onChanged();
      toast(tr("rwt.created", { name }), { kind: "success" });
      if (sessions.length === 0) onClose();
    } catch {
      toast(tr("rwt.create_failed"));
    } finally {
      setBusy(false);
    }
  };

  const ordered = useMemo(
    () => [...sessions].sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || "")),
    [sessions],
  );

  const toggle = (n: string) =>
    setChosen((prev) => {
      const next = new Set(prev);
      if (next.has(n)) next.delete(n);
      else next.add(n);
      return next;
    });

  const restoreChosen = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    let restored = 0;
    let failed = 0;
    try {
      // One at a time, like the shelf's own restore-all.
      for (const s of ordered) {
        if (!chosen.has(s.name)) continue;
        try {
          const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/restore`, { method: "POST" });
          if (res.ok) restored += 1;
          else failed += 1;
        } catch {
          failed += 1;
        }
      }
      if (restored > 0) onChanged();
      if (failed > 0) toast(tr("arch.restored_some", { restored, failed }));
      else if (restored > 0) toast(tr("arch.restored_n", { restored }), { kind: "success" });
      onClose();
    } finally {
      setBusy(false);
    }
  };

  const sourceText = (c: RecreateCandidate) => {
    switch (c.source) {
      case "deleted":
        return c.snapshot ? tr("rwt.src_deleted_snapshot") : tr("rwt.src_deleted");
      case "local":
        return tr("rwt.src_local");
      case "remote":
        return tr("rwt.src_remote", { ref: c.ref || "" });
      case "trash":
        return tr("rwt.src_trash");
      case "merged":
        return c.pr ? tr("rwt.src_merged_pr", { pr: c.pr }) : tr("rwt.src_merged");
      case "new":
        return c.ref ? tr("rwt.src_new", { base: c.ref }) : tr("rwt.src_new_head");
    }
  };

  if (created !== null) {
    return (
      <Modal title={tr("rwt.sessions_title")} onClose={onClose} as="form" onSubmit={restoreChosen} lockClose={busy}>
        <div className="ui-modal-body">
          <p>{tr("rwt.sessions_lead", { name, branch: created })}</p>
          <ul className="rwt-sessions">
            {ordered.map((s) => {
              const differs = !!s.branch && s.branch !== created;
              return (
                <li key={s.name}>
                  <label className="rwt-session">
                    <input type="checkbox" checked={chosen.has(s.name)} onChange={() => toggle(s.name)} disabled={busy} />
                    <span className="rwt-session-name">{displayName(s)}</span>
                    <span className="arch-sub">
                      <span className={"kind-tag kind-" + kindClass(s.kind)}>
                        <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                      </span>
                      {s.started ? " · " + s.started : ""}
                      {s.branch ? " · " + s.branch : ""}
                    </span>
                    {differs && (
                      <span className="rwt-warn" title={tr("rwt.branch_differs_hint")}>
                        <Icon name="warning" /> {tr("rwt.branch_differs", { branch: s.branch || "" })}
                      </span>
                    )}
                  </label>
                </li>
              );
            })}
          </ul>
        </div>
        <footer className="ui-modal-foot">
          <Button type="submit" variant="primary" icon="debug-restart" disabled={busy || chosen.size === 0}>
            {tr("rwt.restore_chosen")}
            {chosen.size ? tr("common.paren", { v: chosen.size }) : ""}
          </Button>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {tr("rwt.skip")}
          </Button>
        </footer>
      </Modal>
    );
  }

  return (
    <Modal title={tr("rwt.title")} onClose={onClose} as="form" onSubmit={create} lockClose={busy}>
      <div className="ui-modal-body">
        <p className="sm-muted">
          <code>{dir}</code>
        </p>
        {!plan && !planErr && <p className="sm-muted">{tr("chat.ph_loading")}</p>}
        {planErr && <p className="rwt-error">{planErr}</p>}
        {elsewhere && <p className="rwt-error">{tr("rwt.elsewhere", { path: plan?.path || "" })}</p>}
        {plan && !elsewhere && plan.candidates.length === 0 && <p className="rwt-error">{tr("rwt.no_candidates")}</p>}
        {plan && !elsewhere && plan.candidates.length > 0 && (
          <>
            <p>{tr("rwt.lead", { parent: plan.parent })}</p>
            <div className="rwt-cands" role="radiogroup">
              {plan.candidates.map((c, i) => (
                <label key={c.source + ":" + c.branch} className="rwt-cand">
                  <input type="radio" name="rwt-cand" checked={pick === i} onChange={() => setPick(i)} disabled={busy} />
                  <span className="rwt-cand-branch">{c.branch || tr("rwt.detached_head")}</span>
                  <span className="sm-muted">
                    {sourceText(c)}
                    {c.sha ? " · " + shortSha(c.sha) : ""}
                  </span>
                </label>
              ))}
            </div>
            {cand?.source === "new" && <p className="sm-muted">{tr("rwt.new_hint")}</p>}
            {needsNewBranch && (
              <label className="ui-field">
                <span className="ui-field-label">{newBranchLabel(cand)}</span>
                <input type="text" value={newBranch} onChange={(e) => setNewBranch(e.target.value)} disabled={busy} />
                <span className="ui-field-hint">{tr("rwt.in_use_hint")}</span>
              </label>
            )}
          </>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button
          type="submit"
          variant="primary"
          icon="repo"
          disabled={busy || !cand || elsewhere || (needsNewBranch && !newBranch.trim())}
        >
          {tr("rwt.create")}
        </Button>
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </Button>
      </footer>
    </Modal>
  );
}
