// ProjectMovePanel — moves or merges the memories of a project whose working copy is gone into
// another project (#1984, ADR 0108 decision 2). The Agent lists the projects and flags the
// orphaned ones; picking one and a target previews what would happen without writing anything.
// A name already taken in the target is the member's call (skip or rename), never decided here.
// The move is sent in batches of 50 memories (two published changes each), with the source
// project id in the query for the audit ledger; each change can be undone in the list above.

import { useCallback, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { WizardFrame, GroupedPreview, type WizardStep } from "./memoryWizard.tsx";
import type { MoveCollision, MoveItem, MovePreview, MoveResultItem, MoveStatus, ProjectRow } from "./memoryTypes.ts";

/** The Agent's per-request cap (agentMemMoveBatch). */
export const MOVE_BATCH = 50;

const POLICIES: ["skip" | "rename", "mem.pm_policy_skip" | "mem.pm_policy_rename"][] = [
  ["skip", "mem.pm_policy_skip"],
  ["rename", "mem.pm_policy_rename"],
];
const STATUSES: MoveStatus[] = ["move", "same", "rename", "collision", "skip"];

export function ProjectMovePanel({ reload, onChanged, onClose }: { reload: number; onChanged: () => void; onClose?: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<ProjectRow[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [policy, setPolicy] = useState<MoveCollision | "">("");
  const [preview, setPreview] = useState<MovePreview | null>(null);
  const [previewErr, setPreviewErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [again, setAgain] = useState(0);
  const [result, setResult] = useState<string | null>(null);
  // The outcome is both toasted and kept in the frame until the choice changes.
  const notify = (msg: string, opts?: { kind?: "success" }) => {
    toast(msg, opts);
    setResult(msg);
  };

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/entries/projects");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    setLoadErr(res?.error ? errText(res.error) : "");
    setRows(res?.error ? [] : (res?.projects ?? []));
    return true;
  }, []);
  useRetryLoad(load, [reload, again]);

  // Only the latest request may answer: a slow preview for an earlier choice must not replace the
  // one shown, or the confirm would pair one project with another's names.
  const seq = useRef(0);
  const loadPreview = useCallback(async (f: string, t: string, p: string) => {
    const n = ++seq.current;
    setPreview(null);
    setPreviewErr("");
    if (!f || !t) return;
    const q = `from=${encodeURIComponent(f)}&to=${encodeURIComponent(t)}` + (p ? `&collision=${p}` : "");
    const res = await api("api/agents/memory/entries/project-move-preview?" + q);
    if (n !== seq.current) return;
    if (res?.error) {
      setPreviewErr(errDetail(res.error));
      return;
    }
    setPreview(res as MovePreview);
  }, []);

  const choose = (f: string, t: string, p: MoveCollision | "") => {
    setFrom(f);
    setResult(null);
    setTo(t);
    setPolicy(p);
    void loadPreview(f, t, p);
  };

  const orphans = (rows ?? []).filter((r) => r.orphaned);
  // Orphans whose repository is also checked out as exactly one live project (#2110). A hint
  // that fills the two pickers and the preview; it moves nothing.
  const suggested = orphans.filter((r) => r.mergeInto);
  const targets = (rows ?? []).filter((r) => !r.orphaned && r.project.id !== from);
  const label = (r: ProjectRow) => `${r.project.display} (${r.count})`;
  const todo: MoveItem[] = (preview?.items ?? []).filter(
    (i) => i.status === "move" || i.status === "same" || i.status === "rename",
  );

  const run = async () => {
    if (!preview || todo.length === 0 || preview.needsPolicy) return;
    setBusy(true);
    setProgress({ done: 0, total: todo.length });
    const counts = { moved: 0, skipped: 0 };
    try {
      for (let at = 0; at < todo.length; at += MOVE_BATCH) {
        // What the member previewed, so a memory that changed since stays where it is.
        const items = todo
          .slice(at, at + MOVE_BATCH)
          .map((i) => ({ name: i.name, newName: i.newName, status: i.status, revision: i.revision }));
        const res = await apiJSON("api/agents/memory/entries/project-move?project=" + encodeURIComponent(from), "POST", {
          from,
          to,
          collision: policy,
          items,
        });
        for (const r of (res?.results ?? []) as MoveResultItem[]) {
          if (r.result === "skipped") counts.skipped++;
          else if (r.result === "moved" || r.result === "merged" || r.result === "renamed") counts.moved++;
        }
        if (res?.error) {
          // A move that broke off still says what it did: finished ones are counted, the one it
          // stopped at has its copy in both projects, and the rest were not tried.
          // Not tried: what the Agent says it left in this batch, and every later batch not sent.
          const results = (res.results ?? []) as MoveResultItem[];
          const pending = ((res.pending as string[] | undefined) ?? []).length + Math.max(0, todo.length - (at + MOVE_BATCH));
          const stopped = results.find((r) => r.name === res.stopped);
          // Only a partial one has its copy in both projects; a failed one was never written.
          const key = stopped?.result === "partial" ? "mem.pm_stopped" : "mem.pm_stopped_failed";
          notify(
            res.stopped
              ? tr(key, { n: counts.moved, name: res.stopped, pending, error: errDetail(res.error) })
              : errDetail(res.error),
          );
          return;
        }
        setProgress({ done: Math.min(at + MOVE_BATCH, todo.length), total: todo.length });
      }
      notify(tr(counts.skipped ? "mem.pm_done_skipped" : "mem.pm_done", { n: counts.moved, skipped: counts.skipped }), {
        kind: counts.skipped ? undefined : "success",
      });
    } catch {
      // The request may or may not have reached the Agent: re-read what is there now.
      notify(tr("mem.pm_failed"));
    } finally {
      setBusy(false);
      setProgress(null);
      setAgain((n) => n + 1);
      onChanged();
      void loadPreview(from, to, policy);
    }
  };

  const statusLabel: Record<MoveStatus, string> = {
    move: tr("mem.pm_status_move"),
    same: tr("mem.pm_status_same"),
    rename: tr("mem.pm_status_rename"),
    collision: tr("mem.pm_status_collision"),
    skip: tr("mem.pm_status_skip"),
  };
  const step: WizardStep = busy ? "apply" : result ? "result" : preview ? "confirm" : from && to ? "preview" : "source";

  return (
    <WizardFrame title={tr("mem.pm_title")} intro={tr("mem.pm_intro")} step={step} busy={busy} result={result} onClose={onClose}>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {rows === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : orphans.length === 0 ? (
        !loadErr && <p className="muted">{tr("mem.pm_none")}</p>
      ) : (
        <>
          {suggested.length > 0 && (
            <ul className="mem-pm-suggest">
              {suggested.map((r) => (
                <li key={r.project.id}>
                  <span className="muted">
                    {tr("mem.pm_suggest", { from: r.project.display, to: r.mergeInto!.display })}
                  </span>{" "}
                  <Button disabled={busy} onClick={() => choose(r.project.id, r.mergeInto!.id, "")}>
                    {tr("mem.pm_suggest_review")}
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <label className="mem-scope">
            <span className="muted">{tr("mem.pm_from")}</span>
            <select value={from} disabled={busy} onChange={(e) => choose(e.target.value, to === e.target.value ? "" : to, "")}>
              <option value="">{tr("mem.pm_pick")}</option>
              {orphans.map((r) => (
                <option key={r.project.id} value={r.project.id}>
                  {label(r)}
                </option>
              ))}
            </select>
          </label>
          {from && (
            <label className="mem-scope">
              <span className="muted">{tr("mem.pm_to")}</span>
              <select value={to} disabled={busy} onChange={(e) => choose(from, e.target.value, "")}>
                <option value="">{tr("mem.pm_pick")}</option>
                {targets.map((r) => (
                  <option key={r.project.id} value={r.project.id}>
                    {label(r)}
                  </option>
                ))}
              </select>
            </label>
          )}
        </>
      )}
      {previewErr && <p className="mem-warn">{previewErr}</p>}
      {from && to && !preview && !previewErr && <p className="muted">{tr("common.loading")}</p>}
      {preview && (
        <>
          <p className="muted ds-hint">{tr("mem.pm_plan", { from: preview.from.display, to: preview.to.display })}</p>
          {(preview.needsPolicy || policy) && (
            <fieldset className="mem-pm-policy" disabled={busy}>
              <legend className="mem-warn">{tr("mem.pm_policy")}</legend>
              {POLICIES.map(([p, text]) => (
                <label key={p}>
                  <input type="radio" name="mem-pm-policy" value={p} checked={policy === p} onChange={() => choose(from, to, p)} />{" "}
                  {tr(text)}
                </label>
              ))}
            </fieldset>
          )}
          <GroupedPreview
            items={preview.items ?? []}
            order={STATUSES}
            statusOf={(i) => i.status}
            label={(st) => statusLabel[st as MoveStatus]}
            badge={(st) => "pm-" + st}
            open={(st) => st !== "same"}
            keyOf={(i) => i.name}
            renderItem={(i) => (
              <>
                <span className="mem-af-name">{i.name}</span>
                {i.newName && (
                  <span className="muted">
                    {" → "}
                    <span className="mem-af-name">{i.newName}</span>
                  </span>
                )}
              </>
            )}
          />
          {(preview.withheld ?? 0) > 0 && <p className="mem-warn">{tr("mem.pm_withheld", { n: preview.withheld ?? 0 })}</p>}
          <p className="muted ds-hint">{tr("mem.pm_undo")}</p>
          <div className="mem-ci-actions">
            <Button variant="primary" disabled={busy || preview.needsPolicy || todo.length === 0} onClick={() => void run()}>
              {tr("mem.pm_apply", { n: todo.length })}
            </Button>
            {progress && (
              <span className="muted" role="status">
                {tr("mem.ci_progress", { done: progress.done, total: progress.total })}
              </span>
            )}
          </div>
        </>
      )}
    </WizardFrame>
  );
}
