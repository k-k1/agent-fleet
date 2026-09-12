// DeleteCopyModal — "delete this working copy", and everything the rail shows below it.
//
// It replaced a plain confirm + a second "force?" confirm, for a reason the Cleanup modal
// does not cover: the thing people actually leave behind is a FINISHED SPAWN — a parent
// worktree with three or four children under it, each with a stopped session, all merged.
// Clearing that was archive + delete per copy, by hand, in the right order. A general
// survey of the whole workspace is the wrong shape for it (and goes unused); this is the
// same list the user is already looking at, on the row they already right-clicked.
//
// Two rules the layout exists to serve:
//   - Ticking a row IS the confirmation. Only rows that lose nothing are ticked for you;
//     a row with uncommitted or unmerged work starts empty, and ticking it is the same
//     deliberate act the old second dialog asked for.
//   - A blocked row is not a scarier review row. A lock or a live session is refused by
//     the Agent whatever flags we send, so those rows cannot be ticked at all and say why.
import { useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { raw, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";
import {
  planTree,
  defaultSelection,
  deleteOrder,
  baseBlockedByWorktrees,
  summarize,
  type CopyPlan,
} from "./deleteTree.ts";

const enc = encodeURIComponent;

/** Grade → badge label. A lookup rather than a composed key, so tsc still checks all three. */
const GRADE_LABEL = {
  safe: "rp.del.grade_safe",
  review: "rp.del.grade_review",
  blocked: "rp.del.grade_blocked",
} as const;

/** Per-row outcome once the run has touched it. */
interface RowResult {
  ok: boolean;
  /** Already-localized line to show under the row. */
  note: string;
}

interface DeleteCopyModalProps {
  /** The right-clicked copy and the subtree the rail nests under it. */
  node: RepoTreeNode;
  onClose: () => void;
  /** Fired only when the run cleared EVERY selected row and the dialog closes itself —
   *  that is the one moment the outcome is not readable on screen, so it is the one that
   *  needs a toast. A partial run keeps the dialog open with a note per row instead. */
  onDeleted?: (count: number) => void;
}

export function DeleteCopyModal({ node, onClose, onDeleted }: DeleteCopyModalProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  // Frozen at open: the sessions list is polled every 4s, and rows appearing or being
  // re-graded under the pointer is how a tick lands on a row the user never read.
  const plans = useMemo(() => planTree(node, sessions), [node]); // eslint-disable-line react-hooks/exhaustive-deps
  const [selected, setSelected] = useState<Set<string>>(() => defaultSelection(plans));
  const [withBranches, setWithBranches] = useState(false);
  const [withRemote, setWithRemote] = useState(false);
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<Map<string, RowResult>>(new Map());

  // The base clone's block depends on what is ticked right now, so it is recomputed here
  // rather than being part of the grade.
  const rootHeld = baseBlockedByWorktrees(plans, selected);
  const effective = useMemo(() => {
    if (!rootHeld) return selected;
    const next = new Set(selected);
    next.delete(plans[0].repo.name);
    return next;
  }, [rootHeld, selected, plans]);
  const sum = summarize(plans, effective, withBranches);
  // Offered independently of the tick: the checkbox has to be visible to be ticked, and its
  // count is "how many of the selected copies have a branch that could go", not "how many
  // will go".
  const branchable = useMemo(
    () => deleteOrder(plans, effective).filter((p) => p.branch).length,
    [plans, effective],
  );
  const done = results.size > 0;

  const toggle = (name: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  /** Archives (or forgets) a row's sessions. Returns the first failure's text, "" when all
   *  landed — a row whose sessions could not be cleared is NOT deleted: removing the folder
   *  under a session that still has a meta leaves a row pointing at nothing. */
  const clearSessions = async (p: CopyPlan): Promise<string> => {
    const call = async (s: Session, ep: "archive" | "stop") => {
      const res = await raw(`api/sessions/${enc(s.name)}/${ep}`, { method: "POST" }).catch(() => null);
      if (!res?.ok) {
        const j = await res?.json().catch(() => null);
        return j?.error ? errText(j.error) : tr("rp.del.session_failed_generic", { name: s.name });
      }
      closeSessionPanes(s.name);
      return "";
    };
    for (const s of p.archive) {
      const err = await call(s, "archive");
      if (err) return err;
    }
    for (const s of p.forget) {
      const err = await call(s, "stop");
      if (err) return err;
    }
    return "";
  };

  /** Deletes the copy's branch from the parent clone — after the copy is gone, because the
   *  branch is checked out in it until then. Returns a note to append, "" when silent. */
  const clearBranch = async (p: CopyPlan): Promise<string> => {
    if (!withBranches || !p.branch || !p.branchIn) return "";
    const q = `branch=${enc(p.branch)}` + (withRemote ? "&remote=1" : "");
    const res = await raw(`api/repos/${enc(p.branchIn)}/branch?${q}`, { method: "DELETE" }).catch(() => null);
    const j = await res?.json().catch(() => null);
    if (!res?.ok) {
      return " " + tr("rp.del.branch_failed", { branch: p.branch, err: j?.error ? errText(j.error) : "" });
    }
    // The push is reported in the body, not as a status: the local delete stood, and the
    // user has to know the remote ref is still there.
    if (j?.remote === "failed") {
      return " " + tr("rp.del.remote_failed", { branch: p.branch, err: String(j.remote_error || "") });
    }
    return "";
  };

  const run = async () => {
    const order = deleteOrder(plans, effective);
    if (order.length === 0) return;
    setBusy(true);
    const out = new Map<string, RowResult>();
    let deleted = 0;
    try {
      // Sequential: every call mutates the same working copies through one Agent, and the
      // order is the safety (deepest first, branch after its copy).
      for (const p of order) {
        const sessErr = await clearSessions(p);
        if (sessErr) {
          out.set(p.repo.name, { ok: false, note: tr("rp.del.kept_sessions_failed", { err: sessErr }) });
          continue;
        }
        const res = await raw(`api/repos/${enc(p.repo.name)}${p.force ? "?force=true" : ""}`, {
          method: "DELETE",
        }).catch(() => null);
        if (!res?.ok) {
          const j = await res?.json().catch(() => null);
          out.set(p.repo.name, {
            ok: false,
            note: tr("rp.del.row_failed", { err: j?.error ? errText(j.error) : "" }),
          });
          continue;
        }
        deleted += 1;
        out.set(p.repo.name, { ok: true, note: tr("rp.del.row_done") + (await clearBranch(p)) });
      }
    } finally {
      setBusy(false);
      setResults(out);
    }
    void useReposStore.getState().refresh();
    void useSessionsStore.getState().refresh();
    useFilesStore.getState().bump();
    // Nothing left to read means nothing left to fix — close instead of making the user
    // dismiss a list of ticks.
    if (deleted === order.length) {
      onDeleted?.(deleted);
      onClose();
    }
  };

  const gradeOf = (p: CopyPlan) => (p.repo.name === plans[0].repo.name && rootHeld ? "blocked" : p.grade);
  const whyOf = (p: CopyPlan): { key: MsgKey | ""; count: number } =>
    p.repo.name === plans[0].repo.name && rootHeld
      ? { key: "rp.del.why_base_has_wt", count: 0 }
      : { key: p.whyKey, count: p.whyCount };

  return (
    <Modal title={tr("rp.delete_workcopy_title")} onClose={onClose} lockClose={busy} className="wcdel-modal">
      <div className="ui-modal-body">
        <p className="wcdel-intro">
          {plans.length > 1
            ? tr("rp.del.intro_tree", { name: plans[0].repo.name, count: plans.length - 1 })
            : tr("rp.delete_workcopy_body", { name: plans[0].repo.name })}
        </p>

        <ul className="wcdel-list">
          {plans.map((p) => {
            const grade = gradeOf(p);
            const why = whyOf(p);
            const res = results.get(p.repo.name);
            const alive = p.sessions.filter((s) => s.alive).length;
            return (
              <li
                key={p.repo.name}
                className={"wcdel-row wcdel-" + grade}
                style={{ "--wcdel-depth": Math.min(p.depth, 3) } as CSSProperties}
              >
                <label className="wcdel-head">
                  <input
                    type="checkbox"
                    checked={grade !== "blocked" && selected.has(p.repo.name)}
                    disabled={busy || done || grade === "blocked"}
                    onChange={() => toggle(p.repo.name)}
                  />
                  <span className="wcdel-name">{p.repo.name}</span>
                  {p.repo.branch && <span className="wcdel-branch">{p.repo.branch}</span>}
                  <span className="wcdel-sess">
                    {p.sessions.length === 0
                      ? tr("rp.del.no_sessions")
                      : alive > 0
                        ? tr("rp.del.sessions_n_alive", { count: p.sessions.length, alive })
                        : tr("rp.del.sessions_n", { count: p.sessions.length })}
                  </span>
                  <span className={"wcdel-grade wcdel-grade-" + grade}>{tr(GRADE_LABEL[grade])}</span>
                </label>
                {why.key && !res && <p className="wcdel-why">{tr(why.key, { count: why.count })}</p>}
                {res && (
                  <p className={"wcdel-result" + (res.ok ? "" : " is-failed")}>
                    <Icon name={res.ok ? "check" : "error"} />
                    {res.note}
                  </p>
                )}
              </li>
            );
          })}
        </ul>

        {branchable > 0 && (
          <label className="wcdel-opt">
            <input
              type="checkbox"
              checked={withBranches}
              disabled={busy || done}
              onChange={(e) => {
                setWithBranches(e.target.checked);
                // Turning the branches off has to take the remote with it — otherwise the
                // ticked-but-inert remote box reads as a promise nothing keeps.
                if (!e.target.checked) setWithRemote(false);
              }}
            />
            <span>{tr("rp.del.branches", { count: branchable })}</span>
            <span className="wcdel-opt-hint">{tr("rp.del.branches_hint")}</span>
          </label>
        )}
        {withBranches && (
          <label className="wcdel-opt wcdel-opt-sub">
            <input
              type="checkbox"
              checked={withRemote}
              disabled={busy || done}
              onChange={(e) => setWithRemote(e.target.checked)}
            />
            <span>{tr("rp.del.remote")}</span>
            <span className="wcdel-opt-hint is-warn">{tr("rp.del.remote_hint")}</span>
          </label>
        )}
      </div>

      <footer className="ui-modal-foot wcdel-foot">
        <span className="wcdel-summary">
          {tr("rp.del.summary", { copies: sum.copies, archive: sum.archive })}
          {sum.forget > 0 && tr("rp.del.summary_forget", { count: sum.forget })}
          {sum.branches > 0 && tr("rp.del.summary_branches", { count: sum.branches })}
          {sum.force > 0 && <span className="wcdel-warn">{tr("rp.del.force_warn", { count: sum.force })}</span>}
        </span>
        <span className="wcdel-foot-actions">
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {done ? tr("rp.del.close") : tr("common.cancel")}
          </Button>
          {!done && (
            <Button variant="danger" onClick={() => void run()} disabled={busy || sum.copies === 0}>
              {tr("rp.del.run", { count: sum.copies })}
            </Button>
          )}
        </span>
      </footer>
    </Modal>
  );
}
