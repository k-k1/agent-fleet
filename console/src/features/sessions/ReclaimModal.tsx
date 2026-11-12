// ReclaimModal — the confirm behind the low-disk notice's "Clean regenerable items…" (#3880).
//
// It is a front for the cleanups that already exist, not a delete path of its own: build output
// in dormant worktrees goes through the same dry-run-then-POST as WorktreeOutputSection (#3800),
// and a tool-cache row through the same DELETE as Settings › Machine (#3803, #3851). What the
// list shows is what the Agent just measured for those calls, not the walk the notice's figure
// came from, so the confirm names exactly what will be removed and nothing is deleted on open.
//
// Nothing is removed until the confirm button; each item can be unticked first. Items run one
// at a time, and the Agent still refuses what it must (a live session, a build in use): that
// shows up in the summary as "skipped", not as a failure of the whole run.
import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { humanSize } from "../../lib/filemeta.ts";
import { emptyToolCache, loadToolCaches } from "../settings/workspace/toolCaches.ts";
import { applyWorktreeOutput, loadSurvey, sortReports, type WtoDir } from "./worktreeOutput.ts";

interface Item {
  id: string;
  kind: "worktree" | "cache";
  /** The worktree's name or the cache row's name: what the Agent is asked about. */
  name: string;
  bytes: number;
  dirs?: WtoDir[];
  truncated?: boolean;
}

type Phase = "loading" | "ready" | "failed" | "running";

