// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useViewShown } from "../../lib/viewShown.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat } from "../sessions/open.ts";
import { memoQueueDeliveries, memoQueueList } from "../memo/api.ts";
import { readDeliveries, readMemoQueues } from "../memo/queues.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { buildRuns, fmtElapsed, type RunDelivery, type RunRow } from "./runs.ts";
import "./autorun.css";

const POLL_MS = 30000;

/** Queues and their deliveries, read while the view is on screen and re-read when it comes back
 *  or the tenant changes. Each category lands as it arrives (a slow one holds nobody up), and
 *  an answer to a read the tenant or the view has outgrown is dropped. The newest fifty
 *  deliveries are all the API returns, so what an earlier read learned about a run is kept. */
function useQueueRuns(): { queues: MemoQueue[]; deliveries: Map<string, RunDelivery> } {
  const shown = useViewShown();
  const tenant = useTenantStore((s) => s.tenant);
  const [queues, setQueues] = useState<MemoQueue[]>([]);
  const [deliveries, setDeliveries] = useState<Map<string, RunDelivery>>(() => new Map());
  const scope = useRef(tenant);
  useEffect(() => {
    if (scope.current === tenant) return;
    scope.current = tenant;
    setQueues([]);
    setDeliveries(new Map());
  }, [tenant]);
  useEffect(() => {
    if (!shown) return;
    let live = true;
    let busy = false;
    const load = async () => {
      if (busy) return;
      busy = true;
      try {
        const wire = readMemoQueues(await memoQueueList());
        if (!live || !wire) return;
        setQueues(wire.queues);
        await Promise.all(
          wire.queues.map(async (q) => {
            try {
              const list = readDeliveries(await memoQueueDeliveries(q.categoryId));
              if (!live || !list) return;
              setDeliveries((prev) => {
                const next = new Map(prev);
                for (const d of list) {
                  if (d.session) next.set(d.session, { categoryId: q.categoryId, state: d.state, sentAt: d.sentAt, createdAt: d.createdAt, closedAt: d.closedAt });
                }
                return next;
              });
            } catch {
              /* keep what this category had */
            }
          }),
        );
      } catch {
        /* keep the last read */
      } finally {
        busy = false;
      }
    };
    void load();
    const id = setInterval(() => {
      if (!document.hidden) void load();
    }, POLL_MS);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [shown, tenant]);
  return { queues, deliveries };
}

const STATE_KEY = { running: "autorun.state_running", waiting: "autorun.state_waiting", ended: "autorun.state_ended" } as const;

const PR_KEY = { open: "autorun.pr_open", merged: "autorun.pr_merged", closed: "autorun.pr_closed" } as const;

function RunItem({ run, now }: { run: RunRow; now: number }) {
  const tr = useT();
  const { root, item, pr, children } = run;
  const end = run.state === "ended" ? run.endedAt : now;
  const elapsed = Number.isFinite(run.startedAt) && Number.isFinite(end) ? fmtElapsed(end - run.startedAt) : "";
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
  const { queues, deliveries } = useQueueRuns();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);
  const runs = useMemo(() => buildRuns(sessions, deliveries), [sessions, deliveries]);
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
