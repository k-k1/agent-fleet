// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat } from "../sessions/open.ts";
import { memoQueueDeliveries, memoQueueList } from "../memo/api.ts";
import { readDeliveries, readMemoQueues } from "../memo/queues.ts";
import type { MemoDelivery, MemoQueue } from "../../types/memo.ts";
import { buildRuns, categoryIndex, fmtElapsed, type RunRow } from "./runs.ts";
import "./autorun.css";

const POLL_MS = 30000;

/** Queues and their deliveries, re-read while the view is mounted. A failed read keeps what was
 *  there; the rows themselves come from the session list, so a missing read costs only the
 *  category headings. */
function useQueueRuns(): { queues: MemoQueue[]; byCategory: Record<string, MemoDelivery[]> } {
  const [queues, setQueues] = useState<MemoQueue[]>([]);
  const [byCategory, setBy] = useState<Record<string, MemoDelivery[]>>({});
  const seq = useRef(0);
  const load = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const wire = readMemoQueues(await memoQueueList());
      if (!wire) return;
      const got = await Promise.all(
        wire.queues.map(async (q) => {
          try {
            return [q.categoryId, readDeliveries(await memoQueueDeliveries(q.categoryId))] as const;
          } catch {
            return [q.categoryId, null] as const;
          }
        }),
      );
      if (mine !== seq.current) return;
      setQueues(wire.queues);
      setBy((prev) => {
        const next: Record<string, MemoDelivery[]> = {};
        for (const [id, list] of got) next[id] = list ?? prev[id] ?? [];
        return next;
      });
    } catch {
      /* keep the last read */
    }
  }, []);
  useEffect(() => {
    void load();
    const id = setInterval(() => {
      if (!document.hidden) void load();
    }, POLL_MS);
    return () => {
      clearInterval(id);
      seq.current++;
    };
  }, [load]);
  return { queues, byCategory };
}

const STATE_KEY = { running: "autorun.state_running", waiting: "autorun.state_waiting", ended: "autorun.state_ended" } as const;

const PR_KEY = { open: "autorun.pr_open", merged: "autorun.pr_merged", closed: "autorun.pr_closed" } as const;

function RunItem({ run, now }: { run: RunRow; now: number }) {
  const tr = useT();
  const { root, item, pr, children } = run;
  const created = Date.parse(root.createdAt || "");
  const elapsed = !run.state.startsWith("ended") && Number.isFinite(created) ? fmtElapsed(now - created) : "";
  const title = item ? `${item.label} ${item.title || displayName(root)}`.trim() : displayName(root);
  return (
    <li className={"autorun-run is-" + run.state} data-autorun-run={root.name} data-autorun-state={run.state}>
      <button
        type="button"
        className="autorun-run-main"
        data-rail-row=""
        onClick={() => openSessionChat(root.name)}
        title={tr("autorun.open", { name: root.name })}
      >
        <span className={"autorun-dot is-" + run.state} role="img" aria-label={tr(STATE_KEY[run.state])} title={tr(STATE_KEY[run.state])} />
        <span className="autorun-title">{title}</span>
        {pr && (
          <span className={"autorun-pr " + (pr.draft && pr.state === "open" ? "draft" : pr.state)} title={tr("autorun.pr_title", { n: pr.number })}>
            #{pr.number} {pr.draft && pr.state === "open" ? tr("autorun.pr_draft") : tr(PR_KEY[pr.state])}
          </span>
        )}
        {elapsed && <span className="autorun-elapsed">{elapsed}</span>}
      </button>
      {children.length > 0 && (
        <ul className="autorun-kids">
          {children.map((c) => (
            <li key={c.name}>
              <button type="button" className="autorun-kid" data-rail-row="" onClick={() => openSessionChat(c.name)} title={tr("autorun.open", { name: c.name })}>
                <span className={"autorun-dot is-" + (c.alive ? "running" : "ended")} aria-hidden="true" />
                <span className="autorun-title">{displayName(c)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function Category({ label, runs, now }: { label: string; runs: RunRow[]; now: number }) {
  const tr = useT();
  const [showEnded, setShowEnded] = useState(false);
  const live = runs.filter((r) => r.state !== "ended");
  const ended = runs.filter((r) => r.state === "ended");
  return (
    <section className="autorun-cat" data-autorun-category={label}>
      <h3 className="autorun-cat-head">{label}</h3>
      <ul className="autorun-list">
        {live.map((r) => (
          <RunItem key={r.root.name} run={r} now={now} />
        ))}
      </ul>
      {ended.length > 0 && (
        <>
          <button type="button" className="autorun-ended-head" data-rail-row="" aria-expanded={showEnded} onClick={() => setShowEnded((v) => !v)}>
            <Icon name={showEnded ? "chevron-down" : "chevron-right"} />
            {tr("autorun.ended", { n: ended.length })}
          </button>
          {showEnded && (
            <ul className="autorun-list">
              {ended.map((r) => (
                <RunItem key={r.root.name} run={r} now={now} />
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

export const AutoRunSection = memo(function AutoRunSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const { queues, byCategory } = useQueueRuns();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);
  const runs = useMemo(() => buildRuns(sessions, categoryIndex(byCategory)), [sessions, byCategory]);
  const labelOf = (id: string): string => {
    const q = queues.find((x) => x.categoryId === id);
    return q ? [q.repo, q.category].filter(Boolean).join(" / ") || tr("memo.autorun") : tr("autorun.other");
  };
  const ids = [...new Set(runs.map((r) => r.categoryId))];
  if (runs.length === 0) return <div className="pane-empty">{tr("autorun.none")}</div>;
  return (
    <div className="autorun-view">
      {ids.map((id) => (
        <Category key={id} label={labelOf(id)} runs={runs.filter((r) => r.categoryId === id)} now={now} />
      ))}
    </div>
  );
});