export function ReclaimModal({ onClose, onChanged }: { onClose: () => void; onChanged?: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [phase, setPhase] = useState<Phase>("loading");
  const [items, setItems] = useState<Item[]>([]);
  const [off, setOff] = useState<Set<string>>(new Set());
  // One side failing (an older Agent without worktree-output, say) must not hide the other.
  const [partial, setPartial] = useState(false);
  // The list changed under the confirm; it was redrawn and nothing was deleted.
  const [stale, setStale] = useState(false);
  const alive = useRef(true);
  // Set synchronously: a second click in the same tick must not start a second run.
  const running = useRef(false);

  useEffect(() => {
    alive.current = true;
    (async () => {
      const found: Item[] = [];
      let missed = false;
      const survey = await loadSurvey();
      if (survey.kind === "ok") {
        for (const r of sortReports(survey.survey.worktrees)) {
          if (r.status !== "candidate" || !alive.current) continue;
          // The dry run is what the delete will re-derive, so list ITS directories.
          const dry = await applyWorktreeOutput(r.name, true);
          if (dry.kind === "report" && dry.report.status === "candidate" && dry.report.bytes > 0) {
            found.push({
              id: "wt:" + r.name,
              kind: "worktree",
              name: r.name,
              bytes: dry.report.bytes,
              dirs: dry.report.dirs,
              truncated: dry.report.truncated,
            });
          }
        }
      } else if (survey.kind === "error") missed = true;
      try {
        const tc = await loadToolCaches();
        for (const c of tc.caches) {
          // A row in use, or one the Agent offers nothing for, is not something this can take.
          if (c.bytes > 0 && !c.busy?.length && !c.skipped) {
            found.push({ id: "cache:" + c.name, kind: "cache", name: c.name, bytes: c.bytes });
          }
        }
      } catch {
        missed = true;
      }
      if (!alive.current) return;
      setItems(found);
      setPartial(missed);
      setPhase(found.length === 0 && missed ? "failed" : "ready");
    })();
    return () => {
      alive.current = false;
    };
  }, []);

  const chosen = items.filter((i) => !off.has(i.id));
  const total = chosen.reduce((n, i) => n + i.bytes, 0);

  const run = async () => {
    if (running.current || chosen.length === 0) return;
    running.current = true;
    setPhase("running");
    // The real POST names only the worktree, and the Agent deletes whatever it finds then, so a
    // directory that appeared since the list was drawn would go unseen. Re-read each dry run and
    // stop for a fresh look when the set differs. (A directory created between this check and the
    // POST is not excluded: that needs the Agent to match an expected set, which it cannot.)
    const changed: string[] = [];
    const next = new Map<string, Item>();
    for (const it of chosen) {
      if (it.kind !== "worktree") continue;
      const dry = await applyWorktreeOutput(it.name, true);
      const rels = (x?: WtoDir[]) => (x ?? []).map((d) => d.rel).sort().join("\n");
      if (dry.kind !== "report" || dry.report.status !== "candidate" || rels(dry.report.dirs) !== rels(it.dirs)) {
        changed.push(it.id);
        if (dry.kind === "report" && dry.report.status === "candidate" && dry.report.bytes > 0) {
          next.set(it.id, { ...it, bytes: dry.report.bytes, dirs: dry.report.dirs, truncated: dry.report.truncated });
        }
      }
    }
    if (changed.length > 0) {
      setItems((p) => p.filter((i) => !changed.includes(i.id) || next.has(i.id)).map((i) => next.get(i.id) ?? i));
      setStale(true);
      running.current = false;
      setPhase("ready");
      return;
    }
    setStale(false);
    let freed = 0;
    let skipped = 0;
    let failed = 0;
    for (const it of chosen) {
      if (it.kind === "worktree") {
        const res = await applyWorktreeOutput(it.name, false);
        if (res.kind === "error") failed++;
        else if (res.report.status === "refused") skipped++;
        else freed += res.report.bytes;
      } else {
        try {
          const res = await emptyToolCache(it.name);
          if (res?.error?.code === "cache_in_use") skipped++;
          else if (res?.error) failed++;
          else freed += res?.bytes ?? it.bytes;
        } catch {
          failed++;
        }
      }
    }
    onChanged?.();
    toast(tr("reclaim.done", { size: humanSize(freed), skipped, failed }), {
      kind: failed > 0 ? "warn" : "success",
      persist: true,
    });
    running.current = false;
    onClose();
  };

  const body =
    phase === "loading" ? (
      <p>{tr("reclaim.loading")}</p>
    ) : phase === "failed" ? (
      <p>{tr("reclaim.failed")}</p>
    ) : items.length === 0 ? (
      <p>{tr("reclaim.none")}</p>
    ) : (
      <>
        <p>{tr("reclaim.intro")}</p>
        <ul className="clean-list wto-list" data-testid="reclaim-items">
          {items.map((i) => (
            <li key={i.id} className="wto-row" data-id={i.id}>
              <label className="wto-head">
                <input
                  type="checkbox"
                  checked={!off.has(i.id)}
                  disabled={phase === "running"}
                  onChange={() =>
                    setOff((p) => {
                      const n = new Set(p);
                      if (!n.delete(i.id)) n.add(i.id);
                      return n;
                    })
                  }
                />
                <span className="clean-target">
                  {i.kind === "worktree" ? tr("reclaim.kind_worktree", { name: i.name }) : tr("reclaim.kind_cache", { name: i.name })}
                </span>
                <span className="clean-size">
                  {humanSize(i.bytes)}
                  {i.truncated ? "+" : ""}
                </span>
              </label>
              {i.dirs && (
                <ul className="wto-dirs">
                  {i.dirs.map((d) => (
                    <li key={d.rel}>
                      <code>{d.rel}</code>
                      <span className="clean-size">{humanSize(d.bytes)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
        {stale && <p className="wto-note" role="status">{tr("reclaim.stale")}</p>}
        {partial && <p className="wto-note">{tr("reclaim.partial")}</p>}
        <p className="wto-warn">{tr("reclaim.irreversible")}</p>
      </>
    );

  const canRun = phase === "ready" && chosen.length > 0;
  return (
    <ConfirmDialog
      title={tr("reclaim.title")}
      confirmLabel={canRun ? tr("reclaim.confirm", { size: humanSize(total) }) : tr("reclaim.confirm_none")}
      busy={phase === "running"}
      confirmDisabled={!canRun}
      onConfirm={run}
      onCancel={onClose}
    >
      {body}
    </ConfirmDialog>
  );
}
