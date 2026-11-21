// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useViewShown } from "../../lib/viewShown.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { raw, errText, sessionDelete } from "../../core/api/client.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { askConfirm } from "../../ui/confirmBridge.ts";
import { toast } from "../../ui/toast.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useReposStore } from "../repos/store.ts";
import { StopSessionsModal } from "../repos/StopSessionsModal.tsx";
import { DeleteCopyModal } from "../repos/DeleteCopyModal.tsx";
import type { RepoTreeNode } from "../../lib/project.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat } from "../sessions/open.ts";
import { memoQueueDeliveries, memoQueueList } from "../memo/api.ts";
import { readDeliveries, readMemoQueues } from "../memo/queues.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { buildRuns, fmtElapsed, waitsForMember, type RunDelivery, type RunRow } from "./runs.ts";
import { ancestorNames, planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";
import "./autorun.css";

const POLL_MS = 30000;

/** Queues and their deliveries, read while the view is on screen and re-read when it comes back
 *  or the tenant or the signed-in account changes. Each category is read on its own, one read at
 *  a time, so a slow one holds nobody up, and an answer to a read the scope has outgrown is
 *  dropped. The API returns only the newest fifty deliveries, so a category read keeps what an
 *  earlier one learned about a run's category, but a run that fell off the list loses its
 *  state (the sessions' own word is used for it). */
function useQueueRuns(): { queues: MemoQueue[]; deliveries: Map<string, RunDelivery> } {
  const shown = useViewShown();
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const scopeKey = tenant + "\u0000" + identity;
  const [queues, setQueues] = useState<MemoQueue[]>([]);
  const [deliveries, setDeliveries] = useState<Map<string, RunDelivery>>(() => new Map());
  const scope = useRef(scopeKey);
  useEffect(() => {
    if (scope.current === scopeKey) return;
    scope.current = scopeKey;
    setQueues([]);
    setDeliveries(new Map());
  }, [scopeKey]);
  useEffect(() => {
    if (!shown) return;
    let live = true;
    let listing = false;
    const reading = new Set<string>();
    let current = new Set<string>(); // categories the latest queue list names
    // Bumped when a category leaves the list, so a read begun before cannot land after it returns.
    const epoch = new Map<string, number>();
    const readCategory = async (q: MemoQueue) => {
      if (reading.has(q.categoryId)) return;
      reading.add(q.categoryId);
      const mine = epoch.get(q.categoryId) ?? 0;
      try {
        const list = readDeliveries(await memoQueueDeliveries(q.categoryId));
        if (!live || !list || !current.has(q.categoryId) || (epoch.get(q.categoryId) ?? 0) !== mine) return;
        setDeliveries((prev) => {
          const next = new Map(prev);
          const seen = new Set(list.map((d) => d.session));
          // A run that fell off the newest-fifty list keeps its category and, once closed, its end;
          // an unsettled state is dropped (the sessions' own word is used for it).
          for (const [name, d] of prev) if (d.categoryId === q.categoryId && !seen.has(name) && d.state !== "closed") next.set(name, { ...d, state: "" });
          for (const d of list) {
            if (d.session) next.set(d.session, { categoryId: q.categoryId, state: d.state, sentAt: d.sentAt, createdAt: d.createdAt, closedAt: d.closedAt, heldBy: d.heldBy });
          }
          return next;
        });
      } catch {
        /* keep what this category had */
      } finally {
        if ((epoch.get(q.categoryId) ?? 0) === mine) reading.delete(q.categoryId);
      }
    };
    const load = async () => {
      if (listing) return;
      listing = true;
      try {
        const wire = readMemoQueues(await memoQueueList());
        if (!live || !wire) return;
        const now = new Set(wire.queues.map((q) => q.categoryId));
        for (const id of current) {
          if (now.has(id)) continue;
          epoch.set(id, (epoch.get(id) ?? 0) + 1);
          reading.delete(id);
        }
        current = now;
        setQueues(wire.queues);
        // A category that is gone is no longer read: whatever it left unsettled is dropped too.
        setDeliveries((prev) => {
          let next: Map<string, RunDelivery> | null = null;
          for (const [name, d] of prev) {
            if (current.has(d.categoryId) || d.state === "closed" || d.state === "") continue;
            next ??= new Map(prev);
            next.set(name, { ...d, state: "" });
          }
          return next ?? prev;
        });
        for (const q of wire.queues) void readCategory(q);
      } catch {
        /* keep the last read */
      } finally {
        listing = false;
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
  }, [shown, scopeKey]);
  return { queues, deliveries };
}

const STATE_KEY = { running: "autorun.state_running", waiting: "autorun.state_waiting", ended: "autorun.state_ended" } as const;

const PR_KEY = { open: "autorun.pr_open", merged: "autorun.pr_merged", closed: "autorun.pr_closed" } as const;

/** What an open dialog acts on, frozen when the menu item is chosen. The view owns the dialog
 *  (not the row) so it survives the row moving to "Ended" or the list refreshing under it. */
type RunDialog = { kind: "stop"; node: RepoTreeNode; only: Set<string> } | { kind: "delete"; node: RepoTreeNode };

/** The row's "..." menu: stop the run's sessions, delete its worktree. Both open the repository
 *  tree's own plan dialogs, which carry the confirmation. */
function RunMenu({ run, onDialog }: { run: RunRow; onDialog: (d: RunDialog) => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const stop = runStopScope(run, repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const wt = runWorktreeNode(run, repos, sessions);
  useDismiss([btnRef, menuRef], !!at, () => setAt(null));
  useMenuRoving(menuRef, !!at);
  useLayoutEffect(() => {
    if (at && menuRef.current) placeFixed(menuRef.current, at.x, at.y);
  });
  const open = () => {
    const r = btnRef.current?.getBoundingClientRect();
    setAt(r ? { x: r.left, y: r.bottom } : { x: 0, y: 0 });
  };
  return (
    <>
      <button
        type="button"
        ref={btnRef}
        className="autorun-run-menu"
        aria-label={tr("autorun.menu")}
        aria-haspopup="menu"
        aria-expanded={!!at}
        title={tr("autorun.menu")}
        onClick={() => (at ? setAt(null) : open())}
      >
        <Icon name="ellipsis" />
      </button>
      {at &&
        createPortal(
          <ul className="ui-menu" ref={menuRef} style={{ left: at.x, top: at.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" disabled={!stop} onClick={() => { setAt(null); if (stop) onDialog({ kind: "stop", ...stop }); }}>
                <Icon name="debug-stop" /> {tr("autorun.stop_run")}
              </button>
            </li>
            {wt && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setAt(null); if (wt) onDialog({ kind: "delete", node: wt }); }}>
                  <Icon name="trash" /> {tr("autorun.delete_worktree")}
                </button>
              </li>
            )}
          </ul>,
          document.body,
        )}
    </>
  );
}

function RunItem({ run, now, onDialog }: { run: RunRow; now: number; onDialog: (d: RunDialog) => void }) {
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
      <RunMenu run={run} onDialog={onDialog} />
      {children.length > 0 && (
        <ul className="autorun-kids">
          {children.map((c) => (
            <li key={c.name}>
              <button type="button" className="autorun-kid" data-rail-row="" onClick={() => openSessionChat(c.name)} title={tr("autorun.open", { name: c.name })}>
                <span className={"autorun-dot is-" + (waitsForMember(c) ? "waiting" : c.alive ? "running" : "ended")} aria-hidden="true" />
                <span className="autorun-title">{displayName(c)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function Category({ label, runs, now, onDialog }: { label: string; runs: RunRow[]; now: number; onDialog: (d: RunDialog) => void }) {
  const tr = useT();
  const [showEnded, setShowEnded] = useState(false);
  const live = runs.filter((r) => r.state !== "ended");
  const ended = runs.filter((r) => r.state === "ended");
  return (
    <section className="autorun-cat" data-autorun-category={label}>
      <h3 className="autorun-cat-head">{label}</h3>
      <ul className="autorun-list">
        {live.map((r) => (
          <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} />
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
                <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} />
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

/** "Clear finished runs": archives the stopped sessions of ended runs after one confirmation, then
 *  offers the worktrees of merged runs to the delete dialog (its own confirmation). */
function ClearFinished({ runs }: { runs: RunRow[] }) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  const [del, setDel] = useState<ReturnType<typeof planClearFinished>["node"]>(null);
  const [busy, setBusy] = useState(false);
  const runsRef = useRef(runs);
  runsRef.current = runs;
  // Read at the moment of use: a member can start a session in a candidate copy while the
  // confirmation is open or the archive runs, and that session must not be swept along.
  const latest = () => planClearFinished(runsRef.current, useSessionsStore.getState().sessions, useReposStore.getState().repos);
  const nothing = useMemo(() => {
    const p = planClearFinished(runs, sessions, repos);
    return p.archive.length === 0 && !p.node;
  }, [runs, sessions, repos]);
  const click = async () => {
    if (busy) return;
    if (latest().archive.length > 0) {
      const ok = await askConfirm({
        title: tr("autorun.clear"),
        body: tr("autorun.clear_confirm", { count: latest().archive.flat().length }),
        confirmLabel: tr("autorun.clear_do"),
      });
      if (!ok) return;
      setBusy(true);
      let archived = 0;
      let failed = 0;
      try {
        for (const group of latest().archive) {
          // Deepest first: a session whose archive failed keeps its ancestors, so the run stays
          // listed with everything below it and the next clear retries.
          const keep = new Set<string>();
          for (const s of group) {
            if (keep.has(s.name)) continue;
            const res = await (agentOf(s.kind).caps.ephemeral
              ? sessionDelete(s.name, { stop: true })
              : raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" })
            ).catch(() => null);
            if (res?.ok) {
              archived++;
              closeSessionPanes(s.name);
              continue;
            }
            failed++;
            const j = await res?.json().catch(() => null);
            toast(tr("autorun.clear_failed", { name: s.name, err: j?.error ? errText(j.error) : "" }));
            for (const n of ancestorNames(s.name, useSessionsStore.getState().sessions)) keep.add(n);
          }
        }
      } finally {
        setBusy(false);
        await useSessionsStore.getState().refresh();
      }
      if (archived > 0 && failed === 0) toast(tr("autorun.cleared", { count: archived }), { kind: "success" });
    }
    // Planned again from what the list says now, after the archive settled.
    const node = latest().node;
    if (node) setDel(node);
  };
  return (
    <>
      <div className="autorun-toolbar">
        <button type="button" className="autorun-clear" disabled={nothing || busy} onClick={() => void click()}>
          {tr("autorun.clear")}
        </button>
      </div>
      {del && (
        <DeleteCopyModal node={del} onClose={() => setDel(null)} onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })} />
      )}
    </>
  );
}

export const AutoRunSection = memo(function AutoRunSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const { queues, deliveries } = useQueueRuns();
  const [dialog, setDialog] = useState<RunDialog | null>(null);
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
  // A category the queue list no longer names (its settings were deleted) is "Other runs" too.
  const keyOf = (id: string) => (queues.some((q) => q.categoryId === id) ? id : "");
  const ids = [...new Set(runs.map((r) => keyOf(r.categoryId)))];
  // Rendered whether or not a run is left: deleting a run's last sessions empties the list while
  // the dialog is still showing its outcome.
  const dialogs = (
    <>
      {dialog?.kind === "stop" && (
        <StopSessionsModal
          node={dialog.node}
          only={dialog.only}
          onClose={() => setDialog(null)}
          onDone={(now, after) => {
            const parts = [
              ...(now > 0 ? [tr("rp.stop.done_now", { count: now })] : []),
              ...(after > 0 ? [tr("rp.stop.done_after", { count: after })] : []),
            ];
            toast(parts.join(tr("common.list_sep")), { kind: "success" });
          }}
        />
      )}
      {dialog?.kind === "delete" && (
        <DeleteCopyModal node={dialog.node} onClose={() => setDialog(null)} onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })} />
      )}
    </>
  );
  return (
    <div className="autorun-view">
      <ClearFinished runs={runs} />
      {runs.length === 0 && <div className="pane-empty">{tr("autorun.none")}</div>}
      {ids.map((id) => (
        <Category key={id} label={labelOf(id)} runs={runs.filter((r) => keyOf(r.categoryId) === id)} now={now} onDialog={setDialog} />
      ))}
      {dialogs}
    </div>
  );
});
